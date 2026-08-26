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

---

## 3. Command Payload Schemas

### `spas link`

#### Link Success Payload

```json
{
  "schemaVersion": 1,
  "linked": true,
  "publicWorkspace": "/path/to/project",
  "privateRepository": "getspas/private-assets",
  "privateBranch": "main"
}
```

#### Link Dry-Run Payload (`--dry-run`)

```json
{
  "schemaVersion": 1,
  "action": "link",
  "publicWorkspace": "/path/to/project",
  "privateRepository": "getspas/private-assets",
  "privateBranch": "main"
}
```

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
  "skippedTrackedPaths": []
}
```

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
  "skippedTrackedPaths": []
}
```

---

### `spas remove`

#### Remove Success Payload

```json
{
  "schemaVersion": 1,
  "pendingRemovals": [
    "config/dev.json"
  ],
  "pendingSync": true,
  "refreshedRemovals": [],
  "unenrolled": []
}
```

#### Remove Dry-Run Payload (`--dry-run`)

```json
{
  "schemaVersion": 1,
  "action": "remove",
  "pendingAdds": [],
  "pendingRemovals": [
    "config/dev.json"
  ]
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
  "publicRemovalsStaged": [],
  "deferredAdditions": [],
  "deferredRemovals": [],
  "recoveryCopies": "/path/to/data/recovery/link-id/op-timestamp"
}
```

#### Sync Dry-Run Payload (`--dry-run`)

```json
{
  "schemaVersion": 1,
  "action": "sync",
  "networkRequired": false,
  "privateInitialized": true,
  "managedFiles": 2,
  "pendingAdds": [],
  "pendingRemovals": [],
  "workspaceModified": [],
  "workspaceMissing": []
}
```

#### Sync Merge Abort Payload (`--abort`)

```json
{
  "schemaVersion": 1,
  "mergeAborted": true,
  "mergeRecoveryCleared": true
}
```

---

### `spas status`

```json
{
  "schemaVersion": 1,
  "linked": true,
  "linkId": "8f9a2b4c",
  "publicWorkspace": "/path/to/project",
  "publicBranch": "main",
  "privateRepository": "getspas/private-assets",
  "privateBranch": "main",
  "privateInitialized": true,
  "privateClone": "/path/to/checkouts/8f9a2b4c",
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
    "status": "enabled",
    "installed": true
  }
}
```

---

### `spas diff`

#### Diff Working Tree

```json
{
  "schemaVersion": 1,
  "changedPaths": [
    "config/dev.json",
    "docs/team-notes.md"
  ]
}
```

#### Diff Staged (`--staged`)

```json
{
  "schemaVersion": 1,
  "stagedPaths": [
    "config/dev.json"
  ]
}
```

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
      "message": "advisory lock acquired and released successfully"
    },
    {
      "name": "exclusions",
      "status": "ok",
      "message": "managed paths are effectively excluded from public Git"
    }
  ],
  "warnings": 0,
  "errors": 0
}
```

---

### `spas unlink`

```json
{
  "schemaVersion": 1,
  "unlinked": true,
  "publicWorkspace": "/path/to/project",
  "removedFiles": [],
  "failedRemovalFiles": []
}
```
