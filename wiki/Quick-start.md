# Quick Start Guide

Set up SPAS for personal files you want to use across your own machines, such as
local configuration, test fixtures, or notes.

[Install](Installation) · [Command reference](Command-reference) · [Troubleshooting](Troubleshooting)

## Prerequisites

- [SPAS installed](Installation) on your `PATH` and Git 2.43.1 or newer.
- A project Git repository with exactly one worktree.
- Git credentials with read/write access to your asset repository: an SSH key or
  agent, or an HTTPS credential helper.
- A Git commit identity (`user.name` and `user.email`) configured in your project
  repository or global Git settings.

## Step 1: Create an Asset Repository on GitHub

[Create an empty GitHub repository](https://github.com/new), such as
`your-org/project-assets`. Leave the README, license, and `.gitignore` options
unchecked for this walkthrough. An existing SPAS asset repository can be reused
when [setting up another machine](#syncing-on-another-development-machine).

> [!IMPORTANT]
> Choose **Private** for sensitive files. Authorized readers, including GitHub,
> can read the files SPAS stores. You control repository visibility. See [access controls](Safety-and-limitations#1-repository-visibility--access-control).

## Step 2: Connect Your Project Workspace

At the root of your project repository, replace `your-org/project-assets` with
your asset repository:

```bash
# Connect using SSH and choose the asset branch
spas link your-org/project-assets --transport ssh --branch main
```

If you use HTTPS credentials, use this command instead:

```bash
spas link your-org/project-assets --transport https --branch main
```

Linking validates the workspace and saves the association locally. The first
sync prepares the managed checkout and resolves the branch; an empty repository
needs an explicit branch.

Link and sync warn when an anonymous probe detects public readability. A failed
probe does not prove that the repository is private. `spas link --dry-run` skips
network access and does not save the link.

Verify the association:

```bash
spas status
```

## Step 3: Select Assets to Manage

Replace these example paths with existing files in your workspace that are not
tracked by the project repository:

```bash
spas add config/dev.json testdata/mock-api.json docs/team-notes.md
```

### What Happens Behind the Scenes

SPAS records the selected paths and adds local rules to `.git/info/exclude`.
Your project `.gitignore` stays unchanged. If a setup decision is missing, SPAS
asks how to handle existing exclude rules and whether to enable Git merge
protection. Complete inputs run directly.

Passing a directory selects its current supported files recursively. Files
created there later need another `spas add`. Preview the selection with:

```bash
spas add testdata --dry-run
```

Leave files that need different contents on each machine unenrolled.

## Step 4: Preview Changes

```bash
spas sync --dry-run
spas diff --stat
```

The dry-run previews locally available state. It leaves files and commits
unchanged and skips network access. Before the first sync, it reports that
initialization needs network access, and `spas diff` lists selected files.
Content diffs become available after checkout initialization.

## Step 5: Run Your First Sync

```bash
spas sync
```

SPAS prepares its managed checkout, fetches the asset branch, and checks your
selected files. If local changes need a commit and you have not supplied a
message, it asks for one. It then merges remote changes, pushes without
force-pushing, verifies exclusions, and applies the result to your workspace.
These commits belong to the asset repository.

### Non-Interactive & CI Command

Supply decisions through flags for scripts:

```bash
spas sync \
  --message "Initial import of dev configuration and test fixtures" \
  --conflict abort \
  --existing-exclude preserve \
  --merge-protection enable \
  --non-interactive
```

Provision credentials before running unattended. See
[authentication and timeouts](Command-reference#unattended-authentication) for
SSH, HTTPS, and JSON examples.

## Step 6: Verify the Setup

```bash
spas status
git status
```

Your managed assets remain at their existing paths. Project Git should no longer
list them as untracked files. If exclusions fail, follow the
[exclusion troubleshooting steps](Troubleshooting#exclusion_validation_failed-exit-code-9).

## Syncing on Another Development Machine

Install SPAS and configure Git authentication on that machine, then:

1. Clone your project repository:

   ```bash
   git clone git@github.com:your-org/main-app.git
   cd main-app
   ```

2. Link it to the same asset repository and branch:

   ```bash
   spas link your-org/project-assets --transport ssh --branch main
   ```

3. Fetch and apply the assets:

   ```bash
   spas sync
   ```

Files already in the asset repository are enrolled during sync.
SPAS uses the same asset set across machines. Sync after editing assets on one
machine and before using them on another. If changes conflict, follow
[merge conflict recovery](Troubleshooting#private_merge_conflict-exit-code-6).

## Next Steps

| Task | Command or guide |
| :--- | :--- |
| Review local edits | `spas diff` or `spas diff --stat` |
| Restore a missing synced file | `spas sync` restores it |
| Delete an asset across linked machines | `spas remove PATH`, then `spas sync`; [removal details](Command-reference#spas-remove) |
| Stop managing this workspace | `spas unlink` keeps files by default; [unlink details](Command-reference#spas-unlink) |
| Diagnose a problem | `spas doctor` and [troubleshooting](Troubleshooting) |

Read [safety & limitations](Safety-and-limitations) before destructive Git
operations. Avoid editing managed files while SPAS is applying or deleting them.
