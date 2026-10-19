# Installation

SPAS runs as a single executable on Linux, macOS, and Windows. It requires **Git 2.43.1 or newer**.

Choose [a prebuilt binary](#1-download-prebuilt-binaries),
[a source build](#3-build-from-source), or a package manager:

| Platform | Installation options |
| :--- | :--- |
| macOS | [Homebrew](#homebrew-macos-and-linux) or a binary |
| Windows | [Scoop](#scoop-windows) or a binary; [WinGet status](#winget-windows) |
| Linux | [Standalone binary](#linux-standalone-binary), [DEB/RPM](#downloadable-linux-packages), or [Homebrew](#homebrew-macos-and-linux) if already installed |

These instructions track `main`. Release assets and package-manager versions
may lag behind it; check the selected release and `spas COMMAND --help` when
using a released build.

## 1. Download Prebuilt Binaries

Visit the [GitHub Releases](https://github.com/getspas/spas/releases/latest) page and download the appropriate binary for your operating system and architecture:

| Platform | Architecture | Binary File |
| :--- | :--- | :--- |
| **Linux** | x86_64 (`amd64`) | `spas_<VERSION>_linux_amd64` |
| **Linux** | ARM64 (`arm64`) | `spas_<VERSION>_linux_arm64` |
| **macOS** | Apple Silicon (`arm64`) | `spas_<VERSION>_darwin_arm64` |
| **macOS** | Intel (`amd64`) | `spas_<VERSION>_darwin_amd64` |
| **Windows** | x86_64 (`amd64`) | `spas_<VERSION>_windows_amd64.exe` |
| **Windows** | ARM64 (`arm64`) | `spas_<VERSION>_windows_arm64.exe` |

Replace `<VERSION>` with the version number without the leading `v`, for example
`1.0.0`. Use the filenames listed on the release page for your chosen version.

### Linux: standalone binary

Choose `linux_amd64` for x86_64 or `linux_arm64` for AArch64. Install Git 2.43.1
or newer and, for SSH authentication, an SSH client.

[Verify the downloaded binary](#2-verify-release-authenticity-and-integrity),
rename it to `spas`, then install it for your user from the download directory:

```sh
mkdir -p "$HOME/.local/bin"
install -m 0755 ./spas "$HOME/.local/bin/spas"
export PATH="$HOME/.local/bin:$PATH"
spas version
```

The `export` applies to the current shell. If `~/.local/bin` is not already on
your `PATH`, add that setting to your shell's configuration. To update, download
and verify the new binary, then repeat the install step. Set up
[shell completion](#5-shell-auto-completion) for your shell.

For package-managed installation and removal, use [DEB or RPM packages](#downloadable-linux-packages)
on compatible distributions, or [Homebrew](#homebrew-macos-and-linux) if you
already use it.

### Rename & Add to PATH

Before running the download, [verify its provenance and checksum](#2-verify-release-authenticity-and-integrity).
Then rename the downloaded file to `spas` (or `spas.exe` on Windows) and move it to a directory included in your system's `PATH` (such as `/usr/local/bin` on Unix systems or `C:\Program Files\spas` on Windows). On macOS and Linux, run `chmod +x spas` to make it executable.

Verify the installation:

```bash
spas version
git --version
```

---

## 2. Verify Release Authenticity and Integrity

The release workflow produces GitHub build provenance attestations and a
`checksums.txt` SHA-256 manifest. Use both checks: the attestation verifies
that the binary was produced by this repository's release workflow, while
the checksum detects corruption and provides a portable digest for other
tooling.

First, install the [GitHub CLI](https://cli.github.com/) and verify the
downloaded binary's provenance:

```bash
gh attestation verify <DOWNLOADED_BINARY> --repo getspas/spas
```

Replace the placeholder with the binary you downloaded, for example
`spas_1.0.0_linux_amd64`. Verification must succeed before you run the binary.

macOS binaries have GitHub build attestations but are not Apple Developer ID
signed or notarized. Downloaded binaries may prompt a macOS security warning;
use the source-built Homebrew formula or `go install` if you prefer a local build.

Download the release’s `checksums.txt` into the same folder as the binary.
The examples below use Linux/Windows AMD64 and macOS ARM64; substitute the
filename for your architecture, then compare the hashes:

### Linux

```bash
sha256sum spas_*_linux_amd64
grep spas_.*_linux_amd64 checksums.txt
```

### macOS

```bash
shasum -a 256 spas_*_darwin_arm64
grep spas_.*_darwin_arm64 checksums.txt
```

### Windows (PowerShell)

```powershell
Get-FileHash .\spas_*_windows_amd64.exe -Algorithm SHA256
Select-String -Path .\checksums.txt -Pattern "windows_amd64"
```

The output hash must match the value listed in `checksums.txt`. Verify the
attestation as well: it establishes build provenance, while the checksum checks
the downloaded bytes.

---

## 3. Build from Source

Building from source requires **Go 1.26.9 or newer**:

```bash
go install -trimpath github.com/getspas/spas@latest
```

Go installs the binary in `$GOBIN`, or in `$GOPATH/bin` when `GOBIN` is unset.
Add that directory to your `PATH`. With the default location on macOS or Linux:

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
The resulting binary runs directly on your machine.

### WinGet: Windows

The [WinGet submission](https://github.com/microsoft/winget-pkgs/pull/455022)
is pending as of October 19, 2026. Use [Scoop](#scoop-windows) or a
[standalone binary](#1-download-prebuilt-binaries) until the package is available
in the WinGet community source.

### Scoop: Windows

Add the [getspas bucket](https://github.com/getspas/scoop-bucket), then install SPAS:

```powershell
scoop bucket add getspas https://github.com/getspas/scoop-bucket
scoop install getspas/spas
scoop update spas
scoop uninstall spas
```

Scoop installs into your user environment by default and preserves your
PowerShell profile, SPAS configuration, and managed files.

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

SPAS generates completion scripts for Bash, Zsh, Fish, and PowerShell. The
examples below install them persistently; use the
[command reference](Command-reference#spas-completion) to load them for one session.
Create the destination directory if needed and use a location your shell loads
and your user can write to.

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
