"""Records the dependencies of the project that its package manager installed.

    python project_deps.py OUT [FILE ...]

Reads the active environment and writes NAME==VERSION lines to OUT. FILE are the project's own lock and requirements
files: a package they name is the project's, even if it is pip, setuptools or wheel. Without that, those three (and
pkg_resources) are the tooling of the image or of the environment and are left out, like the project itself and anything
that was installed from a directory, an archive or a repository (it is not on an index to look it up in).
Says in one line what was recorded and what was left out.
"""
import importlib.metadata as md
import json
import re
import sys

SEEDED = {"pip", "setuptools", "wheel", "pkg-resources", "distribute"}


def norm(name):
    return re.sub(r"[-_.]+", "-", name).lower()


def declared(files):
    names = set()
    for f in files:
        try:
            text = open(f, encoding="utf-8", errors="replace").read()
        except OSError:
            continue
        for m in re.finditer(r'^name\s*=\s*"([^"]+)"', text, re.M):  # uv.lock, poetry.lock, pdm.lock
            names.add(norm(m.group(1)))
        for m in re.finditer(r"^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*(?:[=<>!~;]|$)", text, re.M):  # requirements
            names.add(norm(m.group(1)))
        for m in re.finditer(r'^\s*"([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*[=<>!~;,"]', text, re.M):  # pyproject lists
            names.add(norm(m.group(1)))
    return names


def local(dist):
    try:
        raw = dist.read_text("direct_url.json")
    except Exception:
        return False
    return bool(raw)


def main():
    out, files = sys.argv[1], sys.argv[2:]
    named = declared(files)
    keep, seeded, other = {}, [], []
    for d in md.distributions():
        name = d.metadata["Name"]
        if not name:
            continue
        n = norm(name)
        if local(d):
            other.append(name)
        elif n in SEEDED and n not in named:
            seeded.append(f"{name} {d.version}")
        else:
            keep[n] = f"{name}=={d.version}"
    with open(out, "w", encoding="utf-8") as fh:
        fh.write("\n".join(sorted(keep.values(), key=str.lower)) + ("\n" if keep else ""))
    line = f"launchway: recorded {len(keep)} installed dependencies of the project in {out}"
    if seeded:
        line += f"; left out as tooling of the image or environment, not the project's: {', '.join(sorted(seeded))}"
    if other:
        line += f"; left out because they were installed from a directory or repository: {', '.join(sorted(other))}"
    print(line)


main()
