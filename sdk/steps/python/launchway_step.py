"""Python kit for Launchway custom steps: a small wrapper of the step contract
(docs/reference/step-contract.md). No dependencies; copy this file into a step
(`launchway step new --lang python` does).

    import launchway_step as lw

    def main(step):
        who = step.string("who")
        step.output("greeting", "Hello, " + who)
        step.summary("Greeted **" + who + "**")

    lw.run(main)

Raise `Failed("message")` to fail (exit 1), `Allowed("message")` for "failed
but allowed" (64), `NeedsPerson("question")` for "needs a person" (65).
"""
import json
import os
import sys

CONTRACT = 1


class Failed(Exception):
    code = 1


class Allowed(Exception):
    code = 64


class NeedsPerson(Exception):
    code = 65


class Step:
    def __init__(self):
        path = os.environ.get("LAUNCHWAY_STEP_INPUTS")
        if not path:
            raise Failed("LAUNCHWAY_STEP_INPUTS is not set: run this step with `launchway step test`, or in a pipeline")
        with open(path, encoding="utf-8") as f:
            doc = json.load(f)
        if doc.get("contract", 1) > CONTRACT:
            raise Failed("the Station speaks contract %d; this kit speaks %d: update the kit" % (doc["contract"], CONTRACT))
        self._inputs = doc.get("inputs", {})
        self._outputs = {}
        self._out_path = os.environ.get("LAUNCHWAY_STEP_OUTPUTS")
        results = os.environ.get("LAUNCHWAY_STEP_RESULTS")
        self._results = open(results, "a", encoding="utf-8") if results else None

    # inputs
    def has(self, name):
        return name in self._inputs

    def _get(self, name, kind, default):
        if name not in self._inputs:
            return default
        v = self._inputs[name]
        if not isinstance(v, kind):
            raise Failed("input %s is not a %s" % (name, kind.__name__))
        return v

    def string(self, name):
        return self._get(name, str, "")

    def number(self, name):
        v = self._inputs.get(name, 0)
        if isinstance(v, bool) or not isinstance(v, (int, float)):
            raise Failed("input %s is not a number" % name)
        return v

    def boolean(self, name):
        return self._get(name, bool, False)

    def list(self, name):
        return self._get(name, list, [])

    def record(self, name):
        return self._get(name, dict, {})

    def secret(self, name):
        ref = self._inputs.get(name)
        if not isinstance(ref, dict) or "secret" not in ref:
            raise Failed("input %s is not a secret reference" % name)
        return os.environ.get(ref["secret"], "")

    # outputs
    def output(self, name, value):
        self._outputs[name] = value

    def secret_output(self, name, value):
        self.mask(value)
        self.output(name, value)

    def write_outputs(self):
        if self._out_path and self._outputs:
            with open(self._out_path, "w", encoding="utf-8") as f:
                json.dump(self._outputs, f)

    # results
    def _result(self, **kw):
        if self._results:
            self._results.write(json.dumps(kw) + "\n")
            self._results.flush()

    def summary(self, markdown):
        self._result(t="summary", markdown=markdown)

    def annotate(self, level, file, line, message):
        self._result(t="annotation", level=level, file=file, line=line, message=message)

    def finding(self, rule, severity, file, line, message):
        self._result(t="finding", rule=rule, severity=severity, file=file, line=line, message=message)

    def test(self, name, status, millis=0, message=""):
        self._result(t="test", name=name, status=status, duration_ms=int(millis), message=message)

    def mask(self, value):
        self._result(t="mask", value=value)


def environ():
    """os.environ without the variables that let a program write results and
    outputs: pass it as env= to the programs the step starts (a build tool, a
    scanner), so that nothing they run can forge a result."""
    return {k: v for k, v in os.environ.items() if k not in ("LAUNCHWAY_STEP_RESULTS", "LAUNCHWAY_STEP_OUTPUTS")}


def run(fn):
    """Open the step, run fn(step), write the outputs and exit with the code the result means."""
    code = 0
    try:
        step = Step()
    except Failed as e:
        print("launchway step:", e, file=sys.stderr)
        sys.exit(1)
    try:
        fn(step)
    except (Failed, Allowed, NeedsPerson) as e:
        code = e.code
        if isinstance(e, NeedsPerson):
            step._result(t="needs_person", message=str(e))
        print(("needs a person: " if code == 65 else "") + str(e), file=sys.stderr)
    step.write_outputs()
    sys.exit(code)
