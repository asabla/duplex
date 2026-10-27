#!/usr/bin/env python3
"""Check download fallback and fail-closed installation without package access."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

INSTALLER = Path("scripts/install-ci-prerequisites.sh").resolve()


class PrerequisitesTest(unittest.TestCase):
    def test_download_and_install_boundaries(self):
        scenarios = (
            ("success", 0, ["update", "download", "install"]),
            ("primary-update", 0, ["update", "update", "download", "install"]),
            (
                "primary-download",
                0,
                ["update", "download", "update", "download", "install"],
            ),
            ("all-downloads", 124, ["update", "download", "update", "download"]),
            ("fallback-update", 100, ["update", "download", "update"]),
            ("install", 42, ["update", "download", "install"]),
            ("unsupported-release", 1, []),
        )
        for scenario, status, phases in scenarios:
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as d:
                root = Path(d)
                self.write_tools(root)
                environment = dict(
                    os.environ,
                    PATH=str(root) + os.pathsep + os.environ["PATH"],
                    FIXTURE_ROOT=str(root),
                    FIXTURE_SCENARIO=scenario,
                )
                result = subprocess.run(
                    ["sh", str(INSTALLER)],
                    env=environment,
                    capture_output=True,
                    text=True,
                )
                self.assertEqual(result.returncode, status, result.stderr)
                calls = root / "apt.jsonl"
                records = (
                    [json.loads(s) for s in calls.read_text().splitlines()]
                    if calls.exists()
                    else []
                )
                self.assertEqual([r["phase"] for r in records], phases)
                self.assertEqual(
                    len((root / "timeouts").read_text().splitlines())
                    if calls.exists()
                    else 0,
                    len([p for p in phases if p != "install"]),
                )
                for record in records:
                    if record["phase"] == "install":
                        self.assertIn("--no-download", record["args"])
                    if record["fallback"]:
                        self.assertIn(
                            "signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg",
                            record["sources"],
                        )
                        self.assertIn(
                            "https://archive.ubuntu.com/ubuntu noble", record["sources"]
                        )
                        self.assertNotIn("trusted=yes", record["sources"])

    def write_tools(self, root):
        tools = {
            "lsb_release": '#!/bin/sh\nif test "$FIXTURE_SCENARIO" = unsupported-release; then echo jammy; else echo noble; fi\n',
            "timeout": '#!/bin/sh\ntest "$1" = 120s || exit 99\nprintf \'%s\\n\' "$*" >>"$FIXTURE_ROOT/timeouts"\nshift\nexec "$@"\n',
            "sudo": "#!"
            + sys.executable
            + "\n"
            + """
import json, os, pathlib, sys
args=sys.argv[1:]
assert args.pop(0)=='apt-get'
root=pathlib.Path(os.environ['FIXTURE_ROOT'])
scenario=os.environ['FIXTURE_SCENARIO']
source=next((a.split('=',1)[1] for a in args if a.startswith('Dir::Etc::sourcelist=')),None)
phase='update' if 'update' in args else 'download' if '--download-only' in args else 'install'
record={'args':args,'phase':phase,'fallback':source is not None,'sources':pathlib.Path(source).read_text() if source else ''}
with (root/'apt.jsonl').open('a') as f:f.write(json.dumps(record)+'\\n')
if phase=='install' and scenario=='install':sys.exit(42)
if phase=='update' and (scenario=='primary-update' and not source or scenario=='fallback-update' and source):sys.exit(100)
if phase=='download' and (scenario=='all-downloads' or scenario in ['primary-download','fallback-update'] and not source):sys.exit(124)
""",
        }
        for name, body in tools.items():
            path = root / name
            path.write_text(body)
            path.chmod(0o700)


if __name__ == "__main__":
    unittest.main()
