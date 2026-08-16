# Contributing to SPAS

Thank you for your interest in improving SPAS! We welcome focused bug fixes, tests, documentation enhancements, and proposals that fit within the project's supported architecture.

If your proposal involves expanding supported repositories, file types, platform support, recovery mechanisms, or the security model, please start with a [GitHub Discussion](https://github.com/getspas/spas/discussions) before opening a pull request.

---

## 1. Project Architecture at a Glance

SPAS is a command-line tool written in Go that enables developers to manage environment-specific and private assets in a Git workspace without committing them to the primary Git repository.

### High-Level Design

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

1. **Local Exclusion Layer:** SPAS manages an isolated `# BEGIN SPAS` / `# END SPAS` block in `.git/info/exclude` to keep assets untracked in the main repo without editing `.gitignore`.
2. **Deterministic State Store (Schema 2):** Authoritative state binds the project workspace to a single linked GitHub repository and branch, enforcing strict head tracking (`Private.ExpectedHead`).
3. **9-Phase Synchronization Pipeline:** Acquires an advisory link lock, stages workspace snapshots, performs Git-native merge operations, pushes to GitHub, and materializes files via atomic renames (`/.spas-tmp/`).
4. **Fail-Safe Recovery:** Journaled checkouts and live merge tracking (`ActiveMerge`) ensure no state is guessed or corrupted during unexpected process interruptions.

---

## 2. External Dependency Requirements

Contributors need the following tools installed locally:

| Dependency | Minimum Version | Purpose |
| :--- | :--- | :--- |
| **Go** | `1.26.5` or newer | Language compiler and runtime toolchain |
| **Git** | `2.43.1` or newer | Git CLI used by tests and runtime operations |
| **govulncheck** | `v1.6.0` or newer | Static analysis for known Go security vulnerabilities |
| **markdownlint-cli2** | `0.23.0` or newer | Documentation formatting and linting (`npx -y markdownlint-cli2`) |

---

## 3. Development Setup

### Clone & Test

```bash
git clone https://github.com/getspas/spas.git
cd spas
go test ./...
```

> [!NOTE]
> Integration tests create temporary local Git repositories to test real Git transactions. They run entirely offline and do not require network access or GitHub credentials.

---

## 4. Making a Change

1. **Focus on one problem:** Keep each pull request centered on a single, well-defined improvement or fix.
2. **Add automated tests:** Include tests that exercise your changes at the lowest appropriate layer. Changes to synchronization, recovery transactions, path resolution, or Git safety checks require unit, integration, or regression tests.
3. **Protect private data:** Never commit credentials, private assets, recovery artifacts, compiled binaries, or unsanitized output from `spas diff`.

---

## 5. Quality Checks

Before submitting a pull request, run the local quality and test suite:

### Code Formatting & Dependencies

```bash
# Format Go source files
gofmt -w path/to/changed_file.go

# Verify module integrity
go mod tidy -diff
go mod verify
```

### Tests & Static Analysis

```bash
# Run unit and integration tests
go test ./...

# Run race detector
go test -race ./...

# Run static analysis
go vet ./...

# Run vulnerability check
go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
govulncheck ./...

# Lint documentation
npx -y markdownlint-cli2 "**/*.md"
```

CI runs full test suites natively across Linux, macOS, and Windows (`amd64` and `arm64`), followed by cross-compilation builds for all release targets with `CGO_ENABLED=0`.

---

## 6. Documentation Changes

- The root [`README.md`](README.md) serves as the project overview and quick-start landing page.
- Comprehensive user documentation lives in the [`wiki/`](wiki/Home.md) directory and is automatically mirrored to the GitHub Wiki on merge to `main`.
- **Edit wiki files directly in the repository source (`wiki/*.md`)**, not via the web Wiki interface, so all documentation undergoes regular peer review.
- When updating commands, flags, path constraints, recovery flows, or output formats, ensure documentation and copyable examples remain accurate and up to date.

---

## 7. Submitting a Pull Request

1. Push your changes to a feature branch on your fork.
2. Open a Pull Request against `main`.
3. Complete the pull request template, describing:
   - The user-visible outcome and motivation.
   - The tests added or updated.
   - The exact validation commands executed in your environment.
