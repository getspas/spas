# Quick Start Guide

This guide walks you through connecting a project workspace to a separate GitHub repository and completing your first asset synchronization with SPAS.

---

## Prerequisites

Before getting started, make sure you have:

- **SPAS installed** on your system `PATH` (see [Installation](Installation)).
- **Git 2.43.1 or newer** installed and accessible in your shell.
- A standard **Git repository** with a single active worktree.
- Configured **Git credentials** (SSH agent or HTTPS credential helper) with read/write access to GitHub.

---

## Step 1: Create an Asset Repository on GitHub

1. Go to [GitHub](https://github.com/new) and create a new repository for your managed files (e.g., `your-org/project-assets`).
2. Choose **Private** (recommended for credentials, internal notes, or proprietary assets) or **Public**.
3. **Important:** Leave the repository completely empty—do *not* initialize it with a README, license, or `.gitignore`.

> [!NOTE]
> SPAS respects your Git credentials and does not modify repository visibility settings.

---

## Step 2: Connect Your Project Workspace

Open a terminal at the root of your local project repository and run `spas link`:

```bash
# Connect using SSH transport (default branch: main)
spas link your-org/project-assets --transport ssh --branch main
```

If your Git environment uses HTTPS authentication:

```bash
spas link your-org/project-assets --transport https --branch main
```

`spas link` operates entirely offline. It validates the local Git workspace structure and saves the link state locally without making network calls or modifying workspace files.

Verify the link status:

```bash
spas status
```

---

## Step 3: Select Assets to Manage

Add the existing local files you want SPAS to manage:

```bash
spas add config/dev.json testdata/mock-api.json docs/team-notes.md
```

### What Happens Behind the Scenes

- SPAS records these files in its local state.
- SPAS creates an owned block inside your workspace's `.git/info/exclude`.
- Your project repository's `.gitignore` remains completely untouched, keeping your public Git configuration clean.

> [!TIP]
> Passing a directory (e.g., `spas add testdata`) enrolls all regular files currently inside that folder. To preview additions without saving, add `--dry-run`:
>
> ```bash
> spas add testdata --dry-run
> ```

---

## Step 4: Preview Changes

Before committing or pushing anything, inspect what SPAS will do:

```bash
# Run a dry-run sync simulation
spas sync --dry-run

# Inspect file differences
spas diff --stat
```

The dry-run takes a read-only snapshot of the workspace. It does not create commits, modify exclusions, fetch remote data, or write files.

---

## Step 5: Run Your First Sync

Run the interactive synchronization command:

```bash
spas sync
```

SPAS will prompt you for a commit message for the asset repository. During the sync, SPAS:

1. Prepares an isolated managed checkout in your local application data directory.
2. Snapshots your approved workspace assets.
3. Commits the assets and pushes them securely to GitHub (without force-pushing).
4. Verifies the files written into the project workspace.

### Non-Interactive & CI Command

For automated scripts or CI workflows, provide all policies explicitly:

```bash
spas sync \
  --message "Initial import of dev configuration and test fixtures" \
  --conflict abort \
  --existing-exclude preserve \
  --merge-protection enable \
  --non-interactive
```

---

## Step 6: Verify the Setup

Check the status of both SPAS and your project Git repository:

```bash
# Check SPAS managed state
spas status

# Verify the main Git repository ignores managed assets
git status
```

Your managed assets remain right in your workspace where tests and build scripts can access them, but `git status` won't show them as untracked files!

---

## Syncing on Another Development Machine

When setting up your project on another machine or collaborating with teammates:

1. Clone your main project repository:

   ```bash
   git clone git@github.com:your-org/main-app.git
   cd main-app
   ```

2. Link SPAS to the existing asset repository (SPAS will automatically detect the default branch):

   ```bash
   spas link your-org/project-assets --transport ssh
   ```

3. Run `spas sync` to pull down the managed assets directly into your workspace:

   ```bash
   spas sync
   ```

---

## Next Steps

- Explore all flags and automation recipes in the [Command Reference](Command-reference).
- Learn how SPAS handles safety invariants in [Safety & Limitations](Safety-and-limitations).
- Run `spas doctor` anytime you want to perform a comprehensive health check on your environment.
