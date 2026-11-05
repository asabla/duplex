#!/usr/bin/env python3
"""Keep formatter diagnostics visible without hiding tool failures."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

CHECK = Path(os.environ.get("DUPLEX_CHECK_SCRIPT", "scripts/check.sh")).resolve()
FINGERPRINT = Path("scripts/coverage-source.sh").resolve()


class FormatterDiagnosticsTest(unittest.TestCase):
    def test_diff_and_tool_error_are_reported(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binaries = root / "bin"
            binaries.mkdir()
            mocks = {
                "golangci-lint": """case "$1" in
version) echo 'golangci-lint has version 2.15.0' ;;
fmt) printf '%s' "$FIXTURE_FORMAT_DIFF"; exit "$FIXTURE_FORMAT_STATUS" ;;
*) echo 'Unexpected lint execution' >&2; exit 99 ;;
esac
""",
                "gitleaks": "echo 8.30.1\n",
                "govulncheck": "echo v1.9.0\n",
            }
            for name, body in mocks.items():
                tool = binaries / name
                tool.write_text("#!/bin/sh\n" + body)
                tool.chmod(0o700)
            for difference, status, message, expected_status in (
                ("fixture diff\n", "1", "Formatting differs.", 1),
                ("", "2", "Formatting check failed with status 2.", 2),
            ):
                with self.subTest(status=status):
                    environment = dict(
                        os.environ,
                        PATH=str(binaries) + os.pathsep + os.environ["PATH"],
                        FIXTURE_FORMAT_DIFF=difference,
                        FIXTURE_FORMAT_STATUS=status,
                    )
                    result = subprocess.run(
                        ["sh", str(CHECK)],
                        cwd=root,
                        env=environment,
                        text=True,
                        capture_output=True,
                    )
                    self.assertEqual(result.returncode, expected_status)
                    self.assertIn(message, result.stderr)
                    self.assertEqual(result.stdout, difference)
                    self.assertNotIn("Unexpected lint execution", result.stderr)


class SourceFingerprintTest(unittest.TestCase):
    def test_embedded_files_and_fixtures_change_the_fingerprint(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".cache").mkdir()
            (root / "cmd").mkdir()
            (root / "internal").mkdir()
            (root / "go.mod").write_text("module fixture\n")
            (root / "go.sum").write_text("")
            inputs = (
                root / "cmd/main.go",
                root / "internal/page.html",
                root / "internal/layout.json",
            )
            for path in inputs:
                path.write_text("original\n")

            def fingerprint():
                return subprocess.check_output(
                    ["sh", str(FINGERPRINT)], cwd=root, text=True
                ).strip()

            original = fingerprint()
            for path in inputs:
                with self.subTest(path=path.name):
                    path.write_text("changed\n")
                    self.assertNotEqual(fingerprint(), original)
                    path.write_text("original\n")
                    self.assertEqual(fingerprint(), original)

            (root / ".cache/unrelated-profile").write_text("private measurement\n")
            self.assertEqual(fingerprint(), original)


if __name__ == "__main__":
    unittest.main()
