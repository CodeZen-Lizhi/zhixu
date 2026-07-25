package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	repository, err := NewRepository(database)
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
	if !strings.Contains(database.query, "WHERE (token.created_at,token.id)<($1,$2::uuid)") {
		t.Fatalf("api token keyset columns are not qualified: %s", database.query)
	}
	if !strings.Contains(database.query, "ORDER BY token.created_at DESC,token.id DESC") {
		t.Fatalf("api token sort columns are not qualified: %s", database.query)
	}
}

func TestRevokeMissingAPITokenReturnsNotFound(t *testing.T) {
	repository, err := NewRepository(&zeroRowsDB{})
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
	repository, err := NewRepository(database)
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

type queryCaptureDB struct{ query string }

func (database *queryCaptureDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected exec")
}

func (database *queryCaptureDB) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	database.query = query
	return nil, errors.New("stop after capturing query")
}

func (database *queryCaptureDB) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("unexpected query row")
}

type zeroRowsDB struct{}

func (*zeroRowsDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 0"), nil
}

func (*zeroRowsDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (*zeroRowsDB) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("unexpected query row")
}

type checkDB struct {
	query string
	err   error
}

func (*checkDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected exec")
}

func (*checkDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (database *checkDB) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	database.query = query
	return checkRow{err: database.err}
}

type checkRow struct{ err error }

func (row checkRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for _, destination := range destinations {
		value, ok := destination.(*bool)
		if !ok {
			return errors.New("unexpected scan destination")
		}
		*value = false
	}
	return nil
}
