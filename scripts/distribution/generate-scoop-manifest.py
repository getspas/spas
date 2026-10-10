#!/usr/bin/env python3
"""Render a Scoop manifest for the standalone Windows release binaries."""
import json
from pathlib import Path


def generate(root=Path(".")):
    metadata = json.loads((root / "dist/metadata.json").read_text())
    checksums = {}
    for line in (root / "dist/checksums.txt").read_text().splitlines():
        digest, name = line.split(maxsplit=1)
        checksums[name] = digest
    manifest = {
        "version": metadata["version"],
        "description": "Secure private asset sync for Git workspaces",
        "homepage": "https://github.com/getspas/spas",
        "license": "AGPL-3.0-only",
        "depends": ["git"],
        "bin": "spas.exe",
        "architecture": {},
    }
    for architecture, goarch in (("64bit", "amd64"), ("arm64", "arm64")):
        name = f"spas_{metadata['version']}_windows_{goarch}.exe"
        manifest["architecture"][architecture] = {
            # Scoop saves the download under this name and creates the spas shim.
            "url": f"https://github.com/getspas/spas/releases/download/{metadata['tag']}/{name}#/spas.exe",
            "hash": checksums[name],
        }
    output = root / "dist/scoop/bucket/spas.json"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    generate()
