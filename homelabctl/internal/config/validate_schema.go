package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"
)

// compiled is the parsed Schema. Compiling it costs a few milliseconds
// and the schema never changes at runtime, so do it once rather than per
// Load - and panic on failure, since a malformed Schema is a build-time
// mistake that TestSchemaCompiles catches.
var compiled = mustCompileSchema()

func mustCompileSchema() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(Schema))
	if err != nil {
		panic(fmt.Sprintf("config.Schema is not valid JSON: %v", err))
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaFile, doc); err != nil {
		panic(fmt.Sprintf("config.Schema: %v", err))
	}
	s, err := c.Compile(schemaFile)
	if err != nil {
		panic(fmt.Sprintf("config.Schema does not compile: %v", err))
	}
	return s
}

const schemaFile = "config.schema.json"

// printer renders a validation kind's message. The library takes a
// *message.Printer and dereferences it, so nil panics - it wants a real
// one even when there is nothing to localize.
var printer = message.NewPrinter(language.English)

// validateAgainstSchema checks the raw YAML against Schema before it is
// decoded into a Config.
//
// The point is that the schema is no longer only documentation for an
// editor. Every constraint it states - the name pattern, replicas'
// minimum, port's range, the kind enum, additionalProperties false at
// every level - is now enforced by the CLI too, so the editor and the
// tool cannot disagree about what a valid config is.
//
// It runs on the raw document rather than the decoded struct because a
// constraint like `minimum: 1` is meaningless after defaulting: by then
// an omitted field and a rejected one look identical. Validate() still
// owns everything a schema cannot express - the cross-field rules, like
// a cronjob requiring a schedule and refusing an ingress.
func validateAgainstSchema(b []byte) error {
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		// Malformed YAML is reported by the decoder in Load, with better
		// position information than this would give.
		return nil
	}
	if doc == nil {
		// An empty file. Validate reports the missing required fields by
		// name, which is more useful than a schema's "missing property".
		return nil
	}

	err := compiled.Validate(doc)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	return fmt.Errorf("invalid config:\n%s", strings.Join(schemaProblems(ve), "\n"))
}

// schemaProblems flattens the validation tree into one sorted line per
// problem, so a config with three mistakes reports three lines instead
// clarifyBound rewrites the library's phrasing for a range violation.
//
// A `minimum: 1` failure prints "minimum: got -1, want 1", which reads
// as "1 is the only valid value". It is not - replicas: 2 is fine, and
// somebody checking that wording goes looking for a constraint that
// does not exist.
//
// Only the wording changes; the schema decides what is valid.
func clarifyBound(msg string) string {
	switch {
	case strings.HasPrefix(msg, "minimum: "):
		return strings.Replace(msg, ", want ", ", want at least ", 1)
	case strings.HasPrefix(msg, "maximum: "):
		return strings.Replace(msg, ", want ", ", want at most ", 1)
	}
	return msg
}

// of the nested causes the library returns.
func schemaProblems(ve *jsonschema.ValidationError) []string {
	var out []string
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := e.InstanceLocation
			where := strings.TrimPrefix(strings.Join(loc, "."), ".")
			if where == "" {
				where = "config"
			}
			out = append(out, fmt.Sprintf("  - %s: %s", where,
				clarifyBound(e.ErrorKind.LocalizedString(printer))))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	sort.Strings(out)
	return out
}
