#!/usr/bin/env python3
"""Check notice ordering, publication, and failure behavior with local modules."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

GENERATOR = Path(
    os.environ.get("DUPLEX_NOTICE_GENERATOR", "scripts/generate-notices.py")
).resolve()


class NoticesTest(unittest.TestCase):
    def test_publication_and_missing_license(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            licenses = root / "docs/licenses"
            licenses.mkdir(parents=True)
            for name in (
                "go-LICENSE.txt",
                "htmx-LICENSE.txt",
                "deno-LICENSE.txt",
                "go-font-LICENSE.txt",
            ):
                (licenses / name).write_text(name + " content\n")
            modules = []
            for name in ("a", "z"):
                module = root / name
                module.mkdir()
                (module / "LICENSE").write_text(name + " license\n")
                (module / "NOTICE").write_text(name + " notice\n")
                (module / "README").write_text("exclude this\n")
                modules.append(
                    {
                        "Path": "example.invalid/" + name,
                        "Version": "v1.0.0",
                        "Dir": str(module),
                    }
                )
            fixture = root / "modules.json"
            fixture.write_text(json.dumps(modules))
            fake_go = root / "go"
            fake_go.write_text(
                "#!" + sys.executable + "\n"
                "import json, pathlib, sys\n"
                "modules=json.loads(pathlib.Path(__file__).with_name('modules.json').read_text())\n"
                "if sys.argv[1:3] == ['list', '-deps']:\n"
                " print('duplex\\nexample.invalid/z\\nexample.invalid/a\\nexample.invalid/a')\n"
                "else:\n"
                " assert sys.argv[1:] == ['list', '-m', '-json', 'example.invalid/a', 'example.invalid/z']\n"
                " for module in modules: print(json.dumps(module))\n"
            )
            fake_go.chmod(0o700)
            environment = dict(os.environ, GO=str(fake_go))
            result = subprocess.run(
                [sys.executable, str(GENERATOR)],
                cwd=root,
                env=environment,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                result.stdout, "Generated notices for 2 linked Go modules and HTMX.\n"
            )
            expected = [
                "duplex linked dependency notices. Tool archives retain their bundled notices.\n"
            ]
            for name in ("a", "z"):
                expected.extend(
                    [
                        "\n===== example.invalid/" + name + " v1.0.0 =====\n",
                        "\nLICENSE\n" + name + " license\n",
                        "\nNOTICE\n" + name + " notice\n",
                    ]
                )
            for name in (
                "go-LICENSE.txt",
                "htmx-LICENSE.txt",
                "deno-LICENSE.txt",
                "go-font-LICENSE.txt",
            ):
                expected.append("\n===== " + name + " =====\n" + name + " content\n")
            self.assertEqual(
                (root / "THIRD_PARTY_NOTICES.txt").read_text(), "\n".join(expected)
            )
            self.assertEqual(
                (root / "docs/go-dependencies.json").read_text(),
                json.dumps(
                    [
                        {"module": module["Path"], "version": module["Version"]}
                        for module in modules
                    ],
                    indent=2,
                )
                + "\n",
            )
            for output in (
                root / "THIRD_PARTY_NOTICES.txt",
                root / "docs/go-dependencies.json",
            ):
                output.write_text("previous evidence\n")
            (root / "z/LICENSE").unlink()
            (root / "z/NOTICE").unlink()
            result = subprocess.run(
                [sys.executable, str(GENERATOR)],
                cwd=root,
                env=environment,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stderr, "Missing license: example.invalid/z\n")
            self.assertEqual(result.stdout, "")
            for output in (
                root / "THIRD_PARTY_NOTICES.txt",
                root / "docs/go-dependencies.json",
            ):
                self.assertEqual(output.read_text(), "previous evidence\n")


if __name__ == "__main__":
    unittest.main()
