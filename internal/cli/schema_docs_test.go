package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/getspas/spas/internal/jsonoutput"
	"github.com/getspas/spas/internal/linkstate"
)

// Documentation is part of the public JSON contract. These checks run with the
// normal Go suite so version changes cannot leave current examples behind.
func TestSchemaDocumentationVersions(t *testing.T) {
	t.Parallel()
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	overview := read("wiki/Schemas.md")
	for _, schema := range []struct {
		name    string
		version int
	}{{"Link state", linkstate.SchemaVersion}, {"CLI JSON output", jsonoutput.SchemaVersion}} {
		pattern := regexp.MustCompile(`(?m)^\| ` + regexp.QuoteMeta(schema.name) + ` \| ([0-9]+) \|`)
		match := pattern.FindStringSubmatch(overview)
		if len(match) != 2 || match[1] != strconv.Itoa(schema.version) {
			t.Errorf("overview version for %s = %v, want %d", schema.name, match, schema.version)
		}
	}
	blocks := regexp.MustCompile("(?s)```json[ \\t]*\\r?\\n(.*?)\\r?\\n```")
	current := regexp.MustCompile("(?m)^- \\*\\*Current Version:\\*\\* `([0-9]+)`$")
	match := current.FindStringSubmatch(read("wiki/JSON-output-schema.md"))
	if len(match) != 2 || match[1] != strconv.Itoa(jsonoutput.SchemaVersion) {
		t.Errorf("JSON reference current version = %v, want %d", match, jsonoutput.SchemaVersion)
	}
	for _, name := range []string{"wiki/JSON-output-schema.md", "wiki/Command-reference.md"} {
		t.Run(name, func(t *testing.T) {
			matches := blocks.FindAllStringSubmatch(read(name), -1)
			if len(matches) == 0 {
				t.Fatal("no JSON contract examples found")
			}
			for index, match := range matches {
				var payload map[string]json.RawMessage
				if err := json.Unmarshal([]byte(strings.TrimSpace(match[1])), &payload); err != nil {
					t.Errorf("example %d: %v", index+1, err)
					continue
				}
				var version int
				if err := json.Unmarshal(payload["schemaVersion"], &version); err != nil || version != jsonoutput.SchemaVersion {
					t.Errorf("example %d: schemaVersion = %s, want %d", index+1, payload["schemaVersion"], jsonoutput.SchemaVersion)
				}
			}
		})
	}
}
