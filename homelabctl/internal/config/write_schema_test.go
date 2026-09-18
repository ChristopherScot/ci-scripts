package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The schema file is a cache of a constant, not service content, so a
// stale copy must not survive. This is the case that motivated it: a
// service scaffolded by an older homelabctl has a schema describing
// fields the tool no longer has.
func TestWriteSchemaReplacesAStaleCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SchemaFileName)
	if err := os.WriteFile(path, []byte(`{"stale": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSchema(dir); err != nil {
		t.Fatalf("WriteSchema() = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != Schema {
		t.Error("a stale schema survived; editors would validate against the wrong fields")
	}
}

func TestWriteSchemaCreatesItWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSchema(dir); err != nil {
		t.Fatalf("WriteSchema() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, SchemaFileName)); err != nil {
		t.Errorf("schema not written: %v", err)
	}
}
