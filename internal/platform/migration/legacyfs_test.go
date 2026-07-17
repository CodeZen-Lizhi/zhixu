package migration

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLegacyAnnotationFSWrapsDollarQuotedDirectionsOnly(t *testing.T) {
	base := fstest.MapFS{
		"00001_plain.sql":    {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 2;\n")},
		"00002_function.sql": {Data: []byte("-- +goose Up\nCREATE FUNCTION test() RETURNS void AS $$ BEGIN RETURN; END; $$ LANGUAGE plpgsql;\nSELECT 1;\n-- +goose Down\nDROP FUNCTION test();\n")},
		"00011_future.sql":   {Data: []byte("-- +goose Up\nCREATE FUNCTION future() RETURNS void AS $$ BEGIN RETURN; END; $$ LANGUAGE plpgsql;\n-- +goose Down\nDROP FUNCTION future();\n")},
	}
	wrapped, err := NewLegacyAnnotationFS(base)
	if err != nil {
		t.Fatal(err)
	}

	plain := readMigrationFile(t, wrapped, "00001_plain.sql")
	if plain != string(base["00001_plain.sql"].Data) {
		t.Fatalf("plain migration changed:\n%s", plain)
	}
	future := readMigrationFile(t, wrapped, "00011_future.sql")
	if future != string(base["00011_future.sql"].Data) {
		t.Fatalf("future migration changed:\n%s", future)
	}

	got := readMigrationFile(t, wrapped, "00002_function.sql")
	want := "-- +goose Up\n-- +goose StatementBegin\nCREATE FUNCTION test() RETURNS void AS $$ BEGIN RETURN; END; $$ LANGUAGE plpgsql;\nSELECT 1;\n-- +goose StatementEnd\n-- +goose Down\nDROP FUNCTION test();\n"
	if got != want {
		t.Fatalf("wrapped migration:\n%s\nwant:\n%s", got, want)
	}
}

func TestLegacyAnnotationFSPreservesSQLAfterRemovingInjectedAnnotations(t *testing.T) {
	original := "-- +goose Up\nCREATE FUNCTION test() RETURNS void AS $$\nBEGIN\n  PERFORM 1;\nEND;\n$$ LANGUAGE plpgsql;\n-- +goose Down\nDROP FUNCTION test();\n"
	wrapped, err := NewLegacyAnnotationFS(fstest.MapFS{"00010_test.sql": {Data: []byte(original)}})
	if err != nil {
		t.Fatal(err)
	}
	got := readMigrationFile(t, wrapped, "00010_test.sql")
	got = strings.ReplaceAll(got, "-- +goose StatementBegin\n", "")
	got = strings.ReplaceAll(got, "-- +goose StatementEnd\n", "")
	if got != original {
		t.Fatalf("SQL content changed:\n%s", got)
	}
}

func TestNewLegacyAnnotationFSRejectsNil(t *testing.T) {
	if _, err := NewLegacyAnnotationFS(nil); err == nil {
		t.Fatal("nil filesystem was accepted")
	}
}

func readMigrationFile(t *testing.T, filesystem fs.FS, name string) string {
	t.Helper()
	content, err := fs.ReadFile(filesystem, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
