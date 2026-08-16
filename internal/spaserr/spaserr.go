// Package spaserr defines the typed errors behind SPAS's documented exit
// codes, machine-readable error codes, and remediation commands.
package spaserr

import "errors"

// Kind is a documented SPAS exit code.
type Kind int

const (
	// KindOperation is the general operational failure (exit 1).
	KindOperation Kind = 1
	// KindInvalidUsage reports invalid command-line usage (exit 2).
	KindInvalidUsage Kind = 2
	// KindNotLinked reports an unlinked public workspace (exit 3).
	KindNotLinked Kind = 3
	// KindDecisionRequired reports a required decision in non-interactive
	// mode (exit 4).
	KindDecisionRequired Kind = 4
	// KindPathConflict reports a public/private path conflict (exit 5).
	KindPathConflict Kind = 5
	// KindMergeConflict reports a private merge conflict (exit 6).
	KindMergeConflict Kind = 6
	// KindAuthNetwork reports a GitHub authentication or network failure
	// (exit 7).
	KindAuthNetwork Kind = 7
	// KindUnsafeGitState reports an unsafe public or private Git state
	// (exit 8).
	KindUnsafeGitState Kind = 8
	// KindExclusionValidation reports a local-exclusion validation failure
	// (exit 9).
	KindExclusionValidation Kind = 9
	// KindLockHeld reports that another SPAS operation holds the link lock
	// (exit 10).
	KindLockHeld Kind = 10
	// KindUnsupportedPath reports an unsupported path or file type (exit 11).
	KindUnsupportedPath Kind = 11
	// KindInterrupted reports cancellation by the user (exit 130).
	KindInterrupted Kind = 130
)

// Code returns the machine-readable error code for the kind.
func (k Kind) Code() string {
	switch k {
	case KindInvalidUsage:
		return "invalid_usage"
	case KindNotLinked:
		return "not_linked"
	case KindDecisionRequired:
		return "decision_required"
	case KindPathConflict:
		return "path_conflict"
	case KindMergeConflict:
		return "private_merge_conflict"
	case KindAuthNetwork:
		return "github_auth_or_network"
	case KindUnsafeGitState:
		return "unsafe_git_state"
	case KindExclusionValidation:
		return "exclusion_validation_failed"
	case KindLockHeld:
		return "lock_held"
	case KindUnsupportedPath:
		return "unsupported_path"
	case KindInterrupted:
		return "interrupted"
	default:
		return "operation_failed"
	}
}

// Error carries a typed kind around an underlying error.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string {
	return e.Err.Error()
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Wrap attaches a kind to err. A nil err returns nil.
func Wrap(kind Kind, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Err: err}
}

// KindOf reports the typed kind attached to err, if any.
func KindOf(err error) (Kind, bool) {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind, true
	}
	return 0, false
}
