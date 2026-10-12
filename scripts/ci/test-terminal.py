"""Check native terminal output on macOS/Linux.

Run with: python3 scripts/ci/test-terminal.py
Binaries, fixtures, and transcripts remain in the printed temporary directory.
Windows console checks require native Windows terminal execution.
"""

import os
import sys

if os.name != "posix":
    raise SystemExit("This harness requires macOS/Linux pseudoterminals.")

import errno
import fcntl
import json
from pathlib import Path
import pty
import select
import shutil
import signal
import struct
import subprocess
import tempfile
import termios
import time


def run_terminal(args, env, width=80, native=False, interrupt=False):
    """Keep stderr on a PTY and capture result stdout separately for CLI runs."""
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, width, 0, 0))
    try:
        with tempfile.TemporaryFile() as result:
            proc = subprocess.Popen(
                args, stdin=subprocess.DEVNULL,
                stdout=slave if native else result, stderr=slave, env=env,
            )
            os.close(slave)
            slave = None
            output = bytearray()
            sent = False
            started = time.monotonic()
            try:
                while True:
                    if select.select([master], [], [], 0.05)[0]:
                        try:
                            data = os.read(master, 65536)
                        except OSError as error:
                            # Linux reports EIO when the PTY's last slave closes.
                            if error.errno != errno.EIO:
                                raise
                            break
                        if not data:
                            break
                        output.extend(data)
                    if interrupt and b"\x1b[2K" in output and not sent:
                        proc.send_signal(signal.SIGINT)
                        sent = True
                    if time.monotonic() - started > 30:
                        raise TimeoutError(f"Terminal test timed out: {args}")
                proc.wait(timeout=5)
            finally:
                if proc.poll() is None:
                    proc.kill()
                    proc.wait()
            result.seek(0)
            return proc.returncode, result.read(), bytes(output)
    finally:
        os.close(master)
        if slave is not None:
            os.close(slave)


def main():
    workspace_root = Path(__file__).resolve().parents[2]
    root = Path(tempfile.mkdtemp(prefix="spas-terminal-tests-"))
    print(f"Evidence: {root}", flush=True)
    real_git = shutil.which("git")
    if real_git is None:
        raise SystemExit("Git is required")
    binaries = {name: str(root / name) for name in ["presentation-test", "app-test", "spas"]}
    for name, package in [
        ("presentation-test", "./internal/presentation"),
        ("app-test", "./internal/app"), ("spas", "."),
    ]:
        args = ["go", "build"] if name == "spas" else ["go", "test", "-c"]
        subprocess.run(args + ["-o", binaries[name], package], cwd=workspace_root, check=True)

    env = os.environ.copy()
    env.update(
        TERM="xterm-256color", CI="", NO_COLOR="1", HOME=str(root),
        XDG_CONFIG_HOME=str(root), XDG_DATA_HOME=str(root),
        GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=str(root / "gitconfig"),
    )
    (root / "gitconfig").touch()
    for width in [80, 12]:
        for name, pattern in [
            ("presentation-test", "TestNativeTerminalProgress"),
            ("app-test", "TestNativeTerminalSyncProgress"),
        ]:
            code, _, diagnostics = run_terminal(
                [binaries[name], "-test.run=" + pattern, "-test.v"], env,
                width=width, native=True,
            )
            (root / f"{name}-{width}.log").write_bytes(diagnostics)
            assert code == 0, (code, diagnostics)
            assert b"\x1b[2K" in diagnostics and b"SKIP" not in diagnostics, diagnostics
            if name == "app-test" and width == 80:
                assert b"Inspecting workspace assets (1 file)" in diagnostics, diagnostics
                assert b"Fetching assets" in diagnostics and b"Pushing assets" in diagnostics, diagnostics
            if name == "presentation-test" and width == 80:
                assert b"Inspecting assets (3 files)" in diagnostics, diagnostics
                prompt = diagnostics.split(b"Message:", 1)[1].split(b"answer", 1)[0]
                assert b"\x1b" not in prompt, prompt
                assert b"Reading Git version" in diagnostics and b"git version" in diagnostics, diagnostics
                assert b"\r\x1b[2K--- PASS" in diagnostics, diagnostics
            print("PASS native", name, width, flush=True)

    proxy = root / "git-proxy"
    proxy.write_text(
        f"#!{sys.executable}\nimport os,sys,time\n"
        'if "--version" in sys.argv[1:]: time.sleep(0.7)\n'
        f'os.execv({real_git!r}, ["git"] + sys.argv[1:])\n'
    )
    proxy.chmod(0o700)
    workspace = str(root / "workspace")
    subprocess.run([real_git, "init", "-q", "-b", "main", workspace], env=env, check=True)
    base = [binaries["spas"], "doctor", "--repo", workspace, "--git", str(proxy), "--color=never"]
    for name, args, extra, animated in [
        ("auto", [], {}, True), ("never", ["--progress=never"], {}, False),
        ("non-interactive", ["--non-interactive"], {}, False),
        ("CI", [], {"CI": "1"}, False), ("dumb", [], {"TERM": "dumb"}, False),
        ("JSON", ["--json"], {}, False), ("timeout", ["--timeout=300ms"], {}, True),
    ]:
        code, out, diagnostics = run_terminal(base + args, env | extra)
        (root / f"{name}.log").write_bytes(diagnostics + b"\nSTDOUT\n" + out)
        assert (b"\x1b[2K" in diagnostics) == animated, (name, code, out, diagnostics)
        assert b"\x1b" not in out, (name, out)
        if animated:
            assert b"Checking workspace health" in diagnostics, (name, diagnostics)
        if name == "JSON":
            json.loads(out)
        assert code == (1 if name == "timeout" else 0), (name, code, out, diagnostics)
        print("PASS CLI", name, "exit", code, flush=True)

    redirected = subprocess.run(base, stdin=subprocess.DEVNULL, capture_output=True, env=env, timeout=30)
    assert redirected.returncode == 0, redirected.stderr
    assert b"\x1b" not in redirected.stdout + redirected.stderr
    print("PASS CLI redirected streams", flush=True)
    code, out, diagnostics = run_terminal(base, env, interrupt=True)
    (root / "SIGINT.log").write_bytes(diagnostics + b"\nSTDOUT\n" + out)
    assert code == 130, (code, out, diagnostics)
    assert b"\r\x1b[2Kerror:" in diagnostics, diagnostics
    print("PASS CLI SIGINT exit", code, flush=True)


if __name__ == "__main__":
    main()
