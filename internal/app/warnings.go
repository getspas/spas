package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/getspas/spas/internal/provider"
)

// Warning carries a stable machine code and a readable diagnostic.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a App) warn(warning Warning) error {
	if a.Err == nil {
		return nil
	}
	if a.JSON {
		return json.NewEncoder(a.Err).Encode(struct {
			SchemaVersion int     `json:"schemaVersion"`
			Warning       Warning `json:"warning"`
		}{JSONSchemaVersion, warning})
	}
	_, err := fmt.Fprintln(a.Err, "warning:", warning.Message)
	return err
}

func (a App) warnPublicRepository(ctx context.Context, ref provider.RepositoryRef) error {
	readable, err := a.Provider.ProbePublic(ctx, a.Git, ref)
	if err != nil {
		return err
	}
	if !readable {
		return nil
	}
	return a.warn(Warning{
		Code:    "public_repository",
		Message: fmt.Sprintf("Repository %q is publicly readable. Anyone may read managed assets stored there.", ref.Canonical),
	})
}
