package privategit

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getspas/spas/internal/atomicfile"
	"github.com/getspas/spas/internal/collision"
	"github.com/getspas/spas/internal/filesync"
	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/limits"
	"github.com/getspas/spas/internal/pathmodel"
	"github.com/getspas/spas/internal/spaserr"
	"golang.org/x/text/unicode/norm"
)

type Repository struct {
	Path      string
	Git       gitexec.Runner
	SafetyDir string
}

type InitResult struct {
	Branch string
	Empty  bool
	Head   string
}

// TreeLimitError reports a private tree outside the SPAS supported size.
// Git may support larger repositories; SPAS fails closed before those trees
// can reach the public workspace.
type TreeLimitError struct {
	Metric string
	Limit  int
}

func (e *TreeLimitError) Error() string {
	return fmt.Sprintf(
		"private tree exceeds the SPAS %s limit of %d",
		e.Metric,
		e.Limit,
	)
}

var (
	ErrEmptyNeedsBranch = errors.New("the private repository is empty; select an initial branch with --branch")
	ErrDefaultBranch    = errors.New("the private repository default branch could not be discovered; select it with --branch")
)

func (r Repository) StagingPath() string {
	return r.Path + ".initializing"
}

func (r Repository) PrepareClone(ctx context.Context, remoteURL, requestedBranch string) (InitResult, error) {
	parent := filepath.Dir(r.Path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return InitResult{}, fmt.Errorf("create private repository storage: %w", err)
	}
	if _, err := os.Lstat(r.Path); err == nil {
		return InitResult{}, fmt.Errorf("private clone destination already exists: %s", r.Path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return InitResult{}, fmt.Errorf("inspect private clone destination: %w", err)
	}
	staging := r.StagingPath()
	if _, err := os.Lstat(staging); err == nil {
		return InitResult{}, fmt.Errorf("private clone staging destination already exists: %s", staging)
	} else if !errors.Is(err, os.ErrNotExist) {
		return InitResult{}, fmt.Errorf("inspect private clone staging destination: %w", err)
	}
	if err := r.prepareSafetyFiles(); err != nil {
		return InitResult{}, err
	}

	cloneArgs := []string{
		"-c", "core.autocrlf=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + r.hooksDir(),
		"-c", "core.attributesFile=" + r.attributesFile(),
		"clone", "--no-checkout", "--origin", "origin", "--", remoteURL, staging,
	}
	if _, err := r.Git.RunStreaming(ctx, parent, cloneArgs...); err != nil {
		return InitResult{}, spaserr.Wrap(spaserr.KindAuthNetwork, fmt.Errorf("clone linked GitHub repository: %w", err))
	}

	stagingRepo := r.stagingRepository()
	if err := stagingRepo.verifyLayout(ctx); err != nil {
		return InitResult{}, err
	}
	if err := stagingRepo.applySafetyConfig(ctx); err != nil {
		return InitResult{}, err
	}
	if err := stagingRepo.resetInfoAttributes(); err != nil {
		return InitResult{}, err
	}

	branch, empty, err := stagingRepo.resolveInitialBranch(ctx, requestedBranch)
	if err != nil {
		return InitResult{}, err
	}
	if empty {
		if branch == "" {
			return InitResult{}, ErrEmptyNeedsBranch
		}
		if err := stagingRepo.ValidateBranch(ctx, branch); err != nil {
			return InitResult{}, err
		}
		if _, err := r.Git.Run(ctx, staging, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
			return InitResult{}, fmt.Errorf("select initial private branch: %w", err)
		}
	} else {
		if err := stagingRepo.ValidateTree(ctx, "refs/remotes/origin/"+branch); err != nil {
			return InitResult{}, err
		}
		if _, err := r.Git.Run(ctx, staging, "checkout", "-B", branch, "refs/remotes/origin/"+branch); err != nil {
			return InitResult{}, fmt.Errorf("check out private branch %q: %w", branch, err)
		}
	}
	head, err := stagingRepo.Head(ctx)
	if err != nil {
		return InitResult{}, err
	}
	result := InitResult{Branch: branch, Empty: empty, Head: head}
	if err := stagingRepo.verifyPreparedResult(ctx, result); err != nil {
		return InitResult{}, err
	}
	return result, nil
}

func (r Repository) PublishPreparedClone(ctx context.Context, expected InitResult) error {
	if err := r.stagingRepository().verifyPreparedResult(ctx, expected); err != nil {
		return fmt.Errorf("verify prepared private clone: %w", err)
	}
	if _, err := os.Lstat(r.Path); err == nil {
		return fmt.Errorf("private clone destination already exists: %s", r.Path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private clone destination: %w", err)
	}
	if err := os.Rename(r.StagingPath(), r.Path); err != nil {
		return fmt.Errorf("publish private clone: %w", err)
	}
	return nil
}

func (r Repository) VerifyPublishedClone(ctx context.Context, remoteURL string, expected InitResult) error {
	if err := r.verifyLayout(ctx); err != nil {
		return err
	}
	if err := r.verifyOrigin(ctx, remoteURL); err != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, err)
	}
	return r.verifyPreparedResult(ctx, expected)
}

func (r Repository) RemoveStagingClone() error {
	if err := os.RemoveAll(r.StagingPath()); err != nil {
		return fmt.Errorf("remove private clone staging directory: %w", err)
	}
	return nil
}

func (r Repository) stagingRepository() Repository {
	return Repository{Path: r.StagingPath(), Git: r.Git, SafetyDir: r.SafetyDir}
}

func (r Repository) Fetch(ctx context.Context, branch string) error {
	if err := r.ValidateBranch(ctx, branch); err != nil {
		return err
	}
	refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
	if _, err := r.Git.RunStreaming(ctx, r.Path, r.safeArgs("fetch", "--prune", "origin", refspec)...); err != nil {
		return spaserr.Wrap(spaserr.KindAuthNetwork, fmt.Errorf("fetch private branch %q: %w", branch, err))
	}
	return nil
}

func (r Repository) RemoteBranchExists(ctx context.Context, branch string) (bool, error) {
	if err := r.ValidateBranch(ctx, branch); err != nil {
		return false, err
	}
	_, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+branch,
	)...)
	if err == nil {
		return true, nil
	}
	if code, ok := gitexec.ExitCode(err); ok && code == 2 {
		return false, nil
	}
	return false, spaserr.Wrap(spaserr.KindAuthNetwork, fmt.Errorf("inspect private remote branch %q: %w", branch, err))
}

func (r Repository) VerifyOrigin(ctx context.Context, expected string) error {
	if err := r.verifyOrigin(ctx, expected); err != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, err)
	}
	return nil
}

func (r Repository) verifyOrigin(ctx context.Context, expected string) error {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("config", "--local", "--get-all", "remote.origin.url")...)
	if err != nil {
		return fmt.Errorf("read private clone origin: %w", err)
	}
	originURLs := strings.Split(strings.TrimSpace(string(result.Stdout)), "\n")
	if len(originURLs) != 1 || originURLs[0] != expected {
		return fmt.Errorf("private clone origin changed: found %q, expected exactly %q", originURLs, expected)
	}
	if result, err := r.Git.Run(ctx, r.Path, r.safeArgs("config", "--local", "--get-all", "remote.origin.pushurl")...); err == nil {
		if strings.TrimSpace(string(result.Stdout)) != "" {
			return fmt.Errorf("private clone has an unsupported origin push URL")
		}
	} else if code, ok := gitexec.ExitCode(err); !ok || code != 1 {
		return fmt.Errorf("inspect private clone push URL: %w", err)
	}

	return nil
}

func (r Repository) MergeRemote(ctx context.Context, branch string) (bool, error) {
	if err := r.ValidateTree(ctx, "refs/remotes/origin/"+branch); err != nil {
		return false, err
	}
	before, err := r.Head(ctx)
	if err != nil {
		return false, err
	}
	_, err = r.Git.Run(ctx, r.Path, r.safeArgs(
		"merge",
		"--no-edit",
		"--no-autostash",
		"--no-overwrite-ignore",
		"refs/remotes/origin/"+branch,
	)...)
	if err != nil {
		return false, fmt.Errorf("merge private remote changes: %w", err)
	}
	after, err := r.Head(ctx)
	if err != nil {
		return false, err
	}
	return before != after, nil
}

func (r Repository) Push(ctx context.Context, branch string) error {
	if err := r.ValidateBranch(ctx, branch); err != nil {
		return err
	}
	if _, err := r.Git.RunStreaming(ctx, r.Path, r.safeArgs("push", "--porcelain", "origin", "HEAD:refs/heads/"+branch)...); err != nil {
		if IsNonFastForward(err) {
			return fmt.Errorf("push private branch %q: %w", branch, err)
		}
		return spaserr.Wrap(spaserr.KindAuthNetwork, fmt.Errorf("push private branch %q: %w", branch, err))
	}
	return nil
}

// IsNonFastForward reports whether a push failure was a rejection because the
// remote branch advanced, as opposed to an authentication or network failure.
func IsNonFastForward(err error) bool {
	var exitErr *gitexec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	// --porcelain emits one tab-separated status line per pushed ref on
	// stdout. For the explicit branch refspec used by SPAS, a local
	// "[rejected]" status means the destination branch was not a
	// fast-forward. "[remote rejected]" remains an ordinary remote-policy
	// failure and must not trigger the fetch/merge retry loop.
	for _, line := range strings.Split(exitErr.Stdout, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(fields) == 3 && fields[0] == "!" && strings.HasPrefix(strings.TrimSpace(fields[2]), "[rejected]") {
			return true
		}
	}

	return false
}

func (r Repository) Commit(ctx context.Context, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("private commit reason must not be empty")
	}
	if _, err := r.Git.RunInput(ctx, r.Path, strings.NewReader(message+"\n"), r.safeArgs("commit", "--cleanup=verbatim", "--file=-")...); err != nil {
		return fmt.Errorf("create private commit: %w", err)
	}
	return nil
}

func (r Repository) Stage(ctx context.Context, paths []pathmodel.Path) error {
	trustFileMode, err := r.trustsFileMode(ctx)
	if err != nil {
		return err
	}
	for _, managedPath := range paths {
		if err := ValidateManagedPath(managedPath); err != nil {
			return err
		}
		if err := pathmodel.ValidateNoSymlinkComponents(r.Path, managedPath); err != nil {
			return spaserr.Wrap(spaserr.KindUnsupportedPath, err)
		}
		absolutePath := managedPath.OSPath(r.Path)
		info, err := os.Lstat(absolutePath)
		if err != nil {
			return fmt.Errorf("inspect private file %q: %w", managedPath, err)
		}
		if !info.Mode().IsRegular() {
			return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("private path %q is not a regular file", managedPath))
		}

		// Write the exact bytes directly to the object database and update the
		// index by object ID. Unlike git add, this cannot execute clean filters
		// selected by a local or newly added .gitattributes file.
		result, err := r.Git.Run(ctx, r.Path, r.safeArgs("hash-object", "-w", "--no-filters", "--", absolutePath)...)
		if err != nil {
			return fmt.Errorf("store private file %q: %w", managedPath, err)
		}
		objectID := strings.TrimSpace(string(result.Stdout))
		if objectID == "" {
			return fmt.Errorf("store private file %q: Git returned an empty object ID", managedPath)
		}
		mode := worktreeMode(info.Mode())
		if !trustFileMode {
			indexMode, found, err := r.indexMode(ctx, managedPath)
			if err != nil {
				return err
			}
			if found {
				// On filesystems where executable bits are unreliable (notably
				// Windows), preserve the existing Git mode while accepting new
				// content. Otherwise any edit to a 100755 file would silently
				// publish it as 100644.
				mode = indexMode
			}
		}
		if _, err := r.Git.Run(ctx, r.Path, r.safeArgs(
			"update-index", "--add", "--cacheinfo", mode, objectID, managedPath.String(),
		)...); err != nil {
			return fmt.Errorf("stage private file %q: %w", managedPath, err)
		}
	}
	return nil
}

func worktreeMode(mode os.FileMode) string {
	if mode.Perm()&0o111 != 0 {
		return "100755"
	}
	return "100644"
}

func (r Repository) trustsFileMode(ctx context.Context) (bool, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("config", "--type=bool", "--get", "core.fileMode")...)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			// Git's documented default is to honor the executable bit.
			return true, nil
		}
		return false, fmt.Errorf("read private clone core.fileMode: %w", err)
	}
	value, err := strconv.ParseBool(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		return false, fmt.Errorf("parse private clone core.fileMode: %w", err)
	}
	return value, nil
}

func (r Repository) indexMode(ctx context.Context, managedPath pathmodel.Path) (string, bool, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"--literal-pathspecs", "ls-files", "--stage", "-z", "--", managedPath.String(),
	)...)
	if err != nil {
		return "", false, fmt.Errorf("read private index mode for %q: %w", managedPath, err)
	}
	var fallback string
	for _, record := range bytes.Split(result.Stdout, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, rawPath, found := bytes.Cut(record, []byte{'\t'})
		if !found || string(rawPath) != managedPath.String() {
			continue
		}
		fields := strings.Fields(string(meta))
		if len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") {
			continue
		}
		switch fields[2] {
		case "0", "2":
			return fields[0], true, nil
		case "3":
			fallback = fields[0]
		}
	}
	if fallback != "" {
		return fallback, true, nil
	}
	return "", false, nil
}

func (r Repository) StageRemovals(ctx context.Context, paths []pathmodel.Path) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := r.Git.RunInput(
		ctx,
		r.Path,
		pathspecInput(paths),
		r.safeArgs(
			"--literal-pathspecs",
			"rm",
			"--quiet",
			"--force",
			"--ignore-unmatch",
			"--pathspec-from-file=-",
			"--pathspec-file-nul",
			"--",
		)...,
	); err != nil {
		return fmt.Errorf("stage private removals: %w", err)
	}
	return nil
}

func pathspecInput(paths []pathmodel.Path) io.Reader {
	var input bytes.Buffer
	for _, path := range paths {
		input.WriteString(path.String())
		input.WriteByte(0)
	}
	return &input
}

func (r Repository) ResetHard(ctx context.Context, revision string) error {
	if _, err := r.Git.Run(ctx, r.Path, r.safeArgs("reset", "--hard", "--end-of-options", revision)...); err != nil {
		return fmt.Errorf("restore private clone: %w", err)
	}
	return nil
}

func (r Repository) RollbackChanges(ctx context.Context, revision string, paths []pathmodel.Path) error {
	if revision != "" {
		return r.ResetHard(ctx, revision)
	}
	if len(paths) == 0 {
		return nil
	}
	if _, err := r.Git.RunInput(
		ctx,
		r.Path,
		pathspecInput(paths),
		r.safeArgs(
			"--literal-pathspecs",
			"rm",
			"--quiet",
			"-r",
			"--cached",
			"--force",
			"--ignore-unmatch",
			"--pathspec-from-file=-",
			"--pathspec-file-nul",
			"--",
		)...,
	); err != nil {
		return fmt.Errorf("unstage unborn private files: %w", err)
	}
	for _, path := range paths {
		if err := filesync.RemoveManaged(r.Path, path); err != nil {
			return err
		}
	}
	return nil
}

func (r Repository) AbortMerge(ctx context.Context) error {
	if _, err := r.Git.Run(ctx, r.Path, r.safeArgs("merge", "--abort")...); err != nil {
		return fmt.Errorf("abort private merge: %w", err)
	}
	return nil
}

func (r Repository) StageMergeResolution(ctx context.Context, writes, removals []pathmodel.Path) error {
	if err := r.Stage(ctx, writes); err != nil {
		return err
	}
	if err := r.StageRemovals(ctx, removals); err != nil {
		return err
	}
	unmerged, err := r.UnmergedPaths(ctx)
	if err != nil {
		return err
	}
	if len(unmerged) != 0 {
		return fmt.Errorf("private merge still has unresolved files")
	}
	return r.ValidateIndex(ctx)
}

func (r Repository) TrackedPaths(ctx context.Context) ([]pathmodel.Path, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("--literal-pathspecs", "ls-files", "--cached", "-z")...)
	if err != nil {
		return nil, fmt.Errorf("list private tracked paths: %w", err)
	}
	return parsePaths(result.Stdout)
}

func (r Repository) TreePaths(ctx context.Context, revision string) ([]pathmodel.Path, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("ls-tree", "-r", "-l", "-z", "--end-of-options", revision)...)
	if err != nil {
		return nil, fmt.Errorf("list private tree %q: %w", revision, err)
	}
	entries, err := parseTree(result.Stdout)
	if err != nil {
		return nil, err
	}
	paths := make([]pathmodel.Path, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	return paths, nil
}

func (r Repository) ChangedPaths(ctx context.Context) ([]ChangedPath, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("diff", "--cached", "--name-status", "-z")...)
	if err != nil {
		return nil, fmt.Errorf("list staged private changes: %w", err)
	}
	fields := bytes.Split(result.Stdout, []byte{0})
	var changes []ChangedPath
	for index := 0; index < len(fields); {
		if len(fields[index]) == 0 {
			index++
			continue
		}
		status := string(fields[index])
		index++
		if index >= len(fields) {
			return nil, fmt.Errorf("parse staged private changes: missing path")
		}
		path, err := pathmodel.Parse(string(fields[index]))
		if err != nil {
			return nil, err
		}
		index++
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index >= len(fields) {
				return nil, fmt.Errorf("parse staged private rename: missing destination")
			}
			path, err = pathmodel.Parse(string(fields[index]))
			if err != nil {
				return nil, err
			}
			index++
		}
		changes = append(changes, ChangedPath{Status: status[:1], Path: path})
	}
	return changes, nil
}

type ChangedPath struct {
	Status string         `json:"status"`
	Path   pathmodel.Path `json:"path"`
}

// StreamStagedDiff writes staged changes without retaining the complete diff
// in memory. Git diagnostics remain bounded by Runner.RunStreaming.
func (r Repository) StreamStagedDiff(
	ctx context.Context,
	stat bool,
	paths []pathmodel.Path,
	output io.Writer,
) error {
	args := r.safeArgs("--literal-pathspecs", "--no-pager", "diff", "--no-ext-diff", "--no-textconv", "--cached")
	if stat {
		args = append(args, "--stat")
	}
	args = append(args, "--")
	for _, path := range paths {
		args = append(args, path.String())
	}
	git := r.Git
	git.Stdout = output
	_, err := git.RunStreaming(ctx, r.Path, args...)
	if err != nil {
		return fmt.Errorf("show staged private changes: %w", err)
	}
	return nil
}

func (r Repository) IsClean(ctx context.Context) (bool, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("status", "--porcelain=v2", "-z", "--untracked-files=all")...)
	if err != nil {
		return false, err
	}
	return len(result.Stdout) == 0, nil
}

func (r Repository) UnmergedPaths(ctx context.Context) ([]pathmodel.Path, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("--literal-pathspecs", "diff", "--name-only", "--diff-filter=U", "-z")...)
	if err != nil {
		return nil, err
	}
	return parsePaths(result.Stdout)
}

func (r Repository) MergeHead(ctx context.Context) (string, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"rev-parse", "--verify", "--quiet", "--end-of-options", "MERGE_HEAD^{commit}",
	)...)
	if err != nil {
		return "", fmt.Errorf("resolve private MERGE_HEAD: %w", err)
	}
	value := strings.TrimSpace(string(result.Stdout))
	if value == "" {
		return "", fmt.Errorf("resolve private MERGE_HEAD: Git returned an empty object ID")
	}
	return value, nil
}

// IndexTree writes and returns the exact tree represented by the current
// private index. During merge continuation this binds the bytes and deletions
// approved by the user to the only merge result recovery may publish.
func (r Repository) IndexTree(ctx context.Context) (string, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("write-tree")...)
	if err != nil {
		return "", fmt.Errorf("write verified private index tree: %w", err)
	}
	value := strings.TrimSpace(string(result.Stdout))
	if value == "" {
		return "", fmt.Errorf("write verified private index tree: Git returned an empty object ID")
	}
	return value, nil
}

// IsMergeResultOf verifies that revision is the expected two-parent merge and
// exact tree.
func (r Repository) IsMergeResultOf(ctx context.Context, revision, firstParent, mergeHead, tree string) (bool, error) {
	if mergeHead == "" || tree == "" {
		return false, fmt.Errorf("expected merge parent and tree must not be empty")
	}
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"rev-list", "--parents", "-n", "1", "--end-of-options", revision,
	)...)
	if err != nil {
		return false, fmt.Errorf("inspect recovered private merge result: %w", err)
	}
	fields := strings.Fields(string(result.Stdout))
	if len(fields) != 3 || fields[1] != firstParent {
		return false, nil
	}
	if fields[2] != mergeHead {
		return false, nil
	}
	treeResult, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"rev-parse", "--verify", "--quiet", "--end-of-options", revision+"^{tree}",
	)...)
	if err != nil {
		return false, fmt.Errorf("inspect recovered private merge tree: %w", err)
	}
	return strings.TrimSpace(string(treeResult.Stdout)) == tree, nil
}

// IsMergeCommitOf verifies only the exact two parents. It is used to recover
// a durable pre-merge marker after Git completed the merge but before SPAS
// could clear that marker; no workspace result is accepted from this proof.
func (r Repository) IsMergeCommitOf(ctx context.Context, revision, firstParent, mergeHead string) (bool, error) {
	if revision == "" || firstParent == "" || mergeHead == "" {
		return false, fmt.Errorf("expected merge revision and parents must not be empty")
	}
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"rev-list", "--parents", "-n", "1", "--end-of-options", revision,
	)...)
	if err != nil {
		return false, fmt.Errorf("inspect recovered private merge result: %w", err)
	}
	fields := strings.Fields(string(result.Stdout))
	return len(fields) == 3 && fields[1] == firstParent && fields[2] == mergeHead, nil
}

func (r Repository) Head(ctx context.Context) (string, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("rev-parse", "--verify", "--quiet", "--end-of-options", "HEAD^{commit}")...)
	if err == nil {
		return strings.TrimSpace(string(result.Stdout)), nil
	}
	if code, ok := gitexec.ExitCode(err); !ok || (code != 1 && code != 128) {
		return "", fmt.Errorf("resolve private HEAD: %w", err)
	}
	var exitErr *gitexec.ExitError
	if errors.As(err, &exitErr) && strings.TrimSpace(exitErr.Stderr) != "" {
		return "", err
	}
	refResult, refErr := r.Git.Run(ctx, r.Path, r.safeArgs("symbolic-ref", "--quiet", "HEAD")...)
	if refErr == nil {
		ref := strings.TrimSpace(string(refResult.Stdout))
		_, existsErr := r.Git.Run(ctx, r.Path, r.safeArgs("show-ref", "--verify", "--quiet", ref)...)
		if existsErr == nil {
			return "", fmt.Errorf("private HEAD ref %q does not name a commit", ref)
		}
		if code, ok := gitexec.ExitCode(existsErr); ok && code == 1 {
			// A symbolic HEAD whose branch ref does not exist is an unborn
			// branch, which is valid for an empty private repository.
			return "", nil
		}
		return "", fmt.Errorf("inspect private HEAD ref %q: %w", ref, existsErr)
	}
	return "", fmt.Errorf("private HEAD does not resolve to a commit: %w", err)
}

func (r Repository) Branch(ctx context.Context) (string, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("symbolic-ref", "--quiet", "--short", "HEAD")...)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func (r Repository) MergeInProgress() (bool, error) {
	_, err := os.Lstat(filepath.Join(r.Path, ".git", "MERGE_HEAD"))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// IndexBlobOID returns the stage-zero blob object ID for path in the private
// index. It avoids reading the blob into process memory, which keeps staged
// verification bounded for large private files.
func (r Repository) IndexContains(ctx context.Context, path pathmodel.Path) (bool, error) {
	_, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"--literal-pathspecs", "ls-files", "--error-unmatch", "--", path.String(),
	)...)
	if err == nil {
		return true, nil
	}
	if code, ok := gitexec.ExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect private index path %q: %w", path, err)
}

func (r Repository) IndexBlobOID(ctx context.Context, path pathmodel.Path) (string, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"--literal-pathspecs", "ls-files", "--stage", "-z", "--", path.String(),
	)...)
	if err != nil {
		return "", err
	}
	record := bytes.TrimSuffix(result.Stdout, []byte{0})
	meta, _, found := bytes.Cut(record, []byte{'\t'})
	if !found {
		return "", fmt.Errorf("private index does not contain %q", path)
	}
	fields := strings.Fields(string(meta))
	if len(fields) != 3 || fields[2] != "0" {
		return "", fmt.Errorf("private index does not contain one resolved stage-zero entry for %q", path)
	}
	return fields[1], nil
}

// FileBlobOID computes the unfiltered Git blob ID of a regular file. The
// result can be compared directly with an index entry without loading the
// file or object into memory.
func (r Repository) FileBlobOID(ctx context.Context, absolutePath string) (string, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("hash-object", "--no-filters", "--", absolutePath)...)
	if err != nil {
		return "", fmt.Errorf("hash private working file: %w", err)
	}
	objectID := strings.TrimSpace(string(result.Stdout))
	if objectID == "" {
		return "", fmt.Errorf("hash private working file: Git returned an empty object ID")
	}
	return objectID, nil
}

func (r Repository) RemoteHead(ctx context.Context, branch string) (string, error) {
	ref := "refs/remotes/origin/" + branch
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")...)
	if err == nil {
		return strings.TrimSpace(string(result.Stdout)), nil
	}
	if code, ok := gitexec.ExitCode(err); !ok || (code != 1 && code != 128) {
		return "", fmt.Errorf("resolve private remote ref %q: %w", ref, err)
	}
	var exitErr *gitexec.ExitError
	if errors.As(err, &exitErr) && strings.TrimSpace(exitErr.Stderr) != "" {
		return "", err
	}
	_, existsErr := r.Git.Run(ctx, r.Path, r.safeArgs("show-ref", "--verify", "--quiet", ref)...)
	if existsErr == nil {
		return "", fmt.Errorf("private remote ref %q does not name a commit", ref)
	}
	if code, ok := gitexec.ExitCode(existsErr); ok && code == 1 {
		return "", nil
	}
	return "", fmt.Errorf("inspect private remote ref %q: %w", ref, existsErr)
}

func (r Repository) HasUnpushedCommits(ctx context.Context, branch string) (bool, error) {
	head, err := r.Head(ctx)
	if err != nil || head == "" {
		return false, err
	}
	remote, err := r.RemoteHead(ctx, branch)
	if err != nil {
		return false, err
	}
	if remote == "" {
		return true, nil
	}
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"rev-list", "--count", "refs/remotes/origin/"+branch+"..HEAD",
	)...)
	if err != nil {
		return false, fmt.Errorf("count unpushed private commits: %w", err)
	}
	return strings.TrimSpace(string(result.Stdout)) != "0", nil
}

// AheadBehind reports the local private branch position against the last
// fetched remote-tracking branch. known is false when either side has no
// commit yet.
func (r Repository) AheadBehind(ctx context.Context, branch string) (ahead, behind int, known bool, err error) {
	head, err := r.Head(ctx)
	if err != nil || head == "" {
		return 0, 0, false, err
	}
	remote, err := r.RemoteHead(ctx, branch)
	if err != nil || remote == "" {
		return 0, 0, false, err
	}
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs(
		"rev-list", "--left-right", "--count", "HEAD...refs/remotes/origin/"+branch,
	)...)
	if err != nil {
		return 0, 0, false, fmt.Errorf("compare private branch with fetched remote: %w", err)
	}
	fields := strings.Fields(string(result.Stdout))
	if len(fields) != 2 {
		return 0, 0, false, fmt.Errorf("parse private ahead/behind counts")
	}
	ahead, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, false, fmt.Errorf("parse private ahead count: %w", err)
	}
	behind, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false, fmt.Errorf("parse private behind count: %w", err)
	}
	return ahead, behind, true, nil
}

func (r Repository) ValidateBranch(ctx context.Context, branch string) error {
	return ValidateBranchName(ctx, r.Git, r.Path, branch)
}

// ValidateBranchName requires a literal short branch name. Git's
// check-ref-format --branch command expands previous-checkout expressions such
// as @{-1}; accepting only unchanged output prevents persisted or user-supplied
// branch data from being interpreted relative to another repository's reflog.
func ValidateBranchName(ctx context.Context, git gitexec.Runner, workingDirectory, branch string) error {
	if branch == "" {
		return fmt.Errorf("private branch is required")
	}
	result, err := git.Run(ctx, workingDirectory, "check-ref-format", "--branch", branch)
	if err != nil || strings.TrimSpace(string(result.Stdout)) != branch {
		return fmt.Errorf("invalid private branch %q", branch)
	}
	return nil
}

// ValidateManagedPath rejects repository-control files whose materialization
// would alter public Git behavior or whose attributes could execute filters
// while private content is staged.
func ValidateManagedPath(managedPath pathmodel.Path) error {
	base := path.Base(managedPath.String())
	firstComponent, _, _ := strings.Cut(managedPath.String(), "/")
	switch {
	case strings.EqualFold(firstComponent, filesync.ManagedTempDirectory):
		return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("private repository must not track reserved SPAS workspace path %q", managedPath))
	case strings.EqualFold(base, ".gitattributes"), strings.EqualFold(base, ".gitmodules"):
		return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("private repository must not track %q in the current implementation", managedPath))
	case strings.EqualFold(base, ".gitignore"):
		return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("private repository must not track %q: a materialized .gitignore would change public Git ignore behavior", managedPath))
	default:
		return nil
	}
}

func (r Repository) ValidateTree(ctx context.Context, revision string) error {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("ls-tree", "-r", "-z", "-l", "--end-of-options", revision)...)
	if err != nil {
		return fmt.Errorf("inspect private tree %q: %w", revision, err)
	}
	entries, err := parseTree(result.Stdout)
	if err != nil {
		return err
	}
	paths := make([]pathmodel.Path, 0, len(entries))
	smallBlobs := make([]treeEntry, 0, len(entries))
	for _, entry := range entries {
		if (entry.Mode != "100644" && entry.Mode != "100755") || entry.Type != "blob" {
			return spaserr.Wrap(spaserr.KindUnsupportedPath, fmt.Errorf("private path %q uses unsupported Git mode %s", entry.Path, entry.Mode))
		}
		if err := ValidateManagedPath(entry.Path); err != nil {
			return err
		}
		if entry.Size < 0 {
			return fmt.Errorf("inspect private path %q: private Git tree did not provide a regular-file size", entry.Path)
		}
		if entry.Size <= limits.MaxGitLFSPointerBytes {
			smallBlobs = append(smallBlobs, entry)
		}
		paths = append(paths, entry.Path)
	}
	lfsPath, err := r.firstLFSPointer(ctx, smallBlobs)
	if err != nil {
		return err
	}
	if lfsPath != "" {
		return fmt.Errorf("private path %q is a Git LFS pointer; Git LFS is not supported in the current implementation", lfsPath)
	}
	if err := portabilityDuplicates(paths); err != nil {
		return err
	}
	return nil
}

// ValidateIndex validates the exact tree currently represented by the private
// index. This catches unsupported local additions and merge resolutions before
// they can become a private commit. git write-tree also proves the index is
// fully merged and that all referenced objects exist.
func (r Repository) ValidateIndex(ctx context.Context) error {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("write-tree")...)
	if err != nil {
		return fmt.Errorf("write private index tree: %w", err)
	}
	tree := strings.TrimSpace(string(result.Stdout))
	if tree == "" {
		return fmt.Errorf("write private index tree: Git returned an empty tree object ID")
	}
	if err := r.ValidateTree(ctx, tree); err != nil {
		return fmt.Errorf("validate private index: %w", err)
	}
	return nil
}

func (r Repository) firstLFSPointer(ctx context.Context, entries []treeEntry) (pathmodel.Path, error) {
	if len(entries) == 0 {
		return "", nil
	}
	var input strings.Builder
	for _, entry := range entries {
		input.WriteString(entry.ObjectID)
		input.WriteByte('\n')
	}
	result, err := r.Git.RunInput(
		ctx,
		r.Path,
		strings.NewReader(input.String()),
		r.safeArgs("cat-file", "--batch")...,
	)
	if err != nil {
		return "", fmt.Errorf("inspect small private blobs: %w", err)
	}
	reader := bufio.NewReader(bytes.NewReader(result.Stdout))
	for _, entry := range entries {
		header, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("inspect private path %q: parse Git batch header: %w", entry.Path, err)
		}
		fields := strings.Fields(strings.TrimSuffix(header, "\n"))
		if len(fields) != 3 || fields[0] != entry.ObjectID || fields[1] != "blob" {
			return "", fmt.Errorf("inspect private path %q: unexpected Git batch header %q", entry.Path, strings.TrimSpace(header))
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size != entry.Size || size < 0 || size > limits.MaxGitLFSPointerBytes {
			return "", fmt.Errorf("inspect private path %q: unexpected Git batch blob size %q", entry.Path, fields[2])
		}
		content := make([]byte, int(size))
		if _, err := io.ReadFull(reader, content); err != nil {
			return "", fmt.Errorf("inspect private path %q: read Git batch blob: %w", entry.Path, err)
		}
		delimiter, err := reader.ReadByte()
		if err != nil || delimiter != '\n' {
			return "", fmt.Errorf("inspect private path %q: malformed Git batch blob delimiter", entry.Path)
		}
		if isLFSPointerContent(content) {
			return entry.Path, nil
		}
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("inspect small private blobs: Git batch returned unexpected trailing output")
	}
	return "", nil
}

func isLFSPointerContent(content []byte) bool {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "version https://git-lfs.github.com/spec/v1" {
		return false
	}
	var hasOID, hasSize bool
	for _, line := range lines[1:] {
		if digest, found := strings.CutPrefix(line, "oid sha256:"); found {
			hasOID = len(digest) == 64 && allHex(digest)
		}
		if rawSize, found := strings.CutPrefix(line, "size "); found {
			_, err := strconv.ParseUint(rawSize, 10, 64)
			hasSize = err == nil
		}
	}
	return hasOID && hasSize
}

func allHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') ||
			(character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func (r Repository) resolveInitialBranch(ctx context.Context, requested string) (string, bool, error) {
	result, err := r.Git.Run(ctx, r.Path, r.safeArgs("for-each-ref", "--format=%(refname)", "refs/remotes/origin")...)
	if err != nil {
		return "", false, err
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		if line == "" || line == "refs/remotes/origin/HEAD" {
			continue
		}
		if strings.HasPrefix(line, "refs/remotes/origin/") {
			branches = append(branches, strings.TrimPrefix(line, "refs/remotes/origin/"))
		}
	}
	sort.Strings(branches)
	if len(branches) == 0 {
		return requested, true, nil
	}
	if requested != "" {
		for _, branch := range branches {
			if branch == requested {
				return requested, false, nil
			}
		}
		return "", false, fmt.Errorf("private branch %q does not exist", requested)
	}

	defaultResult, defaultErr := r.Git.Run(ctx, r.Path, r.safeArgs("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")...)
	if defaultErr != nil {
		return "", false, ErrDefaultBranch
	}
	value := strings.TrimSpace(string(defaultResult.Stdout))
	if !strings.HasPrefix(value, "origin/") {
		return "", false, ErrDefaultBranch
	}
	return strings.TrimPrefix(value, "origin/"), false, nil
}

func (r Repository) verifyPreparedResult(ctx context.Context, expected InitResult) error {
	if err := r.verifySafetyConfig(ctx); err != nil {
		return err
	}
	branch, err := r.Branch(ctx)
	if err != nil {
		return fmt.Errorf("resolve prepared private branch: %w", err)
	}
	if branch != expected.Branch {
		return fmt.Errorf("private clone branch changed: found %q, expected %q", branch, expected.Branch)
	}
	head, err := r.Head(ctx)
	if err != nil {
		return fmt.Errorf("resolve prepared private HEAD: %w", err)
	}
	if head != expected.Head {
		return fmt.Errorf("private clone HEAD changed: found %q, expected %q", head, expected.Head)
	}
	if expected.Empty {
		if head != "" {
			return fmt.Errorf("empty private clone has a committed HEAD %q", head)
		}
		return nil
	}
	if head == "" {
		return fmt.Errorf("non-empty private clone has an unborn HEAD")
	}
	if err := r.ValidateTree(ctx, head); err != nil {
		return fmt.Errorf("validate prepared private tree: %w", err)
	}
	return nil
}

func (r Repository) verifySafetyConfig(ctx context.Context) error {
	settings := [][2]string{
		{"core.autocrlf", "false"},
		{"core.fsmonitor", "false"},
		{"core.hooksPath", r.hooksDir()},
		{"core.attributesFile", r.attributesFile()},
	}
	for _, setting := range settings {
		result, err := r.Git.Run(ctx, r.Path, "config", "--local", "--get", setting[0])
		if err != nil {
			return fmt.Errorf("read private clone %s: %w", setting[0], err)
		}
		if strings.TrimSpace(string(result.Stdout)) != setting[1] {
			return fmt.Errorf("private clone safety setting %s changed: found %q, expected %q", setting[0], strings.TrimSpace(string(result.Stdout)), setting[1])
		}
	}
	if info, err := os.Lstat(r.hooksDir()); err != nil {
		return fmt.Errorf("inspect private clone hooks directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private clone hooks path is not a regular directory")
	}
	if info, err := os.Lstat(r.attributesFile()); err != nil {
		return fmt.Errorf("inspect private clone safety attributes: %w", err)
	} else if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private clone safety attributes path is not a regular file")
	}
	return nil
}

func (r Repository) applySafetyConfig(ctx context.Context) error {
	settings := [][2]string{
		{"core.autocrlf", "false"},
		{"core.fsmonitor", "false"},
		{"core.hooksPath", r.hooksDir()},
		{"core.attributesFile", r.attributesFile()},
	}
	for _, setting := range settings {
		if _, err := r.Git.Run(ctx, r.Path, "config", "--local", setting[0], setting[1]); err != nil {
			return fmt.Errorf("configure private clone %s: %w", setting[0], err)
		}
	}
	return nil
}

// EnsureSafety recreates the SPAS-owned, link-scoped safety files. It is used
// for existing clones as well as new clones so upgrades cannot leave a clone
// pointing at a stale shared safety directory.
func (r Repository) EnsureSafety(ctx context.Context) error {
	if err := r.ensureSafety(ctx); err != nil {
		return spaserr.Wrap(spaserr.KindUnsafeGitState, err)
	}
	return nil
}

func (r Repository) ensureSafety(ctx context.Context) error {
	if err := r.prepareSafetyFiles(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(r.Path, ".git")); err == nil {
		if err := r.verifyLayout(ctx); err != nil {
			return err
		}
		if err := r.resetInfoAttributes(); err != nil {
			return err
		}
		return r.applySafetyConfig(ctx)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private clone metadata: %w", err)
	}
	return nil
}

// verifyLayout prevents a modified local private repository from redirecting
// destructive Git operations to another working tree or metadata directory.
// SPAS supports only an ordinary, non-bare clone rooted exactly at r.Path.
func (r Repository) verifyLayout(ctx context.Context) error {
	rootInfo, err := os.Lstat(r.Path)
	if err != nil {
		return fmt.Errorf("inspect private clone root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private clone root is not a regular directory")
	}

	expectedGitDir := filepath.Join(r.Path, ".git")
	gitInfo, err := os.Lstat(expectedGitDir)
	if err != nil {
		return fmt.Errorf("inspect private clone Git directory: %w", err)
	}
	if !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private clone Git metadata path is not a regular directory")
	}

	bareResult, err := r.Git.Run(ctx, r.Path, "rev-parse", "--is-bare-repository")
	if err != nil {
		return fmt.Errorf("inspect private clone repository type: %w", err)
	}
	if strings.TrimSpace(string(bareResult.Stdout)) != "false" {
		return fmt.Errorf("private clone must be a non-bare repository")
	}

	topResult, err := r.Git.Run(ctx, r.Path, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("resolve private clone working tree: %w", err)
	}
	if same, err := sameFilesystemObject(r.Path, strings.TrimSpace(string(topResult.Stdout))); err != nil {
		return fmt.Errorf("verify private clone working tree: %w", err)
	} else if !same {
		return fmt.Errorf("private clone working tree was redirected outside SPAS storage")
	}

	gitDirResult, err := r.Git.Run(ctx, r.Path, "rev-parse", "--path-format=absolute", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("resolve private clone Git directory: %w", err)
	}
	if same, err := sameFilesystemObject(expectedGitDir, strings.TrimSpace(string(gitDirResult.Stdout))); err != nil {
		return fmt.Errorf("verify private clone Git directory: %w", err)
	} else if !same {
		return fmt.Errorf("private clone Git metadata was redirected outside SPAS storage")
	}
	if err := r.rejectPartialCloneConfiguration(ctx); err != nil {
		return err
	}
	return nil
}

func (r Repository) rejectPartialCloneConfiguration(ctx context.Context) error {
	result, err := r.Git.Run(ctx, r.Path, "config", "--get-regexp", "^(extensions\\.partialclone|remote\\..*\\.partialclonefilter)$")
	if err == nil {
		if configuration := strings.TrimSpace(string(result.Stdout)); configuration != "" {
			return fmt.Errorf("private clone uses unsupported partial-clone configuration: %s", configuration)
		}
	} else if code, ok := gitexec.ExitCode(err); !ok || code != 1 {
		return fmt.Errorf("inspect private clone partial-clone configuration: %w", err)
	} else {
		var exitErr *gitexec.ExitError
		if errors.As(err, &exitErr) && strings.TrimSpace(exitErr.Stderr) != "" {
			return fmt.Errorf("inspect private clone partial-clone configuration: %w", err)
		}
	}

	result, err = r.Git.Run(ctx, r.Path, "config", "--type=bool", "--get-regexp", "^remote\\..*\\.promisor$")
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			var exitErr *gitexec.ExitError
			if !errors.As(err, &exitErr) || strings.TrimSpace(exitErr.Stderr) == "" {
				return nil
			}
		}
		return fmt.Errorf("inspect private clone promisor configuration: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("parse private clone promisor configuration %q", line)
		}
		switch fields[1] {
		case "false":
			continue
		case "true":
			return fmt.Errorf("private clone uses unsupported promisor configuration: %s", line)
		default:
			return fmt.Errorf("parse private clone promisor configuration %q", line)
		}
	}
	return nil
}

func sameFilesystemObject(first, second string) (bool, error) {
	firstInfo, err := os.Stat(first)
	if err != nil {
		return false, err
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		return false, err
	}
	return os.SameFile(firstInfo, secondInfo), nil
}

// resetInfoAttributes clears the private clone's highest-precedence local
// attributes file. Without this, a stale .git/info/attributes entry could
// select a smudge or clean filter even though system, global, and tracked
// attributes are neutralized or rejected.
func (r Repository) resetInfoAttributes() error {
	gitDir := filepath.Join(r.Path, ".git")
	if info, err := os.Lstat(gitDir); err != nil {
		return fmt.Errorf("inspect private clone Git directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private clone Git metadata path is not a regular directory")
	}
	infoDir := filepath.Join(gitDir, "info")
	if info, err := os.Lstat(infoDir); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(infoDir, 0o700); err != nil {
			return fmt.Errorf("create private clone info directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect private clone info directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private clone info path is not a regular directory")
	}
	attributes := filepath.Join(infoDir, "attributes")
	if info, err := os.Lstat(attributes); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private clone info attributes path is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private clone info attributes: %w", err)
	}
	if err := atomicfile.Write(attributes, nil, 0o600); err != nil {
		return fmt.Errorf("reset private clone info attributes: %w", err)
	}
	return nil
}

func (r Repository) prepareSafetyFiles() error {
	if err := os.MkdirAll(r.SafetyDir, 0o700); err != nil {
		return fmt.Errorf("create private clone safety directory: %w", err)
	}
	if info, err := os.Lstat(r.SafetyDir); err != nil {
		return fmt.Errorf("inspect private clone safety directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private clone safety path is not a regular directory")
	}
	// The safety directory is SPAS-owned and link-scoped. Recreate the hooks
	// directory so stale hook programs can never run during a later Git
	// operation. Validate the parent first so cleanup cannot be redirected by a
	// symlinked safety root.
	if err := os.RemoveAll(r.hooksDir()); err != nil {
		return fmt.Errorf("reset private clone hooks directory: %w", err)
	}
	if err := os.Mkdir(r.hooksDir(), 0o700); err != nil {
		return fmt.Errorf("create private clone hooks directory: %w", err)
	}
	if info, err := os.Lstat(r.attributesFile()); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("private clone safety attributes path is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private clone safety attributes path: %w", err)
	}
	if err := atomicfile.Write(r.attributesFile(), nil, 0o600); err != nil {
		return fmt.Errorf("reset private clone safety attributes: %w", err)
	}
	return nil
}

func (r Repository) safeArgs(args ...string) []string {
	prefix := []string{
		"-c", "core.autocrlf=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + r.hooksDir(),
		"-c", "core.attributesFile=" + r.attributesFile(),
	}
	return append(prefix, args...)
}

func (r Repository) hooksDir() string {
	return filepath.Join(r.SafetyDir, "hooks")
}

func (r Repository) attributesFile() string {
	return filepath.Join(r.SafetyDir, "attributes")
}

type treeEntry struct {
	Mode     string
	Type     string
	ObjectID string
	Size     int64
	Path     pathmodel.Path
}

func parseTree(output []byte) ([]treeEntry, error) {
	if len(output) > limits.MaxPrivateTreeMetadataBytes {
		return nil, &TreeLimitError{
			Metric: "tree-metadata byte",
			Limit:  limits.MaxPrivateTreeMetadataBytes,
		}
	}
	entryCount := bytes.Count(output, []byte{0})
	if entryCount > limits.MaxPrivateTreeEntries {
		return nil, &TreeLimitError{
			Metric: "tree-entry",
			Limit:  limits.MaxPrivateTreeEntries,
		}
	}
	if len(output) > 0 && output[len(output)-1] != 0 {
		return nil, fmt.Errorf("parse private Git tree: unterminated entry")
	}

	entries := make([]treeEntry, 0, entryCount)
	for len(output) > 0 {
		record, rest, found := bytes.Cut(output, []byte{0})
		if !found {
			return nil, fmt.Errorf("parse private Git tree: unterminated entry")
		}
		output = rest
		if len(record) == 0 {
			return nil, fmt.Errorf("parse private Git tree: empty entry")
		}
		meta, rawPath, found := bytes.Cut(record, []byte{'\t'})
		if !found {
			return nil, fmt.Errorf("parse private Git tree entry")
		}
		fields := strings.Fields(string(meta))
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse private Git tree metadata")
		}
		size := int64(-1)
		if fields[3] != "-" {
			parsed, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil || parsed < 0 {
				return nil, fmt.Errorf("parse private Git tree blob size")
			}
			size = parsed
		}
		path, err := parsePrivatePath(string(rawPath))
		if err != nil {
			return nil, fmt.Errorf("private Git tree contains unsupported path: %w", err)
		}
		entries = append(entries, treeEntry{
			Mode:     fields[0],
			Type:     fields[1],
			ObjectID: fields[2],
			Size:     size,
			Path:     path,
		})
	}
	return entries, nil
}

// parsePrivatePath validates one path read back from the private Git index or
// tree. The raw spelling must already be NFC: pathmodel.Parse silently
// normalizes, and a silently renormalized path would no longer match the Git
// entry it came from when used as a pathspec.
func parsePrivatePath(raw string) (pathmodel.Path, error) {
	if raw != norm.NFC.String(raw) {
		return "", fmt.Errorf("path %q is not in Unicode NFC form and is not portable across platforms", raw)
	}
	return pathmodel.Parse(raw)
}

func parsePaths(output []byte) ([]pathmodel.Path, error) {
	fields := bytes.Split(output, []byte{0})
	paths := make([]pathmodel.Path, 0, len(fields))
	for _, field := range fields {
		if len(field) == 0 {
			continue
		}
		path, err := parsePrivatePath(string(field))
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func portabilityDuplicates(paths []pathmodel.Path) error {
	return collision.PrivatePortabilityConflicts(paths, true)
}
