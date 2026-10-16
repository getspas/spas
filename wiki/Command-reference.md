# Command Reference

SPAS commands are interactive by default. For scripts and continuous integration (CI), supply decisions through CLI flags. Use `--json` for structured output with prompts disabled.

---

## Global Options

The following flags apply to all SPAS commands:

| Option | Type | Description |
| :--- | :--- | :--- |
| `--repo PATH` | String | Path to the project Git workspace directory (defaults to `.`) |
| `--git PATH` | String | Git executable; relative filesystem paths resolve from the invocation directory, and bare names use `PATH` |
| `--non-interactive` | Flag | Disable SPAS and supported Git authentication prompts; fail if a required decision is missing |
| `--json` | Flag | Output structured JSON to stdout and disable interactive prompts |
| `--progress MODE` | `auto` \| `never` | Select phase progress on stderr (default: `auto`) |
| `--color MODE` | `auto` \| `always` \| `never` | Select terminal color (default: `auto`); machine output stays plain |
| `-y, --yes` | Flag | Automatically accept non-destructive setup suggestions |
| `-v, --verbose` | Flag | Output detailed diagnostic logs (excludes sensitive asset contents) |
| `--timeout DURATION` | String | Maximum execution duration (e.g. `30s`, `5m`; default: no timeout) |
| `-h, --help` | Flag | Display help information for the command |
| `--version` | Flag | Display version information (root command only) |

> [!NOTE]
> `-y` / `--yes` automatically accepts non-destructive setup suggestions. It does *not* authorize committing assets, deleting files, overriding tracked repository paths, discarding uncommitted workspace changes, or resolving merge conflicts.

Human output uses restrained color for result labels, diagnostic statuses,
warnings, errors, and Git diffs. Each destination is checked separately:
redirecting stdout leaves its automatic output plain even when stderr is a
terminal. Presentation is independent of terminal input and prompt availability.

`--color=auto` uses supported output terminals. `--non-interactive`, a nonempty
`CI` or `NO_COLOR`, and `TERM=dumb` disable automatic color. Unix terminals need
a recognized ANSI color terminal type in `TERM`; empty or unknown types use
plain text. Windows consoles need virtual-terminal processing already enabled;
SPAS leaves console modes unchanged. `--color=always` overrides automatic policy
and emits color in redirected human output. `--color=never` disables color.
Invalid values return usage exit code 2. See the [NO_COLOR convention](https://no-color.org/).

JSON, shell-completion scripts and requests, `status --short`, and
`diff --name-only` remain plain for every color setting. Git diffs receive the
stdout color policy explicitly because forwarded pipes do not establish native
terminal attachment. Results stay on stdout; warnings and errors stay on stderr.
Color supplements readable labels.

`--progress=auto` animates real phases and available file counts on supported
stderr terminals. Fast operations finish before animation appears. Progress
is independent of color, `NO_COLOR`, and prompt availability. It is disabled
for `--non-interactive`, CI, redirected stderr, unsupported/dumb terminals,
JSON, completion, `status --short`, and `diff --name-only`.
`--progress=never` disables motion while retaining warnings and errors;
invalid values return usage exit code 2.

Transient progress stays on stderr and fits within its terminal width.
Animation clears before prompts, diagnostics, and results. Streaming Git and
remote operations use a stable phase line while the child runs. Motion resumes
after terminal ownership returns and clears on success, failure, timeout, or
cancellation. SPAS leaves cursor visibility, input mode, and console modes
unchanged.

---

## `spas link`

Establish a local association between your project workspace and a linked GitHub repository.

```text
spas link [OWNER/REPOSITORY | GITHUB-URL] [flags]
```

`spas link` validates the workspace worktree structure and writes local link state without cloning, fetching, or editing workspace files. Except with `--dry-run`, it probes anonymous readability with Git credential helpers and prompts disabled. Detected public readability produces a warning on stderr and requires no approval, including in non-interactive mode. Sync checks again on each invocation that reaches its visibility check; dry-run and local merge abort do not probe. An unsuccessful probe does not prove private visibility; authenticated Git operations determine usability. Owner and repository names are case-insensitive and canonicalized to lowercase.

| Option | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--transport` | `https` \| `ssh` | *Prompt* / `https` | Git transport protocol for `OWNER/REPOSITORY` references (defaults to `https` in non-interactive mode) |
| `--branch` | String | *Auto* | Target branch in the linked repository (required for empty repositories) |
| `--replace` | Flag | `false` | Replace an unused, pristine link association without deleting its clone |
| `--dry-run` | Flag | `false` | Validate arguments and display proposed link settings without saving or network access |

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

`spas add` operates offline. It registers paths in local SPAS state and adds corresponding exclusion patterns to `.git/info/exclude`. Your project `.gitignore` remains unchanged. Link state determines enrollment; editing exclusions does not change the managed asset set. Exclusion-writing commands rebuild missing or edited owned rules and report unexpected repairs on stderr (JSON code `exclusion_drift_repaired`). Ordinary requested enrollment/removal changes stay quiet. See [Safety and Limitations](Safety-and-limitations) for ownership markers, manual repair, and concurrency limits.

Enrollment uses the actual native directory-entry spelling. Distinct case or
Unicode entries remain independent when both destinations support them. Imported
asset-repository paths keep their exact Git spelling. Owned exclusions include
native and precomposed aliases where necessary for project Git enumeration;
those rules do not enroll additional assets.

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

### Verified Merge Protection

SPAS verifies `branch.<name>.mergeOptions` only when there is one value stored
directly in the repository config file containing `--no-overwrite-ignore`. It may also
contain `--no-edit`, `--log`, and `--no-ff`, separated by ASCII whitespace.
With these flags, `enable` preserves the original local value for unlink
restoration and adds `--no-overwrite-ignore` when needed.

Multiple values, included values, non-local scopes, quoted or argument-taking options,
`--overwrite-ignore`, and other flags are reported as unverified. `require`
and `enable` reject that configuration. Configure a single supported local
value before using those policies; `skip` leaves merge protection to you.

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

File selection reads existing directory entries without creating a workspace
probe. Exact stored spellings take priority. An existing case or Unicode alias
selects the enrolled path only when both spellings address the same native
entry; distinct hard-link names remain distinct entries. Missing files require
an exact stored spelling, including pending removals and staged deletions.
`core.ignoreCase` does not establish native equivalence for diff selection.

Accepted aliases select the authoritative enrolled filename in JSON, name-only,
patch, and stat output, including `--staged`. The original selected spelling is
checked before accepting a case or Unicode-normalization alias: a distinct
existing neighbor cannot select the enrolled file. An explicit file selection
that matches no candidate produces an empty result.
Staged renames expose both their source and destination as selectable paths;
unfiltered patch output keeps Git's normal rename presentation.
Use an exact stored spelling when an alias could refer to both sides of a
case-only rename.

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

Check Git configuration, path collisions, exclusion block integrity, merge protection, and crash recovery state offline.

```text
spas doctor [flags]
```

- When run with `--json`, `spas doctor` outputs a single diagnostic JSON object to stdout.
- Returns exit code `0` when healthy, or nonzero when issues require attention.
- When no Git repository is found, or the repository is not linked, `doctor` runs the available environment checks (Git version, data directories, advisory locking, and worktree shape where applicable). If those checks pass, it exits `0` with a `workspace` or `link-state` warning explaining that link checks were skipped.
- Failed repository inspection is an error. Corrupt configuration, unreadable or unrecognized Git metadata, cancellation, and invalid Git output produce an unhealthy result and nonzero status. A no-repository warning requires Git's absence diagnostic and no `.git` marker in the physical directory ancestry; existing metadata that Git cannot inspect requires attention.

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
| `--discard-unsynced-changes` | Flag | `false` | Separately approve discarding unsynchronized contents and executable changes; requires `--remove-files` |
| `--keep-private-clone` | Flag | `true` | Retain the local managed checkout in application storage (default) |
| `--remove-private-clone` | Flag | `false` | Delete the local managed checkout from storage |
| `--force` | Flag | `false` | Allow unlink despite pending synchronization or checkout recovery; does not approve workspace deletion or unsynchronized discard |

Rules outside the exact owned block are preserved. If old ownership markers no longer match, those rules remain external and may survive unlink; review them manually if files stay ignored.

Cleanup checks every owned target for project Git ownership and validates the
exclusion-block structure before deleting files. A conflict or malformed block
refuses the entire cleanup. General removal approval covers synchronized files;
unsynchronized contents, executable changes, and never-synchronized additions
require a separate prompt naming the affected paths or
`--discard-unsynced-changes`. Neither `--yes` nor `--force` supplies that approval.
Missing approval returns exit code 4 before deletion. Complete options run
without prompting, including with `--json`.

```bash
# Remove synchronized workspace assets
spas unlink --remove-files --approve-remove-files --non-interactive

# Separately approve abandoning pending work and discarding unsynchronized files
spas unlink --force --remove-files --approve-remove-files \
  --discard-unsynced-changes --non-interactive
```

The managed checkout stays unless `--remove-private-clone` is supplied. Cleanup
failure after link removal returns nonzero, states that unlink succeeded, and
names the retained checkout paths.

> [!WARNING]
>
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

## Exit Codes, JSON Schemas & Errors

When using `--json`, all responses (including errors) adhere to the versioned [JSON Output Schema](JSON-output-schema).

Errors are returned as structured JSON objects with `schemaVersion`:

```json
{
  "schemaVersion": 3,
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
| `7` | `auth_or_network` | Git authentication or network failure when contacting remote provider. |
| `8` | `unsafe_git_state` | Unsafe Git state detected (detached HEAD, uncommitted project merge, multiple worktrees). |
| `9` | `exclusion_validation_failed` | `.git/info/exclude` does not match SPAS state or tracked `.gitignore` conflicts. |
| `10` | `lock_held` | Another SPAS process is holding the link advisory lock. |
| `11` | `unsupported_path` | Path is not a regular file (symlinks, junctions, control characters, or invalid encodings). |
| `130` | `interrupted` | Execution cancelled by user interrupt (Ctrl+C / SIGINT). |

`--timeout` expiry returns exit code `1` (`operation_failed`), including during
remote Git operations. User signals return exit code `130` (`interrupted`).

### Unattended authentication

Complete inputs run directly. `--non-interactive`, `--json`, or nonterminal
input/output prevents SPAS prompts; a missing decision returns exit code 4 and
names the required option. `--yes` covers recommended setup choices only.
Credentials must be provisioned before unattended execution.

SPAS disconnects inherited terminal input from unattended streaming Git while
preserving explicit plumbing input. It disables Git terminal/askpass prompts,
sets `SSH_ASKPASS_REQUIRE=never` and `GCM_INTERACTIVE=never`, and passes
`credential.interactive=false` to Git commands. These controls also cover
implicit fetches when reading a partial project clone. Stored HTTPS credentials
remain available to configured helpers.

Without custom SSH selection, SPAS uses OpenSSH with `BatchMode=yes`: keys or
an agent and verified known-host entries must already work. Custom selection
through `GIT_SSH_COMMAND`, `GIT_SSH`, `GIT_SSH_VARIANT`, `core.sshCommand`, or
`ssh.variant` is preserved. Configure such commands for unattended use yourself
(for example, `GIT_SSH_COMMAND='ssh -oBatchMode=yes -i /path/to/key'`). Other
credential helpers, proxy commands, and custom interfaces must likewise be
configured to avoid prompts; these controls cannot guarantee that arbitrary
external programs remain prompt-free. SPAS never rewrites global Git or SSH
configuration and preserves Git authentication diagnostics (exit code 7).

`--timeout 5m` bounds command execution; there is no default deadline. Timeout
or cancellation kills the unattended Git process group on macOS/Linux and uses
the Windows system `taskkill /T /F` to stop the process tree. If Windows tree
termination is unavailable, direct Git termination still applies. Detached
helpers and pre-existing services are outside this boundary. Sync releases its
link lock and retains pending recovery state so a subsequent sync can resume.
