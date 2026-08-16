# Security Policy

The SPAS project takes the security of private assets and workspace integrity seriously. We appreciate the community's efforts to disclose vulnerabilities responsibly.

---

## Supported Versions

Only the latest released minor version of SPAS receives security updates:

| Version | Supported |
| :--- | :--- |
| `0.1.x` | :white_check_mark: |
| `< 0.1.0` | :x: |

---

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues, pull requests, or discussions.**

Instead, report vulnerabilities privately through GitHub's Security Advisories feature:

👉 **[Submit a Private Security Advisory](https://github.com/getspas/spas/security/advisories/new)**

### What to Include in Your Report

To help us investigate and remediate the issue quickly, please provide:

1. **Description:** A clear summary of the vulnerability and its potential security impact.
2. **Steps to Reproduce:** Exact reproduction steps, CLI commands, minimal repository configurations, or proof-of-concept scripts.
3. **Environment:**
   - SPAS version (`spas version`)
   - Git version (`git --version`)
   - Operating system and architecture (`uname -a` or OS version)
4. **Proposed Fix (Optional):** Any patches or mitigation strategies you have identified.

---

## Response & Disclosure Process

1. **Acknowledgment:** We will acknowledge receipt of your vulnerability report within **48 hours**.
2. **Assessment:** We will confirm the issue, determine its severity, and keep you informed of remediation progress.
3. **Fix & Release:** A fix will be prepared in a private fork and tested across all supported platforms (Linux, macOS, Windows on `amd64` and `arm64`).
4. **Coordinated Disclosure:** A patch release and corresponding GitHub Security Advisory (with CVE assignment if applicable) will be published simultaneously, crediting the reporter if desired.

---

## Security Boundaries & Out of Scope

SPAS manages workspace asset exclusion and synchronization with a dedicated GitHub repository. When assessing whether a finding is a vulnerability, please keep the following operational boundaries in mind:

- **Local Filesystem Permissions:** SPAS does not alter local OS file permissions or create an OS-level sandbox. Any process with read/write access to the project directory can read or modify workspace files.
- **Explicit Force Operations:** Commands that intentionally bypass safety checks (such as `git add -f` in the primary repository or `spas unlink --force`) are deliberate user overrides, not bypass vulnerabilities.
- **Upstream Git Vulnerabilities:** Vulnerabilities within Git itself should be reported to the [Git Security Team](https://git-scm.com/security).
