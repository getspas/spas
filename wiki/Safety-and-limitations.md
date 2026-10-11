# Safety & Limitations

SPAS stops immediately and reports the condition when it encounters an unsupported path, ambiguous Git state, or potential data race. Review these operational boundaries before using it in your workflows.

---

## 1. Repository Visibility & Access Control

- **Repository Visibility:** Link and sync probe anonymous readability with Git credential helpers and prompts disabled. Detected public readability emits a warning without requiring confirmation. Dry-run and local merge abort skip the probe. An unsuccessful probe does not prove private visibility; authenticated Git operations determine usability. Select storage with appropriate access controls for sensitive files. Authorized readers, including the storage provider, are trusted to read asset contents; SPAS provides no provider-blind encryption and does not define arbitrary providers' privacy semantics.
- **Local Workspace Permissions:** SPAS keeps managed assets untracked in your project repository. Files it materializes during sync inherit standard Git checkout semantics — created with maximal modes filtered by your process umask, exactly as `git clone` of the linked repository would produce — and only Git's executable bit is preserved across machines. Recovery copies under the SPAS data directory remain owner-only. Anyone with local read access to your project workspace directory can read the files.
- **Git URL Rewrites:** SPAS verifies its recorded origin URL, but respects your system and global Git configuration (including `url.*.insteadOf` and `pushInsteadOf` rewrites). Ensure your global Git configuration points to trusted remotes.

---

## 2. Workspace & Filesystem Concurrency

- **Advisory File Locking:** SPAS coordinates operations using OS-level advisory locks on link operations (in user data storage) and local exclusions (`.git/info/exclude.spas.lock`). Other processes modifying `.git/info/exclude` without acquiring this lock may race with SPAS.
- **Atomic File Operations:** SPAS writes synchronized assets to temporary staging files on the destination filesystem and renames them into place. While this minimizes corruption risks, cross-platform crash durability depends on host filesystem semantics.
- **Concurrent Edits:** Avoid modifying managed assets in your IDE or editor while `spas sync` is actively materializing files or `spas unlink --remove-files` is deleting them. Cleanup rejects observed changes after inspection, but noncooperating edits or Git operations can still race the final checks and filesystem operations.

---

## 3. Worktree & Repository Invariants

| Invariant | Requirement | Rationale |
| :--- | :--- | :--- |
| **Sole Worktree Rule** | Exactly one active worktree per Git common directory (`.git`). | Prevents exclusion collision across sibling worktrees. |
| **Single Linked Repo** | One linked GitHub repository per project workspace. | Eliminates ambiguous multi-remote ownership. |
| **Dedicated Branch** | One fixed branch bound at initialization. | Ensures linear, deterministic asset history. |

---

## 4. Supported File Types & Path Constraints

### Supported Files & Permission Normalization

- **Standard Regular Files (`100644`):** Non-executable regular POSIX files.
- **Executable Regular Files (`100755`):** Executable scripts and binaries. SPAS preserves the POSIX executable permission bit across synchronization.
- **Permission Normalization:** Git tracks only whether a file is executable (`100755`) or non-executable (`100644`). Other file attributes—such as setuid/setgid bits, sticky bits, extended filesystem attributes, and platform-specific ACLs—are normalized to standard Git modes upon commit. On Windows, executable attributes map to Git's standard executable-bit detection (`core.fileMode`).

### Strictly Prohibited & Rejected Paths

- **Symlinks & Directory Junctions:** Disallowed to prevent symlink traversal vulnerabilities.
- **Submodules & LFS Pointers:** Git submodules and Git LFS pointer files are not supported.
- **Special Git Files:** `.gitignore`, `.gitattributes`, and `.gitmodules` cannot be managed by SPAS.
- **Unicode Control & Format Characters:** Control characters and Unicode category `Cf` characters (such as U+200C ZWNJ and U+200D ZWJ) are rejected to prevent homograph and visual spoofing issues.
- **Non-Portable Filenames & Excessive Path Lengths:** Filename components exceeding 255 bytes and files with case-collision risks across Windows, macOS, and Linux are rejected on all platforms. On Windows, SPAS conservatively rejects paths when either the workspace or private-clone absolute path reaches 260 UTF-8 bytes. This byte-count preflight is stricter than the native `MAX_PATH` character limit for non-ASCII names. It is machine-local; another machine's roots are checked there, and Git or the filesystem can impose additional limits.

---

## 5. Git Safeguards & Destructive Operations

Link state determines enrollment. Manual changes to `.git/info/exclude` do not enroll or remove assets. When a command writes exclusions, it reconstructs missing or edited recognizable owned rules and emits `exclusion_drift_repaired` if it repaired unexpected changes. Ordinary enrollment and removal changes do not produce this warning. Dry-run and `spas doctor` remain read-only.

Only exact `# BEGIN SPAS link=<block-id>` and `# END SPAS link=<block-id>` lines identify the owned block. SPAS preserves outside rules. If both markers become unrecognizable, their text and old rules remain external content; SPAS may append a new owned block, and the old external rules can survive unlink. Review those rules manually if files remain ignored after unlink.

Malformed or duplicate matching markers cause exit 9. Inspect the exclude file named in the error and restore exactly one correctly ordered matching BEGIN/END pair, or remove only the verified owned block and retry. Preserve outside rules; SPAS does not guess ownership.

SPAS rereads and rebuilds under its advisory lock, preserving outside edits completed before that read. The lock does not prevent a noncooperating editor from racing atomic replacement, removing exclusions later, or exposing files between commands. Avoid concurrent writes during SPAS mutations and edit outside its block.

The local exclusion block prevents standard Git operations from tracking managed files. However, it is subject to Git precedence rules:

> [!WARNING]
>
> - **`.gitignore` Negation Precedence:** Git gives project `.gitignore` rules higher precedence than `.git/info/exclude`, which takes precedence over the global `core.excludesFile`. If a project `.gitignore` contains a negation such as `!*.json` or `!config/dev.json`, SPAS's `verifyExclusion` safety probe detects that the asset is no longer effectively ignored and halts with exit code 9 (`exclusion_validation_failed`).
> - `git add -f` (force add) will bypass exclusion rules and stage private assets in your main repository.
> - Destructive Git commands like `git clean -xdf`, forced checkouts (`git checkout -f`), or hard resets (`git reset --hard`) can delete or overwrite excluded files.
> - **Best Practice:** Run `spas sync` before performing destructive Git operations, and review `git status` before committing.

---

## 6. Resource Limits & Quotas

SPAS enforces safety limits to prevent runaway resource consumption:

| Resource | Enforced Limit |
| :--- | :--- |
| **Managed Tree Size** | Up to **10,000 recursive file entries** |
| **Tree Metadata** | Up to **16 MiB** captured tree metadata |
| **Captured Git Command Output** | Up to **16 MiB stdout** and **1 MiB stderr** |
| **Streaming Git Command Output** | Full output forwarded; last **64 KiB per stream** retained for diagnostics |

SPAS does not place hard quotas on individual blob sizes or overall network transfers, though large assets are constrained by available disk space and network bandwidth.

---

## 7. Unlink Cleanup

Default unlink keeps workspace files and removes SPAS's local exclusions.
Optional `--remove-files` cleanup derives targets from managed, pending, and
owned recovery metadata. Explicitly skipped, unenrolled files remain untouched.
Before deletion, cleanup validates owned exclusion markers and refuses all
targets if project Git now tracks any target.

Workspace contents and executable state are compared with the last completed
synchronization, even when the managed checkout contains an unpushed commit.
Unsynchronized changes and never-synchronized additions require separate discard
approval naming the affected files, or `--discard-unsynced-changes` with
`--remove-files`. General file-removal approval, `--yes`, and `--force` cannot
substitute for that decision. Missing required approval returns exit code 4
without deleting files.

Deletion is bound to inspected contents, existence, and executable state. Later
observed edits or unsupported type substitutions stop cleanup. Link metadata
remains available if workspace cleanup or unlink metadata updates fail, allowing
a retry after resolving the failure. Already removed synchronized contents can
be restored through sync; approved discarded unsynchronized bytes cannot be
reconstructed by SPAS. Multi-file cleanup is not atomic.

Internal checkout removal is a separate option during unlink. If
`--remove-private-clone` fails after successful unlink, the command returns
nonzero and names the remaining checkout paths. The workspace remains unlinked.

---

## Summary Checklist

- [x] Set linked GitHub repository visibility to **Private** for sensitive assets.
- [x] Ensure your repository is a single worktree.
- [x] Use regular files (avoid symlinks, junctions, or submodules).
- [x] Run `spas sync` regularly to keep assets safely backed up on GitHub.
