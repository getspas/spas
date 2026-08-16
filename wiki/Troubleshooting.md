# Troubleshooting Guide

When encountering unexpected behavior, SPAS provides built-in offline diagnostic tools to help identify and resolve issues quickly.

---

## 1. Quick Diagnostic Triage

Always start by checking local state and running health checks:

```bash
# Check link status, managed assets, and recovery state
spas status

# Run automated offline health diagnostics
spas doctor
```

### Generating Sanitized JSON for Bug Reports

For automated analysis or when filing an issue report, generate structured JSON output:

```bash
spas doctor --json
```

> [!WARNING]
> **Protect Sensitive Data:** Never paste unedited `spas diff` output into public forums or issue trackers, as it may contain secret keys or private file contents. Use `spas diff --stat` or `spas diff --name-only` when sharing reports publicly.

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

### `github_auth_or_network` (Exit Code 7)

- **Cause:** Git was unable to authenticate with GitHub or encountered a network timeout.
- **Fix:**
  - Verify your SSH keys (`ssh -T git@github.com`) or HTTPS credential helper.
  - Confirm repository permissions for your GitHub user account.
  - Test raw Git connectivity to the remote URL.

---

### `path_conflict` (Exit Code 5)

- **Cause:** A managed asset shares a path with a file already tracked by your main project Git repository.
- **Fix:**
  - To keep the main repository version: use `--conflict=skip`.
  - To transfer ownership to SPAS: use `--conflict=override`. *(SPAS stages deletion in the project Git index; you can review with `git diff --cached` before committing.)*

---

### `unsupported_path` (Exit Code 11)

- **Cause:** An asset is a symlink, directory junction, submodule, or contains non-portable characters (such as Unicode `Cf` characters or control codes).
- **Fix:** Ensure all managed assets are standard regular files with valid filenames.

---

### `unsafe_git_state` (Exit Code 8)

- **Cause:** SPAS detected multiple worktrees, an in-progress rebase/merge in the main repository, or an unexpected commit in the managed checkout.
- **Fix:** Run `spas doctor` to pinpoint the unsafe condition. Complete or abort any pending Git operations in your project repository before syncing.

---

### `exclusion_validation_failed` (Exit Code 9)

- **Cause:** A managed path is not effectively excluded from the project's primary Git repository. This typically happens when a `.gitignore` file (or a global `core.excludesFile`) contains a negation pattern (`!path` or `!*.json`) that re-includes a path. Because Git evaluates `.gitignore` with higher precedence than `.git/info/exclude`, the negation rule defeats SPAS's local exclusion block.
- **Why SPAS Fails Closed:** If SPAS allowed materialization while a negation rule was active, standard Git commands (`git status`, `git add .`, `git commit`) in your main project repository would track and stage your private assets. SPAS verifies effective exclusion via `git check-ignore --no-index` before mutating the workspace and halts immediately if any managed path is not effectively ignored.
- **How to Diagnose & Fix:**
  1. Identify which rule is re-including the path:

     ```bash
     git check-ignore -v --no-index path/to/file
     ```

  2. Open the reported `.gitignore` file and remove or adjust the negation pattern (e.g. change `!config/dev.json` so it does not match SPAS-managed private files).
  3. Run `spas doctor` to verify that all managed paths are now effectively excluded.

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

If a synchronization process is interrupted (network loss, laptop sleep, or system restart):

1. Check current recovery status:

   ```bash
   spas status
   ```

2. Re-run `spas sync` to complete the transaction safely:

   ```bash
   spas sync
   ```

3. If recovery files are present under your user data directory after a successful sync, SPAS will automatically clear temporary journals.

---

## 4. Getting Support

If you run into an issue not covered here:

- 💬 **Ask the community:** [GitHub Discussions Q&A](https://github.com/getspas/spas/discussions/categories/q-a)
- 🐛 **Submit a bug report:** [GitHub Issues](https://github.com/getspas/spas/issues/new?template=bug-report.yml) (include output from `spas version` and sanitized `spas doctor --json`)
