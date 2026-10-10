"""Check recipe download URLs, checksums, and required release inputs."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError


def load_script(name):
    spec = importlib.util.spec_from_file_location(
        name, Path(__file__).with_name(name + ".py")
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


homebrew = load_script("generate-homebrew-formula")
scoop = load_script("generate-scoop-manifest")


class RecipeTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        (self.root / "dist").mkdir()
        (self.root / "dist/metadata.json").write_text(json.dumps({
            "version": "1.2.3", "tag": "v1.2.3", "commit": "a" * 40,
        }))
        templates = self.root / "packaging/homebrew"
        templates.mkdir(parents=True)
        template = Path(__file__).resolve().parents[2] / "packaging/homebrew/spas.rb.in"
        (templates / "spas.rb.in").write_text(template.read_text())

    def test_homebrew_uses_github_source_archive(self):
        archive = b"github-generated-source-archive"
        with patch.object(homebrew, "urlopen", return_value=io.BytesIO(archive)) as download:
            with patch.object(homebrew.subprocess, "check_output", return_value="2026-10-10T00:00:00Z\n"):
                homebrew.generate(self.root)
        download.assert_called_once_with(
            "https://github.com/getspas/spas/archive/refs/tags/v1.2.3.tar.gz", timeout=60
        )
        formula = (self.root / "dist/homebrew/Formula/spas.rb").read_text()
        self.assertIn('url "https://github.com/getspas/spas/archive/refs/tags/v1.2.3.tar.gz"', formula)
        self.assertIn(f'sha256 "{hashlib.sha256(archive).hexdigest()}"', formula)
        self.assertIn("Commit=" + "a" * 40, formula)
        self.assertIn("Date=2026-10-10T00:00:00Z", formula)
        self.assertNotIn("@SOURCE", formula)
        self.assertNotIn("releases/download", formula)

    def test_homebrew_download_failure_does_not_write_formula(self):
        error = HTTPError("https://github.com", 404, "Not Found", {}, None)
        with patch.object(homebrew, "urlopen", side_effect=error):
            with self.assertRaises(HTTPError):
                homebrew.generate(self.root)
        self.assertFalse((self.root / "dist/homebrew/Formula/spas.rb").exists())

    def test_scoop_uses_both_standalone_executables_with_release_checksums(self):
        hashes = {"amd64": "a" * 64, "arm64": "b" * 64}
        (self.root / "dist/checksums.txt").write_text("\n".join(
            f"{digest}  spas_1.2.3_windows_{arch}.exe" for arch, digest in hashes.items()
        ))
        scoop.generate(self.root)
        manifest = json.loads((self.root / "dist/scoop/bucket/spas.json").read_text())
        self.assertEqual(manifest["version"], "1.2.3")
        self.assertEqual(manifest["bin"], "spas.exe")
        self.assertEqual(manifest["depends"], ["git"])
        self.assertEqual(set(manifest["architecture"]), {"64bit", "arm64"})
        for architecture, goarch in (("64bit", "amd64"), ("arm64", "arm64")):
            resource = manifest["architecture"][architecture]
            self.assertEqual(resource["url"],
                f"https://github.com/getspas/spas/releases/download/v1.2.3/spas_1.2.3_windows_{goarch}.exe#/spas.exe")
            self.assertEqual(resource["hash"], hashes[goarch])

    def test_scoop_requires_checksums_for_both_architectures(self):
        for missing in ("amd64", "arm64"):
            with self.subTest(missing=missing):
                present = "arm64" if missing == "amd64" else "amd64"
                (self.root / "dist/checksums.txt").write_text(
                    f"{'a' * 64}  spas_1.2.3_windows_{present}.exe\n"
                )
                with self.assertRaises(KeyError):
                    scoop.generate(self.root)
                self.assertFalse((self.root / "dist/scoop/bucket/spas.json").exists())


if __name__ == "__main__":
    unittest.main()
