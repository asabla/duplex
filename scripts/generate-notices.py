#!/usr/bin/env python3
"""Regenerate notices for modules linked into the duplex binary."""

import json
import os
import subprocess
from pathlib import Path


def linked_modules(go):
    dependencies = subprocess.check_output(
        [
            go,
            "list",
            "-deps",
            "-f",
            "{{if .Module}}{{.Module.Path}}{{end}}",
            "./cmd/duplex",
        ],
        text=True,
    )
    paths = sorted(set(dependencies.split()) - {"duplex"})
    raw = subprocess.check_output([go, "list", "-m", "-json", *paths], text=True)
    modules = []
    decoder = json.JSONDecoder()
    while raw.strip():
        raw = raw.lstrip()
        module, end = decoder.raw_decode(raw)
        modules.append(module)
        raw = raw[end:]
    return modules


def module_notices(module):
    root = Path(module["Dir"])
    files = sorted(
        path
        for path in root.iterdir()
        if path.is_file()
        and (
            path.name.upper().startswith("LICENSE")
            or path.name.upper() in ("COPYING", "NOTICE")
        )
    )
    if not files:
        raise SystemExit("Missing license: " + module["Path"])
    heading = "\n===== " + module["Path"] + " " + module["Version"] + " =====\n"
    return [heading, *("\n" + path.name + "\n" + path.read_text() for path in files)]


def render_notices(modules):
    notices = [
        "duplex linked dependency notices. Tool archives retain their bundled notices.\n"
    ]
    for module in modules:
        notices.extend(module_notices(module))
    for name in (
        "go-LICENSE.txt",
        "htmx-LICENSE.txt",
        "deno-LICENSE.txt",
        "go-font-LICENSE.txt",
    ):
        notices.append(
            "\n===== " + name + " =====\n" + Path("docs/licenses", name).read_text()
        )
    return "\n".join(notices)


def main():
    modules = linked_modules(os.environ.get("GO", "go"))
    notices = render_notices(modules)
    Path("THIRD_PARTY_NOTICES.txt").write_text(notices)
    dependencies = [
        {"module": module["Path"], "version": module["Version"]} for module in modules
    ]
    Path("docs/go-dependencies.json").write_text(
        json.dumps(dependencies, indent=2) + "\n"
    )
    print("Generated notices for", len(modules), "linked Go modules and HTMX.")


if __name__ == "__main__":
    main()
