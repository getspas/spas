#!/usr/bin/env python3
"""Offline publisher regressions: real Git repositories, fake GitHub boundary."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


PUBLISHER = Path(__file__).with_name("publish-recipes.sh").resolve()
TAG = "v1.2.3"
COMMIT = "a" * 40
CHANNELS = {
    "homebrew": ("homebrew-tap", "Formula", "spas.rb"),
    "scoop": ("scoop-bucket", "bucket", "spas.json"),
    "winget": ("winget-pkgs", "manifests/g/GetSPAS/SPAS", "GetSPAS.SPAS.yaml"),
}
BOUNDARY = r'''
import json, os, pathlib, shutil, subprocess, sys
args = sys.argv[1:]
tool = pathlib.Path(sys.argv[0]).name
with open(os.environ["CALL_LOG"], "a") as log:
    log.write(json.dumps([tool, os.environ.get("GH_TOKEN"), args]) + "\n")
real_git = os.environ["REAL_GIT"]
if tool == "git":
    if args[0] == "clone":
        url = "https://github.com/getspas/" + os.environ["REPOSITORY"] + ".git"
        args[args.index(url)] = pathlib.Path(os.environ["FORK"]).as_uri()
    os.execv(real_git, [real_git, *args])
elif args[:2] == ["release", "view"]:
    print("true" if "isDraft,isPrerelease" in args else "v1.2.3")
elif args[0] == "api" and args[1].endswith("/commits/v1.2.3"):
    print("a" * 40)
elif args[:2] == ["run", "list"]:
    print("12345")
elif args[:2] == ["run", "download"]:
    if os.environ.get("MISSING_ARTIFACT"):
        sys.exit("artifact not found")
    shutil.copytree(os.environ["ARTIFACT"], args[args.index("--dir") + 1])
elif args[:2] == ["attestation", "verify"]:
    if not pathlib.Path(args[2]).is_file():
        sys.exit("recipe missing")
elif args[:2] == ["auth", "setup-git"]:
    pass
elif args[:2] == ["repo", "sync"]:
    subprocess.run([real_git, "-C", os.environ["UPSTREAM"], "push",
                    os.environ["FORK"], "master"], check=True)
elif args[0] == "api" and "repos/microsoft/winget-pkgs/pulls" in args:
    method = args[args.index("--method") + 1]
    if method == "POST" or os.environ.get("EXISTING_PR"):
        print("https://github.com/microsoft/winget-pkgs/pull/42")
else:
    sys.exit("unexpected GitHub command: " + repr(args))
'''


class PublisherTests(unittest.TestCase):
    def git(self, *args):
        return subprocess.run([self.real_git, *args], env=self.env, check=True,
                              text=True, capture_output=True).stdout.strip()

    def prepare(self, root, channel, **options):
        self.root = Path(root)
        self.real_git = shutil.which("git")
        self.env = {**os.environ, "GIT_CONFIG_GLOBAL": os.devnull,
                    "GIT_CONFIG_NOSYSTEM": "1", "GIT_ALLOW_PROTOCOL": "file",
                    "GH_TOKEN": "distribution-test-token",
                    "RELEASE_READ_TOKEN": "release-read-test-token",
                    "GITHUB_REPOSITORY": "getspas/spas", "TAG": TAG,
                    "CHANNEL": channel, "REAL_GIT": self.real_git}
        repository, directory, name = CHANNELS[channel]
        self.env.update(REPOSITORY=repository, DIRECTORY=directory, **options)
        for key in ("FORK", "UPSTREAM", "ARTIFACT", "CALL_LOG"):
            self.env[key] = str(self.root / key.lower())
        upstream = Path(self.env["UPSTREAM"])
        branch = "master" if channel == "winget" else "main"
        self.git("init", "-b", branch, str(upstream))
        self.git("-C", str(upstream), "config", "user.name", "Test")
        self.git("-C", str(upstream), "config", "user.email", "test@example.invalid")
        (upstream / "README").write_text("initial\n")
        self.git("-C", str(upstream), "add", ".")
        self.git("-C", str(upstream), "commit", "-m", "initial")
        self.git("clone", "--bare", str(upstream), self.env["FORK"])
        (upstream / "README").write_text("upstream advanced\n")
        self.git("-C", str(upstream), "commit", "-am", "upstream advance")
        self.upstream_head = self.git("-C", str(upstream), "rev-parse", "HEAD")
        target = directory + ("/1.2.3" if channel == "winget" else "")
        self.recipe_path = f"{target}/{name}"
        recipe = Path(self.env["ARTIFACT"]) / channel / self.recipe_path
        recipe.parent.mkdir(parents=True)
        recipe.write_text(f"generated {channel} recipe\n")
        self.recipe = recipe.read_text()
        binaries = self.root / "bin"
        binaries.mkdir()
        for name in ("gh", "git"):
            executable = binaries / name
            executable.write_text(f"#!{sys.executable}\n" + BOUNDARY)
            executable.chmod(0o755)
        self.env["PATH"] = f"{binaries}{os.pathsep}{os.environ['PATH']}"

    def publish(self):
        result = subprocess.run(["bash", str(PUBLISHER)], cwd=self.root,
                                env=self.env, text=True, capture_output=True)
        self.calls = [json.loads(line) for line in
                      Path(self.env["CALL_LOG"]).read_text().splitlines()]
        return result

    def test_channels_copy_attested_release_run_artifact(self):
        for channel in CHANNELS:
            with self.subTest(channel=channel), tempfile.TemporaryDirectory() as root:
                self.prepare(root, channel)
                result = self.publish()
                self.assertEqual(result.returncode, 0, result.stderr)
                ref = f"codex/spas-{TAG}" if channel == "winget" else "main"
                contents = self.git("--git-dir", self.env["FORK"], "show",
                                    f"{ref}:{self.recipe_path}")
                self.assertEqual(contents, self.recipe.strip())
                gh_calls = [(token, args) for tool, token, args in self.calls if tool == "gh"]
                for prefix in (["api"], ["run", "list"], ["run", "download"]):
                    token, args = next(call for call in gh_calls if call[1][:len(prefix)] == prefix)
                    self.assertEqual(token, self.env["RELEASE_READ_TOKEN"])
                run = next(args for _, args in gh_calls if args[:2] == ["run", "list"])
                for flag, value in (("--workflow", "release.yml"), ("--event", "push"),
                                    ("--branch", TAG), ("--commit", COMMIT), ("--status", "success")):
                    self.assertEqual(run[run.index(flag) + 1], value)
                download = next(args for _, args in gh_calls if args[:2] == ["run", "download"])
                self.assertEqual(download[2], "12345")
                self.assertEqual(download[download.index("--name") + 1], "package-manager-recipes")
                attest = next(args for _, args in gh_calls if args[:2] == ["attestation", "verify"])
                for flag, value in (("--source-ref", f"refs/tags/{TAG}"),
                                    ("--source-digest", COMMIT),
                                    ("--signer-workflow", "getspas/spas/.github/workflows/release.yml")):
                    self.assertEqual(attest[attest.index(flag) + 1], value)

    def test_winget_reuses_pr_and_starts_from_synchronized_master(self):
        with tempfile.TemporaryDirectory() as root:
            self.prepare(root, "winget", EXISTING_PR="1")
            result = self.publish()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("/pull/42", result.stdout)
            parent = self.git("--git-dir", self.env["FORK"], "rev-parse", f"codex/spas-{TAG}^")
            self.assertEqual(parent, self.upstream_head)
            requests = [args for tool, _, args in self.calls if tool == "gh" and args[0] == "api"]
            lookup = next(args for args in requests if "repos/microsoft/winget-pkgs/pulls" in args)
            self.assertIn(f"head=getspas:codex/spas-{TAG}", lookup)
            self.assertIn("GET", lookup)
            self.assertFalse(any("POST" in args for args in requests))

    def test_missing_artifact_fails_before_repository_write(self):
        with tempfile.TemporaryDirectory() as root:
            self.prepare(root, "winget", MISSING_ARTIFACT="1")
            before = self.git("--git-dir", self.env["FORK"], "show-ref")
            result = self.publish()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("artifact not found", result.stderr)
            self.assertEqual(self.git("--git-dir", self.env["FORK"], "show-ref"), before)
            self.assertFalse(any(tool == "git" or args[:2] == ["repo", "sync"]
                                 for tool, _, args in self.calls))


if __name__ == "__main__":
    unittest.main()
