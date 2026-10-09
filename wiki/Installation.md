# Installation

SPAS runs as a single executable on Linux, macOS, and Windows. It requires **Git 2.43.1 or newer**.

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

## 2. Verify Release Authenticity and Integrity

SPAS releases provide GitHub build provenance attestations and a
`checksums.txt` SHA-256 manifest. Use both checks: the attestation verifies
that the archive was produced by this repository's release workflow, while
the checksum detects corruption and provides a portable digest for other
tooling.

First, install the [GitHub CLI](https://cli.github.com/) and verify the
downloaded archive's provenance:

```bash
gh attestation verify spas_<VERSION>_<OS>_<ARCH>.<EXT> --repo getspas/spas
```

Replace the placeholder with the archive you downloaded, for example
`spas_1.0.0_linux_amd64.tar.gz`. Verification must succeed before you extract
or run the binary.

macOS archives have GitHub build attestations but are not Apple Developer ID
signed or notarized. Downloaded binaries may prompt a macOS security warning;
use the source-built Homebrew formula or `go install` if you prefer a local build.

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

The output hash must match the value listed in `checksums.txt`. A checksum
match is not a substitute for the provenance check above because the archive
and checksum file are distributed through the same release channel.

---

## 3. Build from Source

Building from source requires **Go 1.26.8 or newer**:

```bash
go install -trimpath github.com/getspas/spas@latest
```

The compiled binary will be installed to `$GOBIN` (or `$GOPATH/bin`). Make sure this directory is in your `PATH`:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
```

---

## 4. Package Managers

All packages require Git 2.43.1 or newer. Go is only needed when building from
source. DEB and RPM packages install
shell completions and documentation. Package removal preserves SPAS application data.

### Homebrew: macOS and Linux

The [Homebrew tap](https://github.com/getspas/homebrew-tap) uses a formula that
builds SPAS from source:

```sh
brew install getspas/tap/spas
brew upgrade spas
brew uninstall spas
```

Homebrew installs Go as a build dependency and Git as a runtime dependency.
SPAS requires Git 2.43.1 or newer.
Go is not needed to run the installed binary. This route does not require Apple
Developer ID signing or notarization.

### WinGet: Windows

Install SPAS in your user account:

```powershell
winget install --exact --id GetSPAS.SPAS --source winget --scope user
winget upgrade --exact --id GetSPAS.SPAS --source winget --scope user
winget uninstall --exact --id GetSPAS.SPAS --scope user
```

### Scoop: Windows

Add the [getspas bucket](https://github.com/getspas/scoop-bucket), then install SPAS:

```powershell
scoop bucket add getspas https://github.com/getspas/scoop-bucket
scoop install getspas/spas
scoop update spas
scoop uninstall spas
```

Scoop installs into your user environment by default. Installation does not
modify your PowerShell profile or move SPAS configuration or private assets.

### Downloadable Linux Packages

Download the DEB or RPM for your architecture from
[GitHub Releases](https://github.com/getspas/spas/releases). Verify its provenance
and SHA-256 digest as described above. The packages depend on Git 2.43.1 or newer,
CA certificates, and an SSH client. For Debian or Ubuntu:

```sh
sudo apt install ./spas_<VERSION>_amd64.deb
```

For Fedora or another compatible RPM system, verify the release's public signing
key before importing it. Download `spas-rpm-signing.asc` from the same release,
check its attestation and checksum, and display its fingerprint:

```sh
curl -fLO "https://github.com/getspas/spas/releases/download/v<VERSION>/spas-rpm-signing.asc"
gh attestation verify spas-rpm-signing.asc --repo getspas/spas
sha256sum spas-rpm-signing.asc
grep spas-rpm-signing.asc checksums.txt
gpg --show-keys --with-fingerprint spas-rpm-signing.asc
```

Compare the complete primary-key fingerprint with the fingerprint in a trusted
SPAS release announcement. Import the key only after it matches, then require
signature verification when installing the local RPM:

```sh
sudo rpm --import spas-rpm-signing.asc
sudo dnf --setopt=localpkg_gpgcheck=1 install ./spas-<VERSION>-1.x86_64.rpm
```

ARM64 filenames end in `_arm64.deb` and `.aarch64.rpm`. To upgrade, download
and verify the newer package and run the same installation command with its
filename. An older distribution's Git package may not meet SPAS's minimum;
update Git through a supported source instead of bypassing the dependency.

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

Follow the [Quick Start Guide](Quick-start) to set up your first linked repository.
