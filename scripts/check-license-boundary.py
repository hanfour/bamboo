#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Fail when Apache-2.0 Go code depends on AGPL Go code (ADR 0011 Phase B).

A package's license comes from the SPDX headers of its non-test .go
files: AGPL if any of them is AGPL, Apache if all are Apache. For every
Apache package, the script walks its transitive in-repo dependencies
(`go list -deps`, test files excluded because they do not ship with
the package) and reports any AGPL package it reaches. A package mixing
both licenses is reported too.

Generated protobuf code under proto/gen carries no header and belongs
to the Apache-2.0 proto module, so it counts as Apache.

INTERIM lists the one dependency ADR 0011 Phase A accepts: the CLI links
clients/core while core is still AGPL, so the shipped `bamboo` binary is
under AGPL terms (LICENSING.md says so). Those edges print as notices.
Anything else fails, so the AGPL surface cannot grow unnoticed.

Exit 0 = clean. Exit 1 = at least one violation.
"""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PREFIX = "github.com/hanfour/bamboo/"

# (dependent package prefix, AGPL dependency prefix, reason)
INTERIM = [
    (
        PREFIX + "clients/cli/",
        PREFIX + "clients/core/",
        "ADR 0011 Phase A: core is AGPL until the clean-room rewrite",
    ),
]


def interim_reason(pkg: str, dep: str) -> str | None:
    for src, dst, reason in INTERIM:
        if pkg.startswith(src) and dep.startswith(dst):
            return reason
    return None


def modules() -> list[Path]:
    out = []
    for line in (ROOT / "go.work").read_text().splitlines():
        line = line.strip()
        if line.startswith("./"):
            out.append(ROOT / line)
    return out


def go_list(module: Path) -> list[dict]:
    res = subprocess.run(
        ["go", "list", "-json=ImportPath,Dir,GoFiles,CgoFiles,Deps", "./..."],
        cwd=module,
        check=True,
        capture_output=True,
        text=True,
    )
    dec = json.JSONDecoder()
    text, i, pkgs = res.stdout, 0, []
    while i < len(text):
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            break
        obj, i = dec.raw_decode(text, i)
        pkgs.append(obj)
    return pkgs


def file_license(path: Path) -> str | None:
    with path.open(encoding="utf-8") as f:
        for _ in range(5):
            line = f.readline()
            if "SPDX-License-Identifier:" in line:
                return line.split("SPDX-License-Identifier:", 1)[1].strip()
    return None


def package_license(pkg: dict) -> tuple[str, set[str]]:
    rel = Path(pkg["Dir"]).relative_to(ROOT).as_posix()
    if rel.startswith("proto/gen/"):
        return "apache", {"Apache-2.0"}
    found = set()
    for name in pkg.get("GoFiles", []) + pkg.get("CgoFiles", []):
        lic = file_license(Path(pkg["Dir"]) / name)
        if lic:
            found.add(lic)
    if any(lic.startswith("AGPL") for lic in found):
        return "agpl", found
    return "apache", found


def main() -> int:
    pkgs: dict[str, dict] = {}
    for mod in modules():
        for pkg in go_list(mod):
            pkgs[pkg["ImportPath"]] = pkg

    lic = {path: package_license(p) for path, p in pkgs.items()}
    problems = []
    notices = []
    for path, (kind, found) in sorted(lic.items()):
        if kind == "agpl" and any(x.startswith("Apache") for x in found):
            problems.append(f"{path}: mixes Apache-2.0 and AGPL files in one package")
        if kind != "apache":
            continue
        for dep in pkgs[path].get("Deps", []):
            if dep.startswith(PREFIX) and dep in lic and lic[dep][0] == "agpl":
                reason = interim_reason(path, dep)
                if reason:
                    notices.append(f"{path} -> {dep}")
                else:
                    problems.append(f"{path} (Apache-2.0) depends on {dep} (AGPL)")

    if notices:
        print(f"notice: {len(notices)} interim CLI -> core edge(s) allowed by ADR 0011 Phase A")
    for p in problems:
        print(p)
    if problems:
        print(f"FAIL: {len(problems)} license boundary violation(s); see ADR 0011.")
        return 1
    apache = sum(1 for k, _ in lic.values() if k == "apache")
    print(f"OK: {apache} Apache-2.0 package(s) depend on no AGPL package.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
