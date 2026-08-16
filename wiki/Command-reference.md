# Command Reference

SPAS commands are interactive by default. For scripting, continuous integration (CI), and automation, supply decisions explicitly via CLI flags or use `--json` to receive structured outputs and disable prompts.

---

## Global Options

The following flags apply to all SPAS commands:

| Option | Type | Description |
| :--- | :--- | :--- |
| `--repo PATH` | String | Path to the project Git workspace directory (defaults to `.`) |
| `--git PATH` | String | Custom path to the Git executable |
| `--non-interactive` | Flag | Disable interactive prompts; fails if any required decision flag is missing |
| `--json` | Flag | Output structured JSON to stdout and disable interactive prompts |
| `-y, --yes` | Flag | Automatically accept non-destructive setup suggestions |
| `-v, --verbose` | Flag | Output detailed diagnostic logs (excludes sensitive asset contents) |
| `-h, --help` | Flag | Display help information for the command |
| `--version` | Flag | Display version information (root command only) |

> [!NOTE]
> `-y` / `--yes` automatically accepts non-destructive setup suggestions. It does *not* authorize committing assets, deleting files, overriding tracked repository paths, discarding uncommitted workspace changes, or resolving merge conflicts.

---

## `spas link`

Establish a local association between your project workspace and a linked GitHub repository.

```text
spas link [OWNER/REPOSITORY | GITHUB-URL] [flags]
```

`spas link` validates the workspace worktree structure and writes local link state without cloning, fetching, or editing workspace files. It verifies repository visibility using an anonymous probe and prompts for confirmation if the repository is publicly readable.

| Option | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--transport` | `https` \| `ssh` | *Prompt* / `https` | Git transport protocol for `OWNER/REPOSITORY` references (defaults to `https` in non-interactive mode) |
| `--branch` | String | *Auto* | Target branch in the linked repository (required for empty repositories) |
| `--replace` | Flag | `false` | Replace an unused, pristine link association without deleting its clone |
| `--dry-run` | Flag | `false` | Validate arguments and display proposed link settings without saving |
| `--allow-public` | Flag | `false` | Allow linking a publicly readable repository without confirmation |
### Link Examples

```bash
# Interactive setup
spas link

# SSH link with explicit branch
spas link my-org/project-assets --transport ssh --branch main

# HTTPS link for CI automation
spas link https://github.com/my-org/project-assets.git --non-interactive
```

---

## `spas add`

Enroll local regular files under SPAS management.

```text
spas add PATH... [flags]
```

`spas add` operates offline. It registers paths in local SPAS state and adds corresponding exclusion patterns to `.git/info/exclude`. Your project `.gitignore` remains unchanged.

| Option | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--existing-exclude` | `ask` \| `preserve` \| `abort` | `ask` | How to handle existing rules in `.git/info/exclude` |
| `--merge-protection` | `ask` \| `enable` \| `skip` \| `require` | `ask` | Policy for configuring `--no-overwrite-ignore` on branch merges |
| `--skip-tracked` | Flag | `false` | Skip files that are already tracked by the project repository |
| `--dry-run` | Flag | `false` | Preview additions and local exclusion edits without saving |

### Add Examples

```bash
# Add specific configuration and test files
spas add config/dev.json testdata/mock-api.json docs/internal-notes.md

# Non-interactive addition with explicit policies
spas add config/local.env \
  --existing-exclude preserve \
  --merge-protection enable \
  --non-interactive
```

---

## `spas remove`

Mark managed assets for removal on the next synchronization.

```text
spas remove PATH... [flags]
```

`spas remove` registers a pending deletion in local state; it does not immediately delete workspace files or push to GitHub.

> [!TIP]
> **Pending Additions:** If you remove a file that was added with `spas add` but has not yet been synced to GitHub, SPAS cancels the enrollment immediately, removes the exclusion rule, and leaves the workspace file intact.

| Option | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--missing-ok` | Flag | `false` | Do not fail if a specified path is not currently managed by SPAS |
| `--dry-run` | Flag | `false` | Preview planned removals without updating local state |

### Remove Examples

```bash
spas remove docs/deprecated-notes.md --non-interactive
```

---

## `spas sync`

Synchronize workspace assets with the linked GitHub repository.

```text
spas sync [flags]
spas sync --continue [flags]
spas sync --abort
```

During a normal sync:

1. SPAS clones or fetches the linked repository.
2. Prompts for approval to create a commit in the linked repository.
3. Merges remote updates.
4. Pushes commits to GitHub (without force-pushing).
5. Materializes updated files safely in your project workspace.

SPAS never creates commits in your project repository.

| Option | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `-m, --message` | String | — | Commit message and explicit approval for the linked repository commit |
| `--message-file` | Path | — | Read commit message from a file (mutually exclusive with `--message`) |
| `--conflict` | `ask` \| `skip` \| `override` \| `abort` | `ask` | Policy for path collisions between project repo and managed assets |
| `--force` | Flag | `false` | Convenience alias for `--conflict=override` |
| `--skip-conflicts` | Flag | `false` | Convenience alias for `--conflict=skip` |
| `--discard-public-changes` | Flag | `false` | Allow overriding uncommitted workspace changes during ownership transfer |
| `--existing-exclude` | `ask` \| `preserve` \| `abort` | `ask` | Policy for existing rules in `.git/info/exclude` |
| `--merge-protection` | `ask` \| `enable` \| `skip` \| `require` | `ask` | Policy for Git merge protection settings |
| `--branch` | String | — | Specify the branch during first synchronization |
| `--continue` | Flag | `false` | Continue a merge in the linked repository after resolving conflicts |
| `--abort` | Flag | `false` | Abort an active merge and restore the pre-merge workspace state |
| `--dry-run` | Flag | `false` | Read-only simulation without taking mutation locks or making network calls |
| `--allow-public` | Flag | `false` | Allow syncing to a publicly readable repository without confirmation |
### Sync Examples

```bash
# Interactive sync
spas sync

# Fully explicit non-interactive sync for CI/CD
spas sync \
  --message "Update dev configs for sprint 4" \
  --conflict abort \
  --existing-exclude preserve \
  --merge-protection enable \
  --non-interactive

# Continue merge after editing conflicted files in workspace
spas sync --continue --message "Resolve team notes merge conflict"

# Abort an in-progress merge
spas sync --abort
```

---

## `spas status`

Display the offline status of your link association, managed assets, pending additions/removals, active merges, and recovery state.

```text
spas status [flags]
```

| Option | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--short` | Flag | `false` | Print a concise single-line status summary |
| `--show-paths` | Flag | `false` | Display absolute filesystem paths for workspace and storage checkouts |

### Status Examples

```bash
spas status --show-paths
```

---

## `spas diff`

Compare managed assets in your local project workspace against the local managed checkout.

```text
spas diff [PATH...] [flags]
```

| Option | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--name-only` | Flag | `false` | Output only the names of changed paths |
| `--stat` | Flag | `false` | Display a statistical diff summary |
| `--staged` | Flag | `false` | Display changes staged during an interrupted synchronization |

> [!WARNING]
> Standard `spas diff` output prints raw asset content. Do not paste full diffs into public issue trackers or chat rooms. Use `--stat` or `--name-only` when sharing diagnostics.

---

## `spas doctor`

Perform comprehensive offline diagnostics on Git configuration, path collisions, exclusion block integrity, merge protection, and crash recovery state.

```text
spas doctor [flags]
```

- When run with `--json`, `spas doctor` outputs a single diagnostic JSON object to stdout.
- Returns exit code `0` when healthy, or nonzero when issues require attention.

---

## `spas unlink`

Remove the local SPAS association and clean up managed lines from `.git/info/exclude`.

```text
spas unlink [flags]
```

| Option | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--keep-files` | Flag | `true` | Retain managed files in the workspace (default) |
| `--remove-files` | Flag | `false` | Delete managed assets from the project workspace |
| `--approve-remove-files` | Flag | `false` | Explicit approval for `--remove-files` in non-interactive mode |
| `--keep-private-clone` | Flag | `true` | Retain the local managed checkout in application storage (default) |
| `--remove-private-clone` | Flag | `false` | Delete the local managed checkout from storage |
| `--force` | Flag | `false` | Bypass pending-recovery and asset safety checks when unlinking |

> [!WARNING]
> Unlinking with `--keep-files` removes the `.git/info/exclude` rules. Git will immediately see those files as untracked. Review `git status` before committing to avoid accidental secret leaks.

---

## `spas completion`

Generate shell auto-completion scripts for your preferred shell.

```bash
# Bash
source <(spas completion bash)

# Zsh
source <(spas completion zsh)

# Fish
spas completion fish | source

# PowerShell
spas completion powershell | Out-String | Invoke-Expression
```

---

## `spas version`

Display version number, build commit hash, and build timestamp.

```bash
spas version
```

---

## Exit Codes & Errors

When using `--json`, errors are returned as structured JSON objects:

```json
{
  "ok": false,
  "error": {
    "code": "decision_required",
    "message": "local managed asset changes require approval to commit config/dev.json to the linked repository; provide --message"
  }
}
```

### Exit Code Reference Table

| Exit Code | Error Code | Description & Troubleshooting |
| :---: | :--- | :--- |
| `0` | `success` | Operation completed successfully. |
| `1` | `operation_failed` | General execution failure. Check error message details. |
| `2` | `invalid_usage` | Invalid CLI syntax, mutually exclusive flags, or missing arguments. |
| `3` | `not_linked` | Workspace is not linked with SPAS. Run `spas link` first. |
| `4` | `decision_required` | Required decision missing in non-interactive mode (e.g. `--message` or `--conflict`). |
| `5` | `path_conflict` | Path collision with a file tracked by the main project Git repository. |
| `6` | `private_merge_conflict` | Merge conflict in the linked repository. Resolve conflicts, then run `spas sync --continue`. |
| `7` | `github_auth_or_network` | Git authentication or network failure when contacting GitHub. |
| `8` | `unsafe_git_state` | Unsafe Git state detected (detached HEAD, uncommitted project merge, multiple worktrees). |
| `9` | `exclusion_validation_failed` | `.git/info/exclude` does not match SPAS state or tracked `.gitignore` conflicts. |
| `10` | `lock_held` | Another SPAS process is holding the link advisory lock. |
| `11` | `unsupported_path` | Path is not a regular file (symlinks, junctions, control characters, or invalid encodings). |
| `130` | `interrupted` | Execution cancelled by user interrupt (Ctrl+C / SIGINT). |
