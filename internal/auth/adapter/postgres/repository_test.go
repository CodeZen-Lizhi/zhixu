package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDecodeScopesRejectsNonCanonicalPersistedJSON(t *testing.T) {
	canonical := `["READ_LOCAL","WRITE_PROPOSAL"]`
	decoded, err := decodeScopes(canonical)
	if err != nil || len(decoded) != 2 || decoded[0] != capability.ReadLocal || decoded[1] != capability.WriteProposal {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	for _, raw := range []string{
		`["WRITE_PROPOSAL","READ_LOCAL"]`,
		`["READ_LOCAL","READ_LOCAL"]`,
		`["NOT_A_SCOPE"]`,
	} {
		if _, err := decodeScopes(raw); err == nil {
			t.Fatalf("non-canonical scopes accepted: %s", raw)
		}
	}
}

func TestAPITokenListQualifiesUUIDKeysetAndSortColumns(t *testing.T) {
	database := &queryCaptureDB{}
	repository, err := newAuthTestRepository(t, database)
	if err != nil {
		t.Fatal(err)
	}
	cursorTime := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	_, _, err = repository.ListAPITokens(context.Background(), domain.APITokenListQuery{
		CursorTime: &cursorTime,
		CursorID:   foundation.ID("a7100000-0000-4000-8000-000000000001"),
		Limit:      domain.MaxAPITokenListLimit,
	})
	if err == nil {
		t.Fatal("captured list query unexpectedly succeeded")
	}
	if !strings.Contains(database.query, "WHERE (token.created_at,token.id) < ($1,$2::uuid)") {
		t.Fatalf("api token keyset columns are not qualified: %s", database.query)
	}
	if !strings.Contains(database.query, "ORDER BY token.created_at DESC,token.id DESC") {
		t.Fatalf("api token sort columns are not qualified: %s", database.query)
	}
}

func TestRevokeMissingAPITokenReturnsNotFound(t *testing.T) {
	repository, err := newAuthTestRepository(t, &zeroRowsDB{})
	if err != nil {
		t.Fatal(err)
	}
	err = repository.RevokeAPIToken(context.Background(), "a7100000-0000-4000-8000-000000000099")
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorNotFound || classified.Code != application.ErrorCodeAPITokenNotFound {
		t.Fatalf("missing api token revocation error=%v", err)
	}
}

func TestCheckVerifiesBothCredentialTables(t *testing.T) {
	database := &checkDB{}
	repository, err := newAuthTestRepository(t, database)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(database.query, "auth.session") || !strings.Contains(database.query, "auth.api_token") {
		t.Fatalf("credential table readiness query=%s", database.query)
	}

	database.err = errors.New("relation does not exist")
	err = repository.Check(context.Background())
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorDependencyUnavailable || classified.Code != application.ErrorCodeUnavailable {
		t.Fatalf("authentication readiness error=%v", err)
	}
}

func newAuthTestRepository(t *testing.T, connection driver.Conn) (*GORMRepository, error) {
	t.Helper()
	database := sql.OpenDB(authTestConnector{connection: connection})
	t.Cleanup(func() { _ = database.Close() })
	root, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: database}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Discard,
	})
	if err != nil {
		return nil, err
	}
	return NewGORMRepository(root)
}

type authTestConnector struct{ connection driver.Conn }

func (connector authTestConnector) Connect(context.Context) (driver.Conn, error) {
	return connector.connection, nil
}

func (connector authTestConnector) Driver() driver.Driver { return connector }

func (connector authTestConnector) Open(string) (driver.Conn, error) {
	return connector.connection, nil
}

type authTestConnection struct{}

func (authTestConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}

func (authTestConnection) Close() error { return nil }

func (authTestConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected begin")
}

type queryCaptureDB struct {
	authTestConnection
	query string
}

func (database *queryCaptureDB) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	database.query = query
	return nil, errors.New("stop after capturing query")
}

type zeroRowsDB struct{ authTestConnection }

func (*zeroRowsDB) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

type checkDB struct {
	authTestConnection
	query string
	err   error
}

func (database *checkDB) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	database.query = query
	if database.err != nil {
		return nil, database.err
	}
	return &checkRow{}, nil
}

type checkRow struct{ read bool }

func (*checkRow) Columns() []string { return []string{"session_exists", "token_exists"} }

func (*checkRow) Close() error { return nil }

func (row *checkRow) Next(destinations []driver.Value) error {
	if row.read {
		return io.EOF
	}
	row.read = true
	if len(destinations) != 2 {
		return errors.New("unexpected scan destination")
	}
	for index := range destinations {
		destinations[index] = false
	}
	return nil
}
