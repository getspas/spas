// Package limits defines the repository and subprocess sizes supported by the
// SPAS current implementation. These are product limits, not Git limits. Keeping them together
// makes a future configuration contract possible without scattering policy.
package limits

import "time"

const (
	MaxCapturedGitStdoutBytes  = 16 << 20
	MaxCapturedGitStderrBytes  = 1 << 20
	StreamedGitDiagnosticBytes = 64 << 10
	GitCommandWaitDelay        = 2 * time.Second

	MaxPrivateTreeEntries       = 10_000
	MaxPrivateTreeMetadataBytes = MaxCapturedGitStdoutBytes
	MaxGitLFSPointerBytes       = 1024
)
