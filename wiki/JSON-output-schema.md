# JSON Output Schema Reference

SPAS provides structured, machine-readable JSON output for all commands when invoked with the `--json` flag. This facilitates integration into continuous integration (CI) environments, automation pipelines, and custom developer tooling.

---

## 1. Schema Versioning & Stability Contract

All JSON payloads emitted by SPAS include a root-level `"schemaVersion"` field:

```json
{
  "schemaVersion": 1
}
```

- **Current Version:** `1`
- **Field Guarantee:** The `"schemaVersion"` field is guaranteed to be an integer present at the root of every JSON response on both stdout and stderr.
- **Breaking Changes:** Any breaking modification to top-level keys or semantic payload types will increment the `schemaVersion`.

---

## 2. Standard Error Envelope

When any command fails in `--json` mode, SPAS writes a structured error object to stderr and exits with the corresponding exit code:

```json
{
  "schemaVersion": 1,
  "ok": false,
  "error": {
    "code": "decision_required",
    "message": "local managed asset changes require approval to commit config/dev.json to the linked repository; provide --message"
  }
}
```

### Error Object Fields

| Field | Type | Description |
| :--- | :--- | :--- |
| `schemaVersion` | `integer` | JSON schema version (`1`). |
| `ok` | `boolean` | Always `false` on error. |
| `error.code` | `string` | Stable machine-readable error classification (e.g. `not_linked`, `decision_required`, `path_conflict`). |
| `error.message` | `string` | Human-readable explanation of the failure. |

A command that already wrote its structured payload (for example `spas doctor` reporting findings) exits nonzero without emitting a second envelope.

---

## 3. Command Payload Schemas

Conventions used below:

- Managed paths are workspace-relative, slash-separated strings.
- Keys marked **conditional** are present only under the stated condition.
- Array-typed fields serialize as empty arrays (`[]`) rather than `null` when empty.

### `spas link`

#### Link Success Payload

```json
{
  "schemaVersion": 1,
  "linked": true,
  "publicWorkspace": "/path/to/project",
  "privateRepository": "getspas/private-assets",
  "networkAccess": true
}
```

- `publicWorkspace` — absolute path of the linked workspace root.
- `networkAccess` — `true` if the visibility probe executed during this invocation; `false` when `--allow-public` bypassed the probe.

#### Link Dry-Run Payload (`--dry-run`)

```json
{
  "schemaVersion": 1,
  "action": "link",
  "publicWorkspace": "/path/to/project",
  "privateRepository": "getspas/private-assets",
  "transport": "ssh",
  "branch": "main",
  "networkAccess": false
}
```

- `branch` is the empty string when no `--branch` was provided (the branch is selected during first sync).
- Dry-run performs no network access; the visibility probe is skipped.

---

### `spas add`

#### Add Success Payload

```json
{
  "schemaVersion": 1,
  "added": [
    "config/dev.json",
    "testdata/mock-api.json"
  ],
  "canceledRemovals": [],
  "skippedTrackedPaths": [],
  "pendingSync": true
}
```

- `added` — paths newly enrolled by this command.
- `canceledRemovals` — pending removals cancelled because the path was re-added.
- `skippedTrackedPaths` — paths skipped because public Git tracks them.
- `pendingSync` — `true` while enrolled additions await `spas sync`.

#### Add Dry-Run Payload (`--dry-run`)

```json
{
  "schemaVersion": 1,
  "action": "add",
  "added": [
    "config/dev.json"
  ],
  "pendingAdds": [
    "config/dev.json"
  ],
  "canceledRemovals": [],
  "skippedTrackedPaths": [],
  "localExcludeWillChange": true,
  "mergeProtection": "enable"
}
```

- `pendingAdds` — the complete pending-addition set after the command.
- `localExcludeWillChange` — whether the SPAS block in `.git/info/exclude` would be rewritten.
- `mergeProtection` — the resolved merge-protection action (`"enable"` or `"skip"`).

---

### `spas remove`

#### Remove Success Payload

```json
{
  "schemaVersion": 1,
  "pendingRemovals": [
    "config/dev.json"
  ],
  "pendingSync": true
}
```

Conditional keys:

- `refreshedRemovals` (array, **conditional**) — already-pending removals whose recorded state this command refreshed; present only when non-empty.
- `unenrolled` (array, **conditional**) — never-synced pending additions that were unenrolled immediately; present only when non-empty. Each such path is also reported on stderr as no longer excluded from public Git.

#### Remove Dry-Run Payload (`--dry-run`)

```json
{
  "schemaVersion": 1,
  "action": "remove",
  "pendingAdds": [],
  "pendingRemovals": [
    "config/dev.json"
  ],
  "refreshedRemovals": [],
  "unenrolled": []
}
```

---

### `spas sync`

#### Sync Success Payload

```json
{
  "schemaVersion": 1,
  "synchronized": true,
  "privateCommitCreated": true,
  "managedFiles": 2,
  "skippedConflicts": [],
  "publicRemovalsStaged": []
}
```

Conditional keys:

- `deferredAdditions` (array, **conditional**) — pending additions whose workspace file is currently missing; enrollment is kept, nothing was staged. Present only when non-empty.
- `deferredRemovals` (array, **conditional**) — pending removals whose workspace file changed after removal was requested; nothing was deleted. Present only when non-empty.
- `recoveryCopies` (string, **conditional**) — absolute directory that received recovery copies during this run. Present only when copies were written.

#### Sync Continue Payload (`--continue`)

```json
{
  "schemaVersion": 1,
  "synchronized": true,
  "mergeContinued": true
}
```

- `recoveryCopies` (string, **conditional**) — as in the sync success payload.

#### Sync Abort Payloads (`--abort`)

One of three shapes, depending on the recorded recovery state:

```json
{
  "schemaVersion": 1,
  "mergeAborted": true,
  "gitNativeRecovery": true
}
```

A Git-native merge without SPAS recovery state was aborted.

```json
{
  "schemaVersion": 1,
  "mergeAborted": false,
  "mergeRecoveryCleared": true
}
```

SPAS merge recovery state was cleared; `mergeAborted` reports whether a Git merge was also aborted.

```json
{
  "schemaVersion": 1,
  "mergeAborted": true,
  "skippedPublicPaths": [],
  "deferredPaths": []
}
```

A full abort with workspace restoration. `recoveryCopies` (string, **conditional**) is added when copies were written.

#### Sync Dry-Run Payload (Uninitialized Clone)

Emitted when the private clone has not been initialized yet:

```json
{
  "schemaVersion": 1,
  "action": "sync",
  "networkRequired": true,
  "privateInitialized": false,
  "pendingAdds": [
    "config/dev.json"
  ],
  "pendingRemovals": []
}
```

#### Sync Dry-Run Payload (Initialized Clone)

```json
{
  "schemaVersion": 1,
  "action": "sync",
  "networkRequired": false,
  "privateInitialized": true,
  "privateHead": "e6a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d",
  "expectedPrivateHead": "e6a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d",
  "privateClean": true,
  "privateMergeInProgress": false,
  "pendingRecovery": false,
  "localChanges": [
    {
      "path": "config/dev.json",
      "status": "M"
    }
  ],
  "commitApprovalRequired": true,
  "commitMessageProvided": false,
  "conflicts": [],
  "pendingAdds": [],
  "pendingRemovals": [],
  "localExcludeWillChange": false,
  "mergeProtection": {
    "branch": "main",
    "enabled": true,
    "value": "--no-overwrite-ignore",
    "present": true
  }
}
```

- `localChanges` entries have the shape `{"path": "...", "status": "..."}` with status `"A"` (added), `"M"` (modified), or `"D"` (deleted).
- `conflicts` entries have the shape `{"kind": "...", "publicPath": "...", "privatePath": "..."}` where `kind` is one of `tracked_path`, `file_directory`, `case_insensitive_filesystem`, `cross_platform_filesystem`.
- In `mergeProtection`, the keys `value`, `present`, and `ambiguous` are omitted when empty or `false`.

---

### `spas status`

```json
{
  "schemaVersion": 1,
  "linked": true,
  "linkId": "lnk_3f9a2b4c17d0",
  "publicBranch": "main",
  "privateRepository": "getspas/private-assets",
  "privateBranch": "main",
  "privateInitialized": true,
  "pendingAdds": [],
  "pendingRemovals": [],
  "managedFiles": 2,
  "workspaceModified": [],
  "workspaceMissing": [],
  "privateCloneMissing": [],
  "expectedPrivateHead": "e6a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d",
  "actualPrivateHead": "e6a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d",
  "privateHeadMismatch": false,
  "privateAhead": 0,
  "privateBehind": 0,
  "pathConflicts": [],
  "exclusionFailures": [],
  "pendingRecovery": false,
  "privateClean": true,
  "mergeProtection": {
    "branch": "main",
    "enabled": true,
    "value": "--no-overwrite-ignore",
    "present": true
  }
}
```

- `linkId` — the link identity, `lnk_` followed by 12 hexadecimal characters.
- `publicWorkspace` and `privateClone` (strings, **conditional**) — absolute paths, present only with `--show-paths`.
- `publicBranch`, `privateBranch`, `expectedPrivateHead`, `actualPrivateHead`, `privateAhead`, `privateBehind`, and `privateClean` are omitted when unknown — for example before initialization, on a detached HEAD, or when remote-tracking information is unavailable.
- `mergeProtection` has the same shape as in the sync dry-run payload.

---

### `spas diff`

#### Diff Working Tree Payload

```json
{
  "schemaVersion": 1,
  "changedPaths": [
    "config/dev.json",
    "docs/team-notes.md"
  ]
}
```

- `changedPaths` is an empty array (`[]`) when no managed path differs.

#### Diff Staged Payload (`--staged`)

```json
{
  "schemaVersion": 1,
  "stagedPaths": [
    "config/dev.json"
  ]
}
```

- `stagedPaths` is an empty array (`[]`) when nothing is staged.

---

### `spas doctor`

```json
{
  "schemaVersion": 1,
  "healthy": true,
  "checks": [
    {
      "name": "git",
      "status": "ok",
      "message": "git version 2.43.1"
    },
    {
      "name": "data-dirs",
      "status": "ok",
      "message": "config and data directories are writable"
    },
    {
      "name": "lock",
      "status": "ok",
      "message": "advisory file locking is functional"
    },
    {
      "name": "worktrees",
      "status": "ok",
      "message": "single public worktree"
    },
    {
      "name": "local-exclusions",
      "status": "ok",
      "message": "2 private path(s) effectively excluded"
    }
  ],
  "warnings": 0,
  "errors": 0
}
```

- `status` is one of `ok`, `warning`, `error`; `healthy` is `false` when any check reports `error`.
- Check inventory: the environment checks `git`, `data-dirs`, and `lock` always run. Outside a Git repository, `workspace` is added with a warning status. Inside a Git repository, `worktrees` is added. In an unlinked workspace, `link-state` is reported with a warning status. In a linked workspace the link checks also run: `link-state`, `pending-recovery`, `case-policy`, `merge-protection`, `pull-mode`, `pending-ownership-transfers`, `path-ownership`, `local-exclusions`, and `exclude-block-integrity`, plus — depending on clone state — `interrupted-private-merge`, `remote-config`, `private-clone`, `expected-private-head`, `unsupported-private-file-types`, or `private-clone-initialization`.
- With `--json`, one or more error checks exit `1` after the payload is written; no separate error envelope follows.
- Warnings alone exit `0`, including the `workspace` warning outside a Git repository and the `link-state` warning in an unlinked workspace.

---

### `spas unlink`

```json
{
  "schemaVersion": 1,
  "unlinked": true,
  "keptFiles": true
}
```

Conditional keys:

- `workspaceFilesNowVisibleToPublicGit` (array, **conditional**) — managed paths whose exclusion rules were removed while the files stayed in the workspace; present only when files were kept and at least one path was affected.
- `privateCloneRemoved` (boolean, **conditional**) — present as `true` only when `--remove-private-clone` completed its cleanup.

---

### `spas version`

#### Version Success Payload (`--json`)

```json
{
  "schemaVersion": 1,
  "version": "1.0.0",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "date": "2026-08-30T00:00:00Z"
}
```

- `version` — semantic version string or `"dev"`.
- `commit` — Git commit SHA (with optional `-dirty` suffix) or `"unknown"`.
- `date` — build timestamp (RFC 3339) or `"unknown"`.
- Without `--json`, `spas version` prints plain text: `spas VERSION (commit COMMIT, built DATE)`.
