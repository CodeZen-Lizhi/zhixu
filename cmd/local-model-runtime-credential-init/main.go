// Command local-model-runtime-credential-init provisions the fixed runtime
// LOGIN role and writes its short-lived deployment credential to a project
// volume. It never prints the credential or includes it in argv.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	runtimeRole       = "zhixu_local_model_runtime"
	runtimeRoleMarker = "zhixu-managed-local-model-runtime/v1"
)

func main() {
	if err := run(context.Background(), os.LookupEnv, os.ReadFile, os.WriteFile, os.MkdirAll); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "local-model-runtime-credential-init: LOCAL_MODEL_RUNTIME_CREDENTIAL_INIT_FAILED")
		os.Exit(1)
	}
}

type lookupFunc func(string) (string, bool)
type readFileFunc func(string) ([]byte, error)
type writeFileFunc func(string, []byte, os.FileMode) error
type mkdirAllFunc func(string, os.FileMode) error
type chmodFunc func(string, os.FileMode) error
type chownFunc func(string, int, int) error
type roleStatusQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func run(ctx context.Context, lookup lookupFunc, readFile readFileFunc, writeFile writeFileFunc, mkdirAll mkdirAllFunc) error {
	if ctx == nil || lookup == nil || readFile == nil || writeFile == nil || mkdirAll == nil {
		return errors.New("credential initializer dependencies are invalid")
	}
	host, err := required(lookup, "ZHIXU_DATABASE_HOST")
	if err != nil {
		return err
	}
	port, _ := optional(lookup, "ZHIXU_DATABASE_PORT", "5432")
	database, err := required(lookup, "ZHIXU_DATABASE_NAME")
	if err != nil {
		return err
	}
	adminUser, err := required(lookup, "ZHIXU_DATABASE_USER")
	if err != nil {
		return err
	}
	adminPassword, err := required(lookup, "ZHIXU_DATABASE_PASSWORD")
	if err != nil {
		return err
	}
	credentialDir, _ := optional(lookup, "ZHIXU_LOCAL_MODEL_RUNTIME_CREDENTIAL_DIR", "/run/zhixu-local-model-runtime")
	if !safeComponent(host) || !safeComponent(port) || !safeComponent(database) || !safeComponent(adminUser) || filepath.IsAbs(credentialDir) == false || filepath.Clean(credentialDir) != credentialDir {
		return errors.New("credential initializer configuration is invalid")
	}
	if err := mkdirAll(credentialDir, 0o700); err != nil {
		return errors.New("create runtime credential directory")
	}
	// The volume root remains root-owned. Execute-only access lets the runtime
	// open its fixed 0600 files without listing any credential directory entry.
	if err := os.Chmod(credentialDir, 0o711); err != nil {
		return errors.New("set runtime credential directory mode")
	}
	userPath := filepath.Join(credentialDir, "database-user")
	passwordPath := filepath.Join(credentialDir, "database-password")
	runtimePassword, err := loadRuntimePassword(readFile, passwordPath)
	if err != nil {
		return err
	}
	connection := (&url.URL{Scheme: "postgres", Host: host + ":" + port, Path: "/" + database, User: url.UserPassword(adminUser, adminPassword)}).String() + "?sslmode=disable"
	conn, err := pgx.Connect(ctx, connection)
	if err != nil {
		return errors.New("connect database for runtime credential provisioning")
	}
	defer conn.Close(context.Background())
	if err := validateRuntimeRole(ctx, conn); err != nil {
		return err
	}
	// The same validated password is persisted before the role is enabled. A
	// provisioning failure can then be retried without creating split-brain
	// credentials, while an unsafe role never receives a password file.
	if err := writeFile(userPath, []byte(runtimeRole+"\n"), 0o600); err != nil {
		return errors.New("write runtime database user")
	}
	if err := writeFile(passwordPath, []byte(runtimePassword+"\n"), 0o600); err != nil {
		return errors.New("write runtime database password")
	}
	if err := secureCredentialFile(userPath, os.Chmod, os.Chown); err != nil {
		return errors.New("secure runtime database user")
	}
	if err := secureCredentialFile(passwordPath, os.Chmod, os.Chown); err != nil {
		return errors.New("secure runtime database password")
	}
	statement := "ALTER ROLE " + runtimeRole + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT -1 VALID UNTIL 'infinity' PASSWORD '" + runtimePassword + "'"
	if _, err := conn.Exec(ctx, statement); err != nil {
		return errors.New("provision runtime database role")
	}
	return nil
}

func secureCredentialFile(path string, chmod chmodFunc, chown chownFunc) error {
	if chmod == nil || chown == nil {
		return errors.New("credential file ownership dependencies are invalid")
	}
	// On a retry the file belongs to the non-root runtime UID. Reclaim it first
	// so the one-shot can enforce its mode without adding CAP_FOWNER.
	if err := chown(path, 0, 0); err != nil {
		return errors.New("reclaim credential file ownership")
	}
	if err := chmod(path, 0o600); err != nil {
		return errors.New("set credential file mode")
	}
	if err := chown(path, 10001, 10001); err != nil {
		return errors.New("assign credential file ownership")
	}
	return nil
}

// validateRuntimeRole accepts only the migration-marked capability role with
// the exact target-database ACLs required by the manager. It rejects ownership,
// role settings, membership, dangerous attributes, extra direct grants, and
// PUBLIC grants that expose protected ops data beyond the runtime contract.
func validateRuntimeRole(ctx context.Context, database roleStatusQuerier) error {
	if ctx == nil || database == nil {
		return errors.New("runtime database role validation is unavailable")
	}
	var valid bool
	err := database.QueryRow(ctx, `SELECT
		EXISTS (
			SELECT 1
			FROM pg_roles role
			WHERE role.rolname=$1
			  AND shobj_description(role.oid, 'pg_authid')=$2
				  AND NOT role.rolsuper AND NOT role.rolcreatedb
				  AND NOT role.rolcreaterole AND NOT role.rolreplication AND NOT role.rolbypassrls
				  AND NOT role.rolinherit
				  AND role.rolconfig IS NULL AND role.rolconnlimit=-1
				  AND (role.rolvaliduntil IS NULL OR role.rolvaliduntil='infinity'::timestamptz)
			)
			AND has_schema_privilege($1, 'ops', 'USAGE')
			AND has_table_privilege($1, 'ops.managed_ollama_runtime', 'SELECT')
			AND has_table_privilege($1, 'ops.managed_ollama_holds', 'SELECT')
			AND has_table_privilege($1, 'ops.managed_ollama_operations', 'SELECT')
			AND has_table_privilege($1, 'ops.managed_ollama_revision_requirements', 'SELECT')
			AND has_column_privilege($1, 'ops.model_settings_state', 'singleton', 'SELECT')
			AND has_column_privilege($1, 'ops.model_settings_state', 'phase', 'SELECT')
			AND has_column_privilege($1, 'ops.model_settings_state', 'active_revision', 'SELECT')
			AND has_column_privilege($1, 'ops.model_settings_state', 'target_revision', 'SELECT')
			AND has_column_privilege($1, 'ops.model_settings_state', 'previous_active_revision', 'SELECT')
			AND has_column_privilege($1, 'ops.model_settings_state', 'version', 'SELECT')
			AND has_function_privilege($1, 'ops.managed_ollama_claim_manager(uuid,interval,interval)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_heartbeat_manager(uuid,bigint,bigint,interval)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_publish_demand(uuid,bigint,bigint,bigint,text,jsonb,bigint)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_seed_active_recovery(uuid,bigint,bigint,interval)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_claim_operation(uuid,text,text,text,bigint,uuid,text,jsonb,uuid,bigint,interval)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_begin_pull_attempt(uuid,uuid,bigint,bigint,interval)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_record_operation_progress(uuid,uuid,bigint,bigint,text,jsonb,jsonb,bigint,bigint,boolean,interval,text)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_complete_operation(uuid,uuid,bigint,bigint,text,text,boolean,jsonb,bigint,bigint)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_expire_operations(uuid,bigint)', 'EXECUTE')
			AND has_function_privilege($1, 'ops.managed_ollama_compare_runtime_phase(uuid,bigint,bigint,text,text,text,text,bigint,text,boolean,bigint)', 'EXECUTE')
			AND NOT EXISTS (
			SELECT 1
			FROM pg_proc procedure
			JOIN pg_namespace namespace ON namespace.oid=procedure.pronamespace
			CROSS JOIN LATERAL aclexplode(COALESCE(procedure.proacl, acldefault('f', procedure.proowner))) privilege
			WHERE namespace.nspname='ops' AND procedure.prosecdef
			  AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
			)
			AND NOT EXISTS (
			SELECT 1 FROM pg_auth_members membership
			JOIN pg_roles role ON role.rolname=$1
			WHERE membership.member=role.oid OR membership.roleid=role.oid
			)
			AND NOT EXISTS (
			SELECT 1 FROM pg_db_role_setting setting
			JOIN pg_roles role ON role.rolname=$1
			WHERE setting.setrole=role.oid
			)
			AND NOT EXISTS (
			SELECT 1 FROM pg_database database
			JOIN pg_roles role ON role.rolname=$1
			WHERE database.datdba=role.oid
			)
			AND NOT EXISTS (
				SELECT 1 FROM pg_shdepend dependency
				JOIN pg_roles role ON role.rolname=$1
				JOIN pg_database current_database_row ON current_database_row.datname=current_database()
				WHERE dependency.refobjid=role.oid
				  AND (dependency.deptype<>'a' OR dependency.dbid<>current_database_row.oid)
			)
			AND NOT EXISTS (
			SELECT 1
			FROM pg_default_acl defaults
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(defaults.defaclacl, acldefault('r', defaults.defaclrole))) privilege
			WHERE privilege.grantee=role.oid
			)
			AND NOT EXISTS (
			SELECT 1
			FROM pg_database database
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(database.datacl, acldefault('d', database.datdba))) privilege
			WHERE privilege.grantee=role.oid
			UNION ALL
			SELECT 1
			FROM pg_namespace namespace
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(namespace.nspacl, acldefault('n', namespace.nspowner))) privilege
			WHERE (privilege.grantee=role.oid OR (privilege.grantee=0 AND namespace.nspname='ops'))
				  AND NOT (namespace.nspname='ops' AND privilege.privilege_type='USAGE' AND NOT privilege.is_grantable)
			UNION ALL
			SELECT 1
			FROM pg_class relation
			JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(relation.relacl, acldefault('r', relation.relowner))) privilege
			WHERE (privilege.grantee=role.oid OR (privilege.grantee=0 AND namespace.nspname='ops'))
			  AND NOT (namespace.nspname='ops'
			           AND relation.relname IN ('managed_ollama_runtime','managed_ollama_holds','managed_ollama_operations','managed_ollama_revision_requirements')
				           AND privilege.privilege_type='SELECT' AND NOT privilege.is_grantable)
			UNION ALL
			SELECT 1
			FROM pg_attribute attribute
			JOIN pg_class relation ON relation.oid=attribute.attrelid
			JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(attribute.attacl) privilege
			WHERE (privilege.grantee=role.oid OR (privilege.grantee=0 AND namespace.nspname='ops'))
			  AND NOT (namespace.nspname='ops' AND relation.relname='model_settings_state'
			           AND attribute.attname IN ('singleton','phase','active_revision','target_revision','previous_active_revision','version')
				           AND privilege.privilege_type='SELECT' AND NOT privilege.is_grantable)
			UNION ALL
			SELECT 1
			FROM pg_proc procedure
			JOIN pg_namespace namespace ON namespace.oid=procedure.pronamespace
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(procedure.proacl, acldefault('f', procedure.proowner))) privilege
				WHERE privilege.grantee=role.oid
				  AND NOT (namespace.nspname='ops' AND privilege.privilege_type='EXECUTE' AND NOT privilege.is_grantable
			           AND procedure.oid IN (
			               'ops.managed_ollama_claim_manager(uuid,interval,interval)'::regprocedure,
			               'ops.managed_ollama_heartbeat_manager(uuid,bigint,bigint,interval)'::regprocedure,
			               'ops.managed_ollama_publish_demand(uuid,bigint,bigint,bigint,text,jsonb,bigint)'::regprocedure,
			               'ops.managed_ollama_seed_active_recovery(uuid,bigint,bigint,interval)'::regprocedure,
			               'ops.managed_ollama_claim_operation(uuid,text,text,text,bigint,uuid,text,jsonb,uuid,bigint,interval)'::regprocedure,
			               'ops.managed_ollama_begin_pull_attempt(uuid,uuid,bigint,bigint,interval)'::regprocedure,
			               'ops.managed_ollama_record_operation_progress(uuid,uuid,bigint,bigint,text,jsonb,jsonb,bigint,bigint,boolean,interval,text)'::regprocedure,
			               'ops.managed_ollama_complete_operation(uuid,uuid,bigint,bigint,text,text,boolean,jsonb,bigint,bigint)'::regprocedure,
			               'ops.managed_ollama_expire_operations(uuid,bigint)'::regprocedure,
			               'ops.managed_ollama_compare_runtime_phase(uuid,bigint,bigint,text,text,text,text,bigint,text,boolean,bigint)'::regprocedure
			           ))
			UNION ALL
			SELECT 1
			FROM pg_type type
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(type.typacl, acldefault('T', type.typowner))) privilege
			WHERE privilege.grantee=role.oid
			UNION ALL
			SELECT 1
			FROM pg_language language
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(language.lanacl, acldefault('l', language.lanowner))) privilege
			WHERE privilege.grantee=role.oid
			UNION ALL
			SELECT 1
			FROM pg_foreign_data_wrapper wrapper
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(wrapper.fdwacl, acldefault('F', wrapper.fdwowner))) privilege
			WHERE privilege.grantee=role.oid
			UNION ALL
			SELECT 1
			FROM pg_foreign_server server
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(server.srvacl, acldefault('S', server.srvowner))) privilege
			WHERE privilege.grantee=role.oid
			UNION ALL
			SELECT 1
			FROM pg_tablespace tablespace
			JOIN pg_roles role ON role.rolname=$1
				CROSS JOIN LATERAL aclexplode(COALESCE(tablespace.spcacl, acldefault('t', tablespace.spcowner))) privilege
			WHERE privilege.grantee=role.oid
			UNION ALL
			SELECT 1
			FROM pg_largeobject_metadata large_object
			JOIN pg_roles role ON role.rolname=$1
			CROSS JOIN LATERAL aclexplode(COALESCE(large_object.lomacl, acldefault('L', large_object.lomowner))) privilege
			WHERE privilege.grantee=role.oid
			UNION ALL
			SELECT 1
			FROM pg_parameter_acl parameter_acl
			JOIN pg_roles role ON role.rolname=$1
				CROSS JOIN LATERAL aclexplode(COALESCE(parameter_acl.paracl, '{}'::aclitem[])) privilege
			WHERE privilege.grantee=role.oid
		)`, runtimeRole, runtimeRoleMarker).Scan(&valid)
	if err != nil {
		return errors.New("read runtime database role")
	}
	if !valid {
		return errors.New("runtime database role privileges are not trusted")
	}
	return nil
}

func loadRuntimePassword(readFile readFileFunc, path string) (string, error) {
	existing, err := readFile(path)
	if err == nil {
		defer clear(existing)
		if password, ok := parseRuntimePassword(existing); ok {
			return password, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("read runtime database password")
	}
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return "", errors.New("generate runtime credential")
	}
	defer clear(password)
	return hex.EncodeToString(password), nil
}

func parseRuntimePassword(data []byte) (string, bool) {
	if len(data) == 65 && data[64] == '\n' {
		data = data[:64]
	}
	if len(data) != 64 {
		return "", false
	}
	for _, value := range data {
		if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			return "", false
		}
	}
	return string(data), true
}

func required(lookup lookupFunc, key string) (string, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func optional(lookup lookupFunc, key, fallback string) (string, bool) {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return value, true
	}
	return fallback, false
}

func safeComponent(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}
