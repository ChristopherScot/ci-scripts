package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// SchemaFileName is where a service keeps its copy of Schema, and what
// the `# yaml-language-server: $schema=` line in config.yaml points at.
const SchemaFileName = "config.schema.json"

// WriteSchema refreshes a service's copy of Schema.
//
// The file is a cache of a constant compiled into this binary, not
// something a service authors: it is byte-identical in every repo, and
// it exists only so an editor can resolve a relative $schema= path.
// Load validates against Schema directly and never reads it.
//
// So it is written unconditionally, where every other scaffolded file is
// skipped if it exists. A copy left behind by an older homelabctl
// describes fields the tool no longer has and omits the ones it gained -
// and the failure is quiet: the editor stops flagging a typo the CLI
// still rejects, or flags a field that is now valid.
//
// Writing it is not the same as writing a config: nothing here is the
// user's, so there is nothing to preserve.
func WriteSchema(dir string) error {
	path := filepath.Join(dir, SchemaFileName)
	if current, err := os.ReadFile(path); err == nil && string(current) == Schema {
		return nil
	}
	if err := os.WriteFile(path, []byte(Schema), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
