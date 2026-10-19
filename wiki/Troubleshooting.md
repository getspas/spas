# Troubleshooting Guide

Start with `spas status` and `spas doctor` to inspect local state. Use the
reported error code to find the relevant fix below.

[Common errors](#2-common-errors--solutions) · [Interrupted sync](#3-resuming-interrupted-synchronizations) · [Support](#4-getting-support)

---

## 1. Quick Diagnostic Triage

Start with the link status and health checks:

```bash
# Check link status, managed assets, and recovery state
spas status

# Run automated offline health diagnostics
spas doctor
```

### Generating Sanitized JSON for Bug Reports

Generate structured diagnostics, then review and redact them before sharing:

```bash
spas doctor --json
```

> [!WARNING]
> Review and redact repository names, paths, and Git error messages before
> sharing diagnostic JSON. Full `spas diff` output contains
> asset contents; `--stat` and `--name-only` omit contents but still reveal filenames.

---

## 2. Common Errors & Solutions

### `not_linked` (Exit Code 3)

- **Cause:** The current directory is not linked to a SPAS asset repository.
- **Fix:** Verify you are in the correct repository root, or run:

  ```bash
  spas link your-org/project-assets --transport ssh --branch main
  ```

---

### `decision_required` (Exit Code 4)

- **Cause:** Running in non-interactive mode without specifying a required policy.
- **Fix:** Supply the missing flag indicated in the error message (e.g. `--message "Commit message"`, `--conflict abort`, or `--existing-exclude preserve`).

---

### `auth_or_network` (Exit Code 7)

- **Cause:** Git was unable to authenticate with the remote repository or encountered a network timeout.
- **Fix:**
  - Verify your SSH keys (`ssh -T git@github.com`) or HTTPS credential helper.
  - Confirm repository permissions for your GitHub user account.
  - Test Git connectivity to the remote URL.
  - For scripts, confirm the key/agent and known-host entry or HTTPS helper work
    without prompts. See [unattended authentication](Command-reference#unattended-authentication).
    A SPAS `--timeout` expiry reports `operation_failed` (exit 1).

---

### `path_conflict` (Exit Code 5)

- **Cause:** A managed asset shares a path with a file already tracked by your main project Git repository.
- **Fix:** Choose a policy for `spas sync`:
  - Keep the project repository's version with `--conflict=skip`.
  - Transfer ownership to SPAS with `--conflict=override`. SPAS stages deletion
    in the project index; review `git diff --cached` before making your own project
    commit. Discarding uncommitted project changes needs separate approval through
    `--discard-public-changes`; see the [sync options](Command-reference#spas-sync).

---

### `unsupported_path` (Exit Code 11)

- **Cause:** An asset is a symlink, directory junction, submodule, a reserved Git/SPAS path, or cannot be represented in SPAS state/exclusion rules (invalid UTF-8, Unicode `Cf` characters, or control codes). Windows additionally rejects its reserved filename syntax; native Git/filesystem operations may reject other names or lengths on the destination.
- **Fix:** Use standard regular files and inspect the failing path and operation in the error. Filename support depends on the destination’s Git and filesystem. Distinct case/Unicode names are supported when both destinations distinguish them; resolve any named native alias collision before retrying. Path lengths depend on actual Git/filesystem support in both destination roots. Correct the named native failure and retry synchronization; SPAS retains any pending recovery state.

---

### `unsafe_git_state` (Exit Code 8)

- **Cause:** SPAS detected multiple worktrees, an in-progress rebase/merge in the main repository, or an unexpected commit in the managed checkout.
- **Fix:** Run `spas doctor` to pinpoint the unsafe condition. Complete or abort any pending Git operations in your project repository before syncing.

---

### `exclusion_validation_failed` (Exit Code 9)

- **Cause:** A managed path is not effectively excluded from the project's primary Git repository. This can happen when a project `.gitignore` contains a negation pattern (`!path` or `!*.json`) that re-includes a path. Project `.gitignore` rules take precedence over `.git/info/exclude`, which takes precedence over the global `core.excludesFile`.
- **Why SPAS Stops:** Ineffective exclusions can expose assets to `git add .` and a later project commit. SPAS checks effective exclusion with `git check-ignore --no-index` before applying files and stops when a managed path is not ignored.
- **How to Diagnose & Fix:**
  1. Identify which rule is re-including the path:

     ```bash
     git check-ignore -v --no-index path/to/file
     ```

  2. Open the reported `.gitignore` file and remove or adjust the negation pattern (e.g. change `!config/dev.json` so it does not match SPAS-managed private files).
  3. Run `spas doctor` to verify that all managed paths are now effectively excluded.

### Unexpected exclusion repair warnings

`exclusion_drift_repaired` indicates that an exclusion-writing operation rebuilt missing or edited owned rules from link state. Check for an editor or tool modifying the SPAS block. Edit outside the exact ownership markers and avoid concurrent writes. The warning can precede a later error and rollback; it does not certify overall success. Ordinary enrollment/removal changes do not warn.

Malformed or duplicate matching markers require manual repair. Open the exclude file named in the error. Restore exactly one correctly ordered `# BEGIN SPAS link=<block-id>` / `# END SPAS link=<block-id>` pair, or remove only the verified owned block and retry. Preserve outside rules. `spas doctor` diagnoses without repairing.

If both markers no longer match, SPAS preserves the old text as external content. Those rules may continue to ignore files after unlink; inspect and remove them manually if appropriate. A reconstructed block still cannot defeat higher-precedence `.gitignore` negations: resolve the effective-exclusion error above.

### `lock_held` (Exit Code 10)

- **Cause:** Another SPAS process is currently running and holding the advisory lock on this link or exclude file.
- **Fix:** Wait for the active process to complete. If a process was forcefully terminated (SIGKILL/power outage), the lock is automatically released by the operating system—the presence of the lock file alone does not prevent new runs.

---

### `private_merge_conflict` (Exit Code 6)

- **Cause:** Changes made to an asset on another machine conflict with local edits in your workspace.
- **How to Resolve:**
  1. Open the conflicted files directly in your workspace editor and resolve the conflict markers.
  2. Continue the sync with a commit message:

     ```bash
     spas sync --continue --message "Resolve asset merge conflict"
     ```

  3. Alternatively, abort the merge and restore your pre-merge workspace state:

     ```bash
     spas sync --abort
     ```

---

## 3. Resuming Interrupted Synchronizations

If a sync stops after network loss, laptop sleep, a timeout, or a system restart:

1. Check current recovery status:

   ```bash
   spas status
   ```

2. Resolve the reported cause, such as missing credentials or a filesystem error,
   then retry:

   ```bash
   spas sync
   ```

3. Check `spas status` again. A successful sync clears its pending journal. If
   SPAS reports a merge conflict, [resolve or abort the merge](#private_merge_conflict-exit-code-6).
   Keep any reported recovery copies until you have checked the resulting files.

Pending recovery can block new enrollment or removal requests. Resolve it before
adding more work; do not edit state files or the managed checkout to bypass it.

---

## 4. Getting Support

If you run into an issue not covered here:

- [Ask a question](https://github.com/getspas/spas/discussions/categories/q-a).
- [Report a bug](https://github.com/getspas/spas/issues/new?template=bug-report.yml)
  with your OS, the failing command, `spas version`, and reviewed/redacted
  `spas doctor --json` output.
- [Report a security vulnerability privately](https://github.com/getspas/spas/security/advisories/new).
