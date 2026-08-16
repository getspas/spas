package githubref

import (
	"testing"

	"github.com/getspas/spas/internal/provider"
)

// SSH URLs must reject query strings and fragments exactly like HTTPS URLs:
// the suffix would otherwise be silently discarded.
func TestParseRejectsSSHQueryAndFragment(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"ssh://git@github.com/acme/secrets.git#refs/heads/prod",
		"ssh://git@github.com/acme/secrets.git?fork=1",
	}
	for _, input := range inputs {
		if _, err := (Provider{}).Resolve(provider.RepositoryRequest{Raw: input}); err == nil {
			t.Errorf("Resolve(%q) error = nil, want query/fragment rejection", input)
		}
	}
	if _, err := (Provider{}).Resolve(provider.RepositoryRequest{Raw: "ssh://git@github.com/acme/secrets.git"}); err != nil {
		t.Fatalf("Resolve(plain ssh) error = %v", err)
	}
}
