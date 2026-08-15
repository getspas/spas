<div align="center">

# SPAS: Secure Private Asset Sync

**Manage private assets in your project workspace without committing them to your main Git repository.**

SPAS (pronounced **"/spæz/"**) seamlessly connects your local workspace to a separate GitHub repository, keeping private files, environment configs, and test fixtures right where your tools expect them.

[![CI](https://github.com/getspas/spas/actions/workflows/ci.yml/badge.svg)](https://github.com/getspas/spas/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/getspas/spas?display_name=tag&sort=semver)](https://github.com/getspas/spas/releases/latest)
![Platforms](https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-4c566a)
![Architectures](https://img.shields.io/badge/arch-amd64%20%7C%20arm64-4c566a)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

[Quick Start](#quick-start) · [How It Works](#how-it-works) · [Commands](#commands) · [Documentation](https://github.com/getspas/spas/wiki) · [Discussions](https://github.com/getspas/spas/discussions)

</div>

---

Every project relies on files that don't belong in the public or shared Git repository: local `.env` secrets, developer overrides, test fixtures, API mocks, and internal team notes.

Moving these files elsewhere breaks build paths. Copying them manually across machines is slow and error-prone. Committing them risks secret leaks and repo bloat.

**SPAS solves this by bridging the gap:**

- **Zero Path Rewiring:** Your files remain in their expected workspace locations. Build scripts, tests, and IDEs work without modification.
- **Clean Main History:** Files are automatically ignored in the main project via `.git/info/exclude`, leaving your `.gitignore` untouched.
- **Dedicated Version Control:** Managed assets are versioned, committed, and synced to a dedicated, separate GitHub repository.
- **Built-in Safety:** Advisory file locking, atomic writes, dry-run previews, and fail-safe recovery protect your workspace from data loss.

---

## Why SPAS?

| Challenge | Without SPAS | With SPAS |
| :--- | :--- | :--- |
| **Asset Location** | Stored outside project or requires symlinks | Kept directly at standard project paths |
| **Main Git Cleanliness** | Cluttered `.gitignore` or accidental commits | Untracked in main repo via local `.git/info/exclude` |
| **Team & Device Sync** | Manual copy-pasting or ad-hoc scripts | One-command synchronization across machines |
| **Version History** | No audit trail for private changes | Full Git history in dedicated asset repository |
| **Automation & CI** | Brittle manual workflows | Script-friendly JSON output, dry-runs, and exit codes |

---

## How It Works

```text
┌─────────────────────────────────────────────────────────────────┐
│                        Project Workspace                        │
│                                                                 │
│   ┌────────────────────────┐      ┌─────────────────────────┐   │
│   │     Tracked Files      │      │     Managed Assets      │   │
│   │    (Main Git Repo)     │      │     (SPAS Managed)      │   │
│   └───────────▲────────────┘      └────────────▲────────────┘   │
│               │                                │                │
│               │     Excluded from Main Repo    │                │
│               │   ◄─────────────────────────   │                │
│               │      (.git/info/exclude)       │                │
└───────────────┼────────────────────────────────┼────────────────┘
                │                                │
         Normal Git Ops                      SPAS Sync
      (git push / git pull)            (spas sync / checkout)
                │                                │
                ▼                                ▼
        Main Git Remote              Linked GitHub Repository
     (Public or Shared Repo)        (Dedicated Asset Storage)
```

SPAS coordinates three distinct layers:

1. **Workspace Layer:** Assets live directly at their standard project paths. SPAS manages an owned block in `.git/info/exclude` so the main repository leaves them untracked.
2. **Coordination & Safety Layer:** SPAS acquires an OS-level advisory lock and stages updates via atomic same-filesystem renames (`/.spas-tmp/`).
3. **Dedicated Asset Storage:** Assets are committed, merged, and pushed to a separate GitHub repository with independent history.

---

## Installation

Download the pre-compiled binary for your system from the [latest GitHub release](https://github.com/getspas/spas/releases/latest), extract it, and place `spas` (or `spas.exe`) in your system `PATH`.

SPAS requires **Git 2.43.1 or newer** at runtime.

For complete verification and shell completion setup, see the [Installation Guide](https://github.com/getspas/spas/wiki/Installation).

### Build from Source

```bash
go install -trimpath github.com/getspas/spas@latest
```

---

## Quick Start

### 1. Create an Asset Repository

Create an empty repository on GitHub (e.g., `your-org/project-assets`).

### 2. Link and Add Files

Open a terminal in your project workspace and run:

```bash
# Link your workspace to the asset repository
spas link your-org/project-assets --transport ssh --branch main

# Add the private assets you want SPAS to manage
spas add config/dev.json testdata/mock-api.json docs/team-notes.md

# Synchronize assets to the linked repository
spas sync
```

### 3. Workflow Summary

1. **`spas link`** — Connects your project workspace to the dedicated asset repository (offline).
2. **`spas add`** — Tracks chosen files and creates local exclusion rules in `.git/info/exclude` (offline).
3. **`spas sync`** — Validates, commits, merges, and synchronizes assets with GitHub.

### Preview Before Syncing

Want to inspect what will happen before modifying files or committing?

```bash
spas sync --dry-run
spas diff --stat
```

---

## Commands

| Command | Description |
| :--- | :--- |
| `spas link` | Connect the project workspace to a GitHub repository |
| `spas add PATH...` | Track regular files under SPAS management |
| `spas remove PATH...` | Untrack selected assets on the next sync |
| `spas sync` | Synchronize assets between workspace and linked repository |
| `spas status` | Check link status, managed assets, collisions, and recovery state |
| `spas diff` | Compare workspace assets against the local managed checkout |
| `spas doctor` | Diagnose Git configuration, path exclusions, and recovery health |
| `spas unlink` | Remove the SPAS association while preserving workspace files |
| `spas completion` | Generate auto-completion scripts for Bash, Zsh, Fish, or PowerShell |
| `spas version` | Display version, commit hash, and build metadata |

### Non-Interactive & CI Automation

SPAS is fully scriptable. Pass `--json` and explicit policies to run in automated pipelines without interactive prompts:

```bash
spas sync --json \
  --message "Update CI environment assets" \
  --conflict abort \
  --existing-exclude preserve \
  --merge-protection skip
```

For complete details on all flags, exit codes, and policies, consult the [Command Reference](https://github.com/getspas/spas/wiki/Command-reference).

---

## Documentation & Community

- 📖 [Documentation Wiki](https://github.com/getspas/spas/wiki)
- 🚀 [Quick Start Guide](https://github.com/getspas/spas/wiki/Quick-start)
- ⚙️ [Command Reference](https://github.com/getspas/spas/wiki/Command-reference)
- 🛡️ [Safety & Limitations](https://github.com/getspas/spas/wiki/Safety-and-limitations)
- 💬 [Discussions & Support](https://github.com/getspas/spas/discussions)
- 🐛 [Report a Bug](https://github.com/getspas/spas/issues/new/choose)
- 🔒 [Security Vulnerability Report](https://github.com/getspas/spas/security/advisories/new)
