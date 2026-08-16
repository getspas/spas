package githubref

import (
	"context"
	"os/exec"
	"testing"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/provider"
)

func TestProviderResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		transport provider.Transport
		want      provider.RepositoryRef
	}{
		{"slug", "getspas/private-files", provider.HTTPS, provider.RepositoryRef{Provider: ID, Canonical: "getspas/private-files", Transport: provider.HTTPS, RemoteURL: "https://github.com/getspas/private-files.git"}},
		{"https", "https://github.com/getspas/private-files.git", "", provider.RepositoryRef{Provider: ID, Canonical: "getspas/private-files", Transport: provider.HTTPS, RemoteURL: "https://github.com/getspas/private-files.git"}},
		{"ssh", "git@github.com:getspas/private-files.git", "", provider.RepositoryRef{Provider: ID, Canonical: "getspas/private-files", Transport: provider.SSH, RemoteURL: "git@github.com:getspas/private-files.git"}},
		{"ssh URL", "ssh://git@github.com/getspas/private-files.git", "", provider.RepositoryRef{Provider: ID, Canonical: "getspas/private-files", Transport: provider.SSH, RemoteURL: "git@github.com:getspas/private-files.git"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := (Provider{}).Resolve(provider.RepositoryRequest{Raw: test.input, Transport: test.transport})
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Resolve() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseRejectsCredentialsAndOtherHosts(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"https://token@github.com/getspas/private-files.git",
		"https://github.com:8443/getspas/private-files.git",
		"https://gitlab.com/getspas/private-files.git",
		"ssh://github.com/getspas/private-files.git",
		"ssh://git@github.com:2222/getspas/private-files.git",
		"ssh://user:password@github.com/getspas/private-files.git",
	} {
		if _, err := (Provider{}).Resolve(provider.RepositoryRequest{Raw: value}); err == nil {
			t.Errorf("Resolve(%q) error = nil, want non-nil", value)
		}
	}
}

func TestProbePublic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	git := gitexec.Runner{}

	// Empty ref
	isPublic, err := (Provider{}).ProbePublic(ctx, git, provider.RepositoryRef{})
	if err != nil || isPublic {
		t.Fatalf("ProbePublic(empty) = %v, %v, want false, nil", isPublic, err)
	}

	// Nonexistent repo on GitHub
	isPublic, err = (Provider{}).ProbePublic(ctx, git, provider.RepositoryRef{
		Provider:  ID,
		Canonical: "getspas/nonexistent-private-repo-123456789",
		RemoteURL: "https://github.com/getspas/nonexistent-private-repo-123456789.git",
	})
	if err != nil || isPublic {
		t.Fatalf("ProbePublic(nonexistent) = %v, %v, want false, nil", isPublic, err)
	}

	// Local bare repo (readable without credentials)
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-q", dir+"/public.git")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	isPublic, err = (Provider{}).ProbePublic(ctx, git, provider.RepositoryRef{
		Provider:  ID,
		Canonical: "local/public",
		RemoteURL: "file://" + dir + "/public.git",
	})
	if err != nil || !isPublic {
		t.Fatalf("ProbePublic(local repo) = %v, %v, want true, nil", isPublic, err)
	}

	// Canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = (Provider{}).ProbePublic(canceledCtx, git, provider.RepositoryRef{
		Provider:  ID,
		Canonical: "local/public",
		RemoteURL: "file://" + dir + "/public.git",
	})
	if err == nil {
		t.Fatal("ProbePublic(canceled) error = nil, want context error")
	}
}
