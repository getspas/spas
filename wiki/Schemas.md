# SPAS Schema Contracts

SPAS has two independent versioned contracts. A schema version identifies the
format and meaning of a record, independently of the SPAS release version.

| Contract | Current version | Authority | Consumers |
| :--- | :--- | :--- | :--- |
| Link state | 4 | `internal/linkstate/store.go`: `SchemaVersion`, `State`, and validation | SPAS commands loading local state and resuming operations |
| CLI JSON output | 3 | `internal/jsonoutput/json.go`: `SchemaVersion` and encoding | Tools reading command stdout and stderr |

Checkout initialization, active merges, and pending materialization are nested
in link state and share its version. All command results, warnings, and errors
share the CLI JSON version. Neither contract has per-command or per-journal
versions. GoReleaser and Dependabot configuration versions belong to those tools.

## Version changes

Keep the two versions independent: an internal recovery change need not change
CLI consumers, and an output change need not invalidate local state.

- Revise the link-state version when persisted structure, meanings, or recovery
  invariants become incompatible with the existing reader or writer. Load and
  save accept only the current version. Older, future, missing, and zero
  versions are rejected. No migration or fallback loader is provided.
- Revise the CLI JSON version for breaking changes to keys, types, meanings, or
  stream behavior. Internal restructuring that preserves the output contract
  does not require a bump. Consumers should check `schemaVersion` before using
  a payload.
- Update this overview, the affected reference, examples, and contract tests
  with a version change. The normal Go test suite compares this table with both
  code constants and validates the documented CLI JSON examples and versions.

Recent changes:

| Contract | Change | Reason |
| :--- | :--- | :--- |
| Link state | 2 → 3 | Removed persisted public-repository approval |
| Link state | 3 → 4 | Preserved exact Git identities and recorded exact ownership replacement associations for recovery |
| CLI JSON output | 1 → 2 | Added immediate warning records to stderr's newline-delimited JSON stream |
| CLI JSON output | 2 → 3 | Replaced portability collision classifications with actual native filesystem alias classifications |

## Persisted link state

State is stored at `<configDir>/links/<linkId>.json`, using the platform config
directory. SPAS replaces it atomically with owner-only creation permissions.
The reader rejects unknown fields, trailing JSON, unsupported versions, and
invalid identity or recovery references. State is local implementation data;
editing it is not an enrollment or recovery interface.

The authoritative Go types and validators live together in `internal/linkstate`.
The following table maps all top-level fields to their responsibilities. An
optional group is omitted when its operation is inactive; empty slice fields in
persisted state may be `null` or `[]`.

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `schemaVersion` | Integer | Persisted link-state version |
| `linkId` | String | Identity derived from the workspace root and Git common directory |
| `public` | Object | `root` and `gitCommonDir` bind state to the project workspace |
| `private` | Object | Asset repository identity and checkout lifecycle, detailed below |
| `pendingAdds` | String array | Exact paths approved for enrollment but awaiting synchronization |
| `pendingRemovals` | Object array | Removal intent with `path`, `digest`, `existed`, `executable`, and `requestedAt` binding it to the observed workspace state |
| `managedPaths` | String array | Exact managed Git path identities |
| `exclude` | Object | `blockId` identifies the owned block; `existingContentApproved` records the setup decision |
| `mergeProtection` | Object | `managedBranches` maps branch names to `before`, `beforePresent`, and `after` Git configuration values |
| `activeMerge` | Optional object | Merge commits, prepared conflict files, snapshots, ownership decisions, and remaining enrollment intent |
| `pendingMaterialization` | Optional object | Approved result, publication phase, and workspace application contract |
| `lastSync` | Object | `publicHead`, `privateHead`, `remoteHead`, and `completedAt` describe the last completed synchronization |

### Checkout lifecycle

`private` contains `provider`, `repository`, `transport`, `remoteURL`, `branch`,
`localRepositoryPath`, `initialized`, `expectedHead`, `remoteEmpty`, and optional
`initialization`. Repository identity and the checkout location must match the
link. `expectedHead` binds subsequent operations to the approved checkout commit.

`initialization` contains `phase`, `requestedBranch`, `branch`, `head`, and
`remoteEmpty`. The persisted preparation phases are `preparing` and `prepared`.
Publishing the checkout sets `initialized` and clears `initialization`.

### Merge and materialization recovery

`activeMerge` binds Git's `preMergeHead` and `mergeHead` to link state.
`conflictFilesReady` records completed preparation of workspace conflict files.
`workspaceSnapshots`, `materializationPaths`, and `materializationSnapshots`
bind recovery and final destinations to the approved workspace observations.
`overrideStatuses` records each ownership path's index status.

Both recovery groups retain exact `skippedPaths`, `deferredPaths`,
`overridePaths`, `overrideReplacements`, `recoveryPaths`,
`remainingPendingAdds`, and `remainingPendingRemovals` as applicable.
`activeMerge` additionally records `conflictPaths`. Each ownership replacement
maps one exact project path to its exact asset path; recovery never reconstructs
that intent through case folding or Unicode normalization.

`pendingMaterialization` records `phase`, `resultPrivateHead`, optional
`mergeHead` and `stagedTree`, `previousPaths`, `finalPaths`, `excludedPaths`, and
`workspaceSnapshots`. Snapshots contain `path`, `digest`, `existed`, and
`executable`; override statuses contain `path` and `status`. Validators enforce
the required snapshot coverage and ownership associations for each phase.

| Phase | Recorded boundary |
| :--- | :--- |
| `merge-continuing` | Approved conflict resolution is being staged and verified |
| `merge-staged` | Approved resolution tree and merge parents are verified; commit creation or verification remains |
| `push-pending` | Approved result awaits successful push |
| `remote-advanced` | A concurrent remote update requires another fetch and merge |
| `pushed` | Publication succeeded; workspace materialization remains |

Recovery copies under `<dataDir>/recovery/<linkId>/<operationId>` contain asset
bytes and modes, rather than a separately versioned JSON journal. Lock-file JSON
contains diagnostic `pid` and `startedAt` metadata; kernel locks determine
ownership, and that metadata is not a state schema.

## CLI JSON output

`internal/jsonoutput` owns version stamping and encoding for results, warnings,
and errors. Application result types retain their command-specific fields.
Each output record is a newline-terminated JSON object with `schemaVersion`.
Results go to stdout. Warnings and errors go to stderr, which must be decoded
as a sequence of records; successful commands can still emit warnings.

See the [JSON output reference](JSON-output-schema) for field types, conditional
keys, command payloads, warning codes, and error envelopes, and the
[command reference](Command-reference) for command and exit behavior. Those
contracts are separate from the internal persisted-state fields above.
