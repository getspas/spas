# SPAS Documentation

Welcome to the official SPAS documentation. SPAS keeps private and environment-specific assets in your local project workspace while synchronizing them with a dedicated, separate GitHub repository—leaving your main project Git history completely untracked and clean.

---

## Getting Started

1. **[Installation](Installation)** — Download prebuilt binaries, verify checksums, or compile from source.
2. **[Quick Start Guide](Quick-start)** — Connect your workspace, select private files, and run your first synchronization in minutes.
3. **[Command Reference](Command-reference)** — Complete syntax, flags, automation recipes, and exit code reference for all CLI commands.
4. **[Troubleshooting Guide](Troubleshooting)** — Diagnose errors with `spas doctor`, resolve merge conflicts, and sanitize debug logs.
5. **[Safety & Limitations](Safety-and-limitations)** — Review security models, worktree constraints, and supported file types.

---

## Documentation Index

| Guide | Description |
| :--- | :--- |
| **[Installation](Installation)** | Installation instructions for Linux, macOS, and Windows with SHA-256 verification and shell completion setup. |
| **[Quick Start](Quick-start)** | Step-by-step walkthrough linking a workspace, managing assets, and syncing across developer machines. |
| **[Command Reference](Command-reference)** | Detailed documentation of all commands (`link`, `add`, `remove`, `sync`, `status`, `diff`, `doctor`, `unlink`). |
| **[Troubleshooting](Troubleshooting)** | Practical solutions for common errors, conflict resolution procedures, and recovery flows. |
| **[Safety & Limitations](Safety-and-limitations)** | Security boundaries, filesystem concurrency models, supported file modes, and resource limits. |

---

## Core Concepts & Architecture

To make the most of SPAS, it helps to understand how different components interact:

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

- **Project Repository:** Your primary Git repository containing application source code. Managed assets are kept untracked here via local exclusion rules.
- **Project Workspace:** The active working tree of your project repository where your code, build scripts, and managed assets live.
- **Linked Repository:** The dedicated GitHub repository (public or private) that stores and versions your managed assets.
- **Managed Asset:** Any regular file enrolled with `spas add` whose contents and version history are synced to the linked repository.
- **Managed Checkout:** The isolated local clone maintained by SPAS in your user data directory to stage, commit, and merge assets safely before writing them to the workspace.

---

## Supported Boundary

SPAS is engineered with strict safety invariants:

- **Runtime Requirement:** Git 2.43.1 or newer.
- **Worktree Model:** One project worktree per Git common directory.
- **Branching:** One linked repository and fixed branch per project workspace.
- **File Modes:** Standard regular files (`100644` standard and `100755` executable).

> [!NOTE]
> SPAS deliberately rejects symlinks, directory junctions, Git submodules, Git LFS pointers, and directory ownership wildcards to ensure deterministic cross-platform synchronization. For complete details, see [Safety and Limitations](Safety-and-limitations).

---

## Community & Support

- 💬 **Ask a question or share feedback:** [GitHub Discussions](https://github.com/getspas/spas/discussions)
- 🐛 **Report a bug:** [Issue Tracker](https://github.com/getspas/spas/issues/new/choose)
- 🔒 **Report security vulnerabilities:** [GitHub Security Advisories](https://github.com/getspas/spas/security/advisories/new)
