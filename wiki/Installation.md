# Installation

SPAS is distributed as a single standalone executable for Linux, macOS, and Windows. It requires **Git 2.43.1 or newer**.

---

## 1. Download Prebuilt Binaries

Visit the [GitHub Releases](https://github.com/getspas/spas/releases/latest) page and download the appropriate archive for your operating system and architecture:

| Platform | Architecture | Archive File |
| :--- | :--- | :--- |
| **Linux** | x86_64 (`amd64`) | `spas_<VERSION>_linux_amd64.tar.gz` |
| **Linux** | ARM64 (`arm64`) | `spas_<VERSION>_linux_arm64.tar.gz` |
| **macOS** | Apple Silicon (`arm64`) | `spas_<VERSION>_darwin_arm64.tar.gz` |
| **macOS** | Intel (`amd64`) | `spas_<VERSION>_darwin_amd64.tar.gz` |
| **Windows** | x86_64 (`amd64`) | `spas_<VERSION>_windows_amd64.zip` |
| **Windows** | ARM64 (`arm64`) | `spas_<VERSION>_windows_arm64.zip` |

*(Replace `<VERSION>` with the release tag, e.g., `1.0.0`)*

### Extract & Add to PATH

Extract the downloaded archive and move the binary to a directory included in your system's `PATH` (such as `/usr/local/bin` on Unix systems or `C:\Program Files\spas` on Windows).

Verify the installation:

```bash
spas version
git --version
```

---

## 2. Verify Download Integrity

Every release includes an official `checksums.txt` file containing SHA-256 digests. Download `checksums.txt` into the same folder as the release archive and verify the integrity:

### Linux

```bash
sha256sum spas_*_linux_amd64.tar.gz
grep spas_.*_linux_amd64.tar.gz checksums.txt
```

### macOS

```bash
shasum -a 256 spas_*_darwin_arm64.tar.gz
grep spas_.*_darwin_arm64.tar.gz checksums.txt
```

### Windows (PowerShell)

```powershell
Get-FileHash .\spas_*_windows_amd64.zip -Algorithm SHA256
Select-String -Path .\checksums.txt -Pattern "windows_amd64"
```

The output hash must match the value listed in `checksums.txt`.

---

## 3. Verify Build Provenance (GitHub Attestation)

SPAS releases include cryptographic build-provenance attestations signed via GitHub Actions. If you have the [GitHub CLI (`gh`)](https://cli.github.com/) installed, you can cryptographically verify that the binary was built directly from our official repository:

```bash
gh attestation verify spas_<VERSION>_linux_amd64.tar.gz --repo getspas/spas
```

---

## 4. Build from Source

If you prefer building from source, ensure you have **Go 1.26.5 or newer** installed:

```bash
go install -trimpath github.com/getspas/spas@latest
```

The compiled binary will be installed to `$GOBIN` (or `$GOPATH/bin`). Make sure this directory is in your `PATH`:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
```

---

## 5. Shell Auto-Completion

SPAS includes built-in auto-completion support for Bash, Zsh, Fish, and PowerShell.

### Bash

```bash
spas completion bash > /usr/local/etc/bash_completion.d/spas
```

### Zsh

```zsh
spas completion zsh > "${fpath[1]}/_spas"
```

### Fish

```fish
spas completion fish > ~/.config/fish/completions/spas.fish
```

### PowerShell

```powershell
# Load completion in the current session
spas completion powershell | Out-String | Invoke-Expression

# Add to your PowerShell profile for persistence:
Add-Content $PROFILE "`nspas completion powershell | Out-String | Invoke-Expression"
```

---

## Next Steps

Now that SPAS is installed, head over to the [Quick Start Guide](Quick-start) to set up your first linked repository.
