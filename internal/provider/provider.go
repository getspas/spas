package provider

import (
	"context"

	"github.com/getspas/spas/internal/gitexec"
)

// ID is the stable identifier persisted for a repository provider.
type ID string

// Transport identifies the provider-specific Git transport selected by the
// user.
type Transport string

const (
	HTTPS Transport = "https"
	SSH   Transport = "ssh"
)

// RepositoryRequest is the user-supplied repository reference and optional
// transport selection.
type RepositoryRequest struct {
	Raw       string
	Transport Transport
}

// RepositoryRef is the complete immutable repository identity produced by a
// provider.
type RepositoryRef struct {
	Provider  ID
	Canonical string
	Transport Transport
	RemoteURL string
}

// RepositoryProvider validates and translates repository references.
type RepositoryProvider interface {
	ID() ID
	Resolve(RepositoryRequest) (RepositoryRef, error)
	ProbePublic(context.Context, gitexec.Runner, RepositoryRef) (bool, error)
}
