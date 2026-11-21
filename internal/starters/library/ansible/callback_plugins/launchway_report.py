# Launchway's Ansible report callback.
#
# It is an aggregate (notification) callback: it does not change what Ansible
# prints. It writes one JSON file, named by LAUNCHWAY_ANSIBLE_REPORT, that
# Launchway reads after the step: per play, per task, per host, the outcome,
# timing and (in diff mode) the change, plus Ansible's own recap.
#
# Secrets: a task marked no_log has its message and diff dropped here, before
# anything is written; the server redacts again when it reads the file.
from __future__ import annotations

import difflib
import json
import os
import time

from ansible.plugins.callback import CallbackBase

DOCUMENTATION = """
  name: launchway_report
  type: aggregate
  short_description: write a structured run report for Launchway
  description:
    - Writes the file named by the LAUNCHWAY_ANSIBLE_REPORT environment variable.
  requirements:
    - enable in callbacks_enabled
"""

MAX_DIFF = 20000
MAX_MSG = 4000
HIDDEN = "(hidden: the task is marked no_log)"


def _clip(text, limit):
    text = text if isinstance(text, str) else json.dumps(text, default=str)
    return text if len(text) <= limit else text[:limit] + "\n(cut: %d more characters)" % (len(text) - limit)


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "aggregate"
    CALLBACK_NAME = "launchway_report"
    CALLBACK_NEEDS_ENABLED = True

    def __init__(self):
        super().__init__()
        self.path = os.environ.get("LAUNCHWAY_ANSIBLE_REPORT", "")
        self.plays = []
        self.stats = {}
        self.started = time.time()
        self.task_started = time.time()
        self.check = False
        self.diff = False
        self.ansible_version = ""

    # -- structure ---------------------------------------------------------
    def v2_playbook_on_start(self, playbook):
        from ansible import context
        from ansible import __version__

        self.ansible_version = __version__
        self.check = bool(context.CLIARGS.get("check"))
        self.diff = bool(context.CLIARGS.get("diff"))
        self.playbook = os.path.basename(getattr(playbook, "_file_name", ""))

    def v2_playbook_on_play_start(self, play):
        self.plays.append({"name": play.get_name().strip(), "tasks": []})

    def v2_playbook_on_task_start(self, task, is_conditional):
        self._task(task)
        self.task_started = time.time()

    def v2_playbook_on_handler_task_start(self, task):
        self._task(task, handler=True)
        self.task_started = time.time()

    def _task(self, task, handler=False):
        if not self.plays:
            self.plays.append({"name": "", "tasks": []})
        name = task.get_name().strip()
        self.plays[-1]["tasks"].append({"name": name, "action": task.action, "handler": handler,
                                        "no_log": bool(task.no_log), "results": {}})

    # -- results -----------------------------------------------------------
    def _record(self, result, status):
        if not self.plays or not self.plays[-1]["tasks"]:
            return
        task = self.plays[-1]["tasks"][-1]
        res = result._result or {}
        hide = bool(result._task.no_log) or bool(res.get("_ansible_no_log"))
        host = result._host.get_name()
        entry = {"status": status, "changed": bool(res.get("changed")),
                 "duration_ms": int((time.time() - self.task_started) * 1000)}
        msg = res.get("msg")
        if hide:
            if status in ("failed", "unreachable"):
                entry["msg"] = HIDDEN
        elif msg:
            entry["msg"] = _clip(msg, MAX_MSG)
        elif status == "failed" and res.get("stderr"):
            entry["msg"] = _clip(res["stderr"], MAX_MSG)
        if res.get("ignore_errors") or res.get("_ansible_ignore_errors"):
            entry["ignored"] = True
        if not hide:
            d = self._diff(res)
            if d:
                entry["diff"] = d
        task["results"][host] = entry

    def _diff(self, res):
        raw = res.get("diff")
        if not raw:
            return []
        out = []
        for item in raw if isinstance(raw, list) else [raw]:
            if not isinstance(item, dict):
                continue
            if "prepared" in item and item["prepared"]:
                text = item["prepared"]
            else:
                before = item.get("before", "")
                after = item.get("after", "")
                if not isinstance(before, str) or not isinstance(after, str):
                    before, after = json.dumps(before, indent=1, default=str, sort_keys=True), json.dumps(after, indent=1, default=str, sort_keys=True)
                text = "".join(difflib.unified_diff(before.splitlines(True), after.splitlines(True),
                                                    item.get("before_header", "before"), item.get("after_header", "after")))
            if text:
                out.append({"before": item.get("before_header", ""), "after": item.get("after_header", ""),
                            "text": _clip(text, MAX_DIFF)})
        return out

    def v2_runner_on_ok(self, result, **kwargs):
        self._record(result, "changed" if (result._result or {}).get("changed") else "ok")

    def v2_runner_on_failed(self, result, ignore_errors=False, **kwargs):
        self._record(result, "failed")
        if ignore_errors and self.plays and self.plays[-1]["tasks"]:
            self.plays[-1]["tasks"][-1]["results"][result._host.get_name()]["ignored"] = True

    def v2_runner_on_skipped(self, result, **kwargs):
        self._record(result, "skipped")

    def v2_runner_on_unreachable(self, result, **kwargs):
        self._record(result, "unreachable")

    # -- end ---------------------------------------------------------------
    def v2_playbook_on_stats(self, stats):
        for host in sorted(stats.processed.keys()):
            s = stats.summarize(host)
            self.stats[host] = {k: int(s.get(k, 0)) for k in ("ok", "changed", "failures", "unreachable", "skipped", "rescued", "ignored")}
        self._write()

    def _write(self):
        if not self.path:
            return
        doc = {"format": "launchway-ansible-report/1", "ansible_version": self.ansible_version,
               "playbook": getattr(self, "playbook", ""), "environment": os.environ.get("PARAM_ENVIRONMENT", ""), "check": self.check, "diff": self.diff,
               "duration_ms": int((time.time() - self.started) * 1000), "plays": self.plays, "stats": self.stats}
        tmp = self.path + ".tmp"
        with open(tmp, "w") as fh:
            json.dump(doc, fh, indent=1, sort_keys=True)
        os.replace(tmp, self.path)
