// Package jsonoutput owns the CLI JSON contract and its result, warning, and
// error encoding. Persisted link state has an independent schema version.
package jsonoutput

import (
	"encoding/json"
	"io"
	"maps"
)

const SchemaVersion = 3

// Warning carries a stable machine code and a readable diagnostic.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Write emits one newline-terminated JSON record. Map results receive the
// output schema version here; typed results carry their own schemaVersion field.
func Write(destination io.Writer, value any) error {
	if fields, ok := value.(map[string]any); ok {
		fields = maps.Clone(fields)
		fields["schemaVersion"] = SchemaVersion
		value = fields
	}
	encoder := json.NewEncoder(destination)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func WriteWarning(destination io.Writer, warning Warning) error {
	return Write(destination, struct {
		SchemaVersion int     `json:"schemaVersion"`
		Warning       Warning `json:"warning"`
	}{SchemaVersion, warning})
}

func WriteError(destination io.Writer, code, message string) error {
	return Write(destination, map[string]any{
		"ok":    false,
		"error": map[string]string{"code": code, "message": message},
	})
}
