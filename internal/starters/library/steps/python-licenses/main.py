import os
import subprocess
import sys

import launchway_step as lw


def say(text):
    print("launchway: " + text)


def run(args, cwd, env):
    print("+ " + " ".join(args))
    return subprocess.run(args, cwd=cwd, env=env).returncode == 0


def main(step):
    ws = os.environ["LAUNCHWAY_WORKSPACE"]
    wd = step.string("working_directory") or "."
    if os.path.isabs(wd) or ".." in wd.split("/"):
        raise lw.Failed("working_directory must be a directory inside the repository, not " + wd)
    cwd = os.path.join(ws, wd)
    if not os.path.isdir(cwd):
        raise lw.Failed("working_directory %s does not exist in this repository" % wd)
    env = lw.environ()
    venv = os.path.join(cwd, ".venv")
    if os.path.isdir(os.path.join(venv, "bin")):  # the same as activating it
        env["VIRTUAL_ENV"] = venv
        env["PATH"] = os.path.join(venv, "bin") + os.pathsep + env.get("PATH", "")
    deny = ";".join(step.list("license_deny"))
    if not any(os.access(os.path.join(p, "pip-licenses"), os.X_OK) for p in env.get("PATH", "").split(os.pathsep)):
        run(["pip", "install", "--quiet", "pip-licenses"], cwd, env)
    if not run(["pip-licenses", "--format=plain", "--summary"], cwd, env):
        raise lw.Failed("pip-licenses failed")
    if deny:
        step.output("checked", True)
        if not run(["pip-licenses", "--fail-on=" + deny], cwd, env):
            step.summary("A dependency has a license on **license_deny**: " + deny)
            raise lw.Failed("a dependency has a license that is on license_deny (%s)" % deny)
    else:
        step.output("checked", False)
        say("license_deny is empty, so licenses are listed and nothing fails (set license_deny: [GPL-3.0] to block)")


if __name__ == "__main__":
    lw.run(main)
