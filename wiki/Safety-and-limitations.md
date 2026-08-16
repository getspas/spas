# Safety & Limitations

SPAS is engineered around a fail-safe security model: when encountering an unsupported path, ambiguous Git state, or potential data race, it stops immediately and reports the exact condition rather than guessing.

Please review these operational boundaries before integrating SPAS into your workflows.

---

## 1. Repository Visibility & Access Control
- **Public vs. Private Repositories:** SPAS automatically verifies linked repository visibility using an offline-credential-free probe (`git ls-remote` with credential helpers and prompts disabled). If the linked repository is publicly readable, SPAS requires explicit interactive confirmation or the `--allow-public` CLI flag to prevent accidental exposure of managed assets. Always ensure your repository is configured as **Private** on GitHub before syncing sensitive files.
- **Local Workspace Permissions:** SPAS keeps managed assets untracked in your project repository, but does not alter local filesystem file permissions. Anyone with local read access to your project workspace directory can read the files.
- **Git URL Rewrites:** SPAS verifies its recorded origin URL, but respects your system and global Git configuration (including `url.*.insteadOf` and `pushInsteadOf` rewrites). Ensure your global Git configuration points to trusted remotes.
---

## 2. Workspace & Filesystem Concurrency

- **Advisory File Locking:** SPAS coordinates operations using OS-level advisory locks on link operations (in user data storage) and local exclusions (`.git/info/exclude.spas.lock`). Other processes modifying `.git/info/exclude` without acquiring this lock may race with SPAS.
- **Atomic File Operations:** SPAS writes synchronized assets to temporary staging files on the destination filesystem and renames them into place. While this minimizes corruption risks, cross-platform crash durability depends on host filesystem semantics.
- **Concurrent Edits:** Avoid modifying managed assets in your IDE or editor while `spas sync` is actively materializing files.

---

## 3. Worktree & Repository Invariants

| Invariant | Requirement | Rationale |
| :--- | :--- | :--- |
| **Sole Worktree Rule** | Exactly one active worktree per Git common directory (`.git`). | Prevents exclusion collision across sibling worktrees. |
| **Single Linked Repo** | One linked GitHub repository per project workspace. | Eliminates ambiguous multi-remote ownership. |
| **Dedicated Branch** | One fixed branch bound at initialization. | Ensures linear, deterministic asset history. |

---

## 4. Supported File Types & Path Constraints

### Supported Files

- Regular POSIX files with Git modes `100644` (standard) and `100755` (executable).

### Strictly Prohibited & Rejected Paths

- **Symlinks & Directory Junctions:** Disallowed to prevent symlink traversal vulnerabilities.
- **Submodules & LFS Pointers:** Git submodules and Git LFS pointer files are not supported.
- **Special Git Files:** `.gitignore`, `.gitattributes`, and `.gitmodules` cannot be managed by SPAS.
- **Unicode Control & Format Characters:** Control characters and Unicode category `Cf` characters (such as U+200C ZWNJ and U+200D ZWJ) are rejected to prevent homograph and visual spoofing issues.
- **Non-Portable Filenames:** Files with case-collision risks across Windows, macOS, and Linux are rejected.

---

## 5. Git Safeguards & Destructive Operations

The local exclusion block inside `.git/info/exclude` prevents standard Git operations from tracking managed files. However, it is a convenience filter, not an access-control barrier:

> [!WARNING]
>
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
| **Git Command Output** | Up to **16 MiB stdout** and **1 MiB stderr** |

SPAS does not place hard quotas on individual blob sizes or overall network transfers, though large assets are constrained by available disk space and network bandwidth.

---

## Summary Checklist

- [x] Set linked GitHub repository visibility to **Private** for sensitive assets.
- [x] Ensure your repository is a single worktree.
- [x] Use regular files (avoid symlinks, junctions, or submodules).
- [x] Run `spas sync` regularly to keep assets safely backed up on GitHub.
