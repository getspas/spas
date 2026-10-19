# SPAS Documentation

SPAS synchronizes your personal project files across your machines through a
separate GitHub repository. Files stay at their usual workspace paths and are
excluded from the project repository through local Git rules.

## Getting Started

[Install SPAS](Installation), then follow the [quick start](Quick-start) to link
your project, select files, and run your first sync.

These docs follow the source on `main`. If you use a released build, check
`spas version` and `spas COMMAND --help` for the options available in that build.

## Documentation Index

| Task | Guide |
| :--- | :--- |
| Choose an installation method, verify a download, or set up completion | [Installation](Installation) |
| Set up your first workspace and a second machine | [Quick start](Quick-start) |
| Look up commands, flags, terminal settings, or automation examples | [Command reference](Command-reference) |
| Parse command results, warnings, and errors | [JSON output schema](JSON-output-schema) |
| Check persisted-state and output compatibility | [Schema contracts](Schemas) |
| Diagnose failures, resolve conflicts, or resume a sync | [Troubleshooting](Troubleshooting) |
| Understand access controls, supported paths, and recovery limits | [Safety & limitations](Safety-and-limitations) |

## Core Concepts & Architecture

```text
Project workspace
├── Source files ────── git push / pull ── Project repository
└── Managed assets ──── spas sync ──────── Asset repository on GitHub
    (excluded from project Git)
```

| Term | Meaning |
| :--- | :--- |
| Project workspace | The local directory where you work with source code and managed assets |
| Project repository | The Git repository for your source code, whether public or private |
| Asset repository | The separate GitHub repository that stores your managed files and their history |
| Managed asset | A regular file selected for SPAS management |
| Managed checkout | SPAS's local clone in your user data directory, used to prepare commits and merges |
| Link | The association between a workspace and its asset repository and branch |

### What gets synchronized

- Run `spas add` to select files. Adding a directory selects its current supported
  files; future files need another `add`.
- Run `spas sync` whenever you want to exchange changes.
- A missing previously synced file is restored on sync. Use `spas remove` to
  request deletion from the asset repository and workspace.
- Every linked machine uses the same asset set. Leave machine-specific files
  unenrolled. Asset history uses the fixed branch selected for the link.

## Supported Boundary

SPAS runs on Windows, macOS, and Linux, on `amd64` and `arm64`, with Git 2.43.1+.
Each project must have exactly one worktree and uses one asset repository and
fixed branch. Regular text and binary files are supported; symlinks, junctions,
submodules, and Git LFS pointers are not.

Filename support depends on each machine's Git and filesystem. See
[supported files and paths](Safety-and-limitations#4-supported-file-types--path-constraints)
before sharing an asset set across operating systems.

> [!IMPORTANT]
> SPAS stores files as supplied. Repository access controls determine who can
> read their contents. Destructive project Git commands and concurrent edits
> during materialization can destroy unsynced changes.
> See [safety & limitations](Safety-and-limitations).

## Community & Support

[Ask a question](https://github.com/getspas/spas/discussions) · [Report a bug](https://github.com/getspas/spas/issues/new/choose) · [Report a security vulnerability](https://github.com/getspas/spas/security/advisories/new)
