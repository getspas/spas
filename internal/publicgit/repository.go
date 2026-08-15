package publicgit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/pathmodel"
)

type Repository struct {
	Root      string
	GitDir    string
	CommonDir string
	Git       gitexec.Runner
}

func Discover(ctx context.Context, git gitexec.Runner, hint string) (Repository, error) {
	if err := RequireSupportedGit(ctx, git); err != nil {
		return Repository{}, err
	}
	if hint == "" {
		hint = "."
	}
	absoluteHint, err := filepath.Abs(hint)
	if err != nil {
		return Repository{}, fmt.Errorf("resolve workspace path: %w", err)
	}

	rootResult, err := git.Run(ctx, absoluteHint, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("%s is not inside a Git working tree: %w", absoluteHint, err)
	}
	root, err := filepath.Abs(strings.TrimSpace(string(rootResult.Stdout)))
	if err != nil {
		return Repository{}, fmt.Errorf("resolve public workspace root: %w", err)
	}

	commonResult, err := git.Run(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("locate public Git metadata: %w", err)
	}
	common := strings.TrimSpace(string(commonResult.Stdout))
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return Repository{}, fmt.Errorf("resolve public Git metadata: %w", err)
	}

	gitDirResult, err := git.Run(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("locate public worktree Git directory: %w", err)
	}
	gitDir, err := filepath.Abs(strings.TrimSpace(string(gitDirResult.Stdout)))
	if err != nil {
		return Repository{}, fmt.Errorf("resolve public worktree Git directory: %w", err)
	}

	return Repository{Root: root, GitDir: gitDir, CommonDir: common, Git: git}, nil
}

func RequireSupportedGit(ctx context.Context, git gitexec.Runner) error {
	result, err := git.Run(ctx, ".", "--version")
	if err != nil {
		return fmt.Errorf("Git 2.43.1 or newer is required: %w", err)
	}
	return validateGitVersion(string(result.Stdout))
}

func validateGitVersion(output string) error {
	fields := strings.Fields(output)
	if len(fields) < 3 {
		return fmt.Errorf("could not parse Git version %q", strings.TrimSpace(output))
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 3 {
		return fmt.Errorf("could not parse Git version %q", fields[2])
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	if majorErr != nil || minorErr != nil || patchErr != nil {
		return fmt.Errorf("could not parse Git version %q", fields[2])
	}
	if major < 2 || (major == 2 && (minor < 43 || (minor == 43 && patch < 1))) {
		return fmt.Errorf("Git 2.43.1 or newer is required; found %s", fields[2])
	}
	return nil
}

func (r Repository) Head(ctx context.Context) (string, error) {
	result, err := r.Git.Run(ctx, r.Root, "rev-parse", "--verify", "--quiet", "--end-of-options", "HEAD^{commit}")
	if err == nil {
		return strings.TrimSpace(string(result.Stdout)), nil
	}
	if code, ok := gitexec.ExitCode(err); !ok || (code != 1 && code != 128) {
		return "", fmt.Errorf("resolve public HEAD: %w", err)
	}
	var exitErr *gitexec.ExitError
	if errors.As(err, &exitErr) && strings.TrimSpace(exitErr.Stderr) != "" {
		return "", err
	}
	refResult, refErr := r.Git.Run(ctx, r.Root, "symbolic-ref", "--quiet", "HEAD")
	if refErr == nil {
		ref := strings.TrimSpace(string(refResult.Stdout))
		_, existsErr := r.Git.Run(ctx, r.Root, "show-ref", "--verify", "--quiet", ref)
		if existsErr == nil {
			return "", fmt.Errorf("public HEAD ref %q does not name a commit", ref)
		}
		if code, ok := gitexec.ExitCode(existsErr); ok && code == 1 {
			return "", nil
		}
		return "", fmt.Errorf("inspect public HEAD ref %q: %w", ref, existsErr)
	}
	return "", fmt.Errorf("public HEAD does not resolve to a commit: %w", err)
}

func (r Repository) Branch(ctx context.Context) (string, error) {
	result, err := r.Git.Run(ctx, r.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func (r Repository) TrackedPaths(ctx context.Context) ([]pathmodel.Path, error) {
	result, err := r.Git.Run(ctx, r.Root, "--literal-pathspecs", "ls-files", "--cached", "-z")
	if err != nil {
		return nil, fmt.Errorf("list public tracked paths: %w", err)
	}
	return parsePaths(result.Stdout)
}

func (r Repository) UnmergedPaths(ctx context.Context) ([]pathmodel.Path, error) {
	result, err := r.Git.Run(ctx, r.Root, "--literal-pathspecs", "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, fmt.Errorf("list public merge conflicts: %w", err)
	}
	return parsePaths(result.Stdout)
}

func (r Repository) InfoExcludePath(ctx context.Context) (string, error) {
	result, err := r.Git.Run(ctx, r.Root, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return "", fmt.Errorf("locate public repository local exclude file: %w", err)
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func (r Repository) IsEffectivelyExcluded(ctx context.Context, path pathmodel.Path) (bool, error) {
	_, err := r.Git.Run(ctx, r.Root, "check-ignore", "--no-index", "--quiet", "--", path.String())
	if err == nil {
		return true, nil
	}
	if code, ok := gitexec.ExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check public exclusion for %q: %w", path, err)
}

func (r Repository) EffectiveIgnoreCase(ctx context.Context) (bool, bool, error) {
	result, err := r.Git.Run(ctx, r.Root, "config", "--type=bool", "--get", "core.ignoreCase")
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return false, false, nil
		}
		return false, false, fmt.Errorf("read core.ignoreCase: %w", err)
	}
	value, err := strconv.ParseBool(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		return false, true, fmt.Errorf("parse core.ignoreCase: %w", err)
	}
	return value, true, nil
}

func (r Repository) FilesystemIgnoresCase() (bool, error) {
	// A separate Git directory can live on another filesystem, so probe the
	// public workspace itself.
	file, err := os.CreateTemp(r.Root, ".spas-case-Probe-*")
	if err != nil {
		return false, fmt.Errorf("probe public workspace case behavior: %w", err)
	}
	original := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(original)
		return false, err
	}
	defer os.Remove(original)

	base := filepath.Base(original)
	alternateBase := swapASCIIcase(base)
	if alternateBase == base {
		return false, errors.New("internal case probe did not produce a distinct name")
	}
	alternate := filepath.Join(filepath.Dir(original), alternateBase)
	originalInfo, err := os.Stat(original)
	if err != nil {
		return false, fmt.Errorf("probe public workspace case behavior: %w", err)
	}
	alternateInfo, err := os.Stat(alternate)
	switch {
	case err == nil:
		// A differently-cased path is evidence of a case-insensitive filesystem
		// only when it resolves to the file we just created. This avoids a false
		// positive in the extremely unlikely event that a distinct path with the
		// generated alternate spelling already exists.
		return os.SameFile(originalInfo, alternateInfo), nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("probe public workspace case behavior: %w", err)
	}
}

func (r Repository) WorktreeCount(ctx context.Context) (int, error) {
	result, err := r.Git.Run(ctx, r.Root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return 0, fmt.Errorf("list public Git worktrees: %w", err)
	}
	return countWorktreeRecords(result.Stdout), nil
}

func countWorktreeRecords(output []byte) int {
	count := 0
	for _, line := range strings.Split(string(output), "\x00") {
		if strings.HasPrefix(line, "worktree ") {
			count++
		}
	}
	return count
}

func (r Repository) PathStatus(ctx context.Context, path pathmodel.Path) ([]byte, error) {
	result, err := r.Git.Run(ctx, r.Root, "--literal-pathspecs", "status", "--porcelain=v2", "-z", "--untracked-files=all", "--", path.String())
	if err != nil {
		return nil, err
	}
	return result.Stdout, nil
}

func (r Repository) RemoveTracked(ctx context.Context, paths []pathmodel.Path) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := r.Git.RunInput(
		ctx,
		r.Root,
		pathspecInput(paths),
		"--literal-pathspecs",
		"rm",
		"--quiet",
		"-r",
		"--cached",
		"--force",
		"--pathspec-from-file=-",
		"--pathspec-file-nul",
		"--",
	); err != nil {
		return fmt.Errorf("stage public ownership removal: %w", err)
	}
	return nil
}

func pathspecInput(paths []pathmodel.Path) *strings.Reader {
	var input strings.Builder
	for _, path := range paths {
		input.WriteString(path.String())
		input.WriteByte(0)
	}
	return strings.NewReader(input.String())
}

func parsePaths(output []byte) ([]pathmodel.Path, error) {
	fields := strings.Split(string(output), "\x00")
	paths := make([]pathmodel.Path, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		// Public tracked paths are observations, not managed paths: a public
		// repository may legally track names SPAS itself would refuse (for
		// example `src/aux.c` on Linux). They are retained for collision
		// comparison and must never make the repository unusable.
		path, err := pathmodel.ParseObserved(field)
		if err != nil {
			return nil, fmt.Errorf("public Git returned unusable path %q: %w", field, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func swapASCIIcase(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			result.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z':
			result.WriteRune(r + ('a' - 'A'))
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}
