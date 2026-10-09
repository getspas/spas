#!/usr/bin/env python3
"""Render the source-built Homebrew formula for a GoReleaser release."""
import hashlib
import json
from pathlib import Path
import subprocess


def generate(root=Path(".")):
    metadata = json.loads((root / "dist/metadata.json").read_text())
    source = root / f"dist/spas_{metadata['version']}_source.tar.gz"
    with source.open("rb") as stream:
        source_sha = hashlib.file_digest(stream, "sha256").hexdigest()
    values = {
        "SOURCE_URL": f"https://github.com/getspas/spas/releases/download/{metadata['tag']}/{source.name}",
        "SOURCE_SHA256": source_sha,
        "COMMIT": metadata["commit"],
        "DATE": subprocess.check_output(
            ["git", "-C", str(root), "show", "-s", "--format=%cI", metadata["commit"]], text=True
        ).strip(),
    }
    formula = (root / "packaging/homebrew/spas.rb.in").read_text()
    for name, value in values.items():
        formula = formula.replace(f"@{name}@", value)
    output = root / "dist/homebrew/Formula/spas.rb"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(formula)


if __name__ == "__main__":
    generate()
