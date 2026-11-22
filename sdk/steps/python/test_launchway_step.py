import json
import os
import tempfile
import unittest

import launchway_step as lw


class KitTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(lambda: [os.environ.pop(k, None) for k in ("LAUNCHWAY_STEP_INPUTS", "LAUNCHWAY_STEP_OUTPUTS", "LAUNCHWAY_STEP_RESULTS", "LW_TEST_TOKEN")])

    def open(self, inputs):
        with open(os.path.join(self.dir, "inputs.json"), "w") as f:
            json.dump({"contract": 1, "inputs": inputs}, f)
        open(os.path.join(self.dir, "results.ndjson"), "w").close()
        os.environ["LAUNCHWAY_STEP_INPUTS"] = os.path.join(self.dir, "inputs.json")
        os.environ["LAUNCHWAY_STEP_OUTPUTS"] = os.path.join(self.dir, "outputs.json")
        os.environ["LAUNCHWAY_STEP_RESULTS"] = os.path.join(self.dir, "results.ndjson")
        return lw.Step()

    def test_inputs_are_typed_and_outputs_and_results_go_to_the_contracts_files(self):
        os.environ["LW_TEST_TOKEN"] = "s3cr3t-value"
        s = self.open({"who": "Ada", "n": 3, "on": True, "files": ["a"], "meta": {"k": 1}, "token": {"secret": "LW_TEST_TOKEN"}})
        self.assertEqual((s.string("who"), s.number("n"), s.boolean("on"), s.list("files"), s.record("meta"), s.secret("token")),
                         ("Ada", 3, True, ["a"], {"k": 1}, "s3cr3t-value"))
        self.assertEqual(s.string("absent"), "")
        s.output("greeting", "hi")
        s.summary("**done**")
        s.annotate("warning", "a.txt", 3, "look")
        s.finding("R1", "high", "a.txt", 3, "bad")
        s.test("t1", "passed", 12)
        s.mask("hunter2-value")
        s.write_outputs()
        with open(os.path.join(self.dir, "outputs.json")) as f:
            self.assertEqual(json.load(f), {"greeting": "hi"})
        with open(os.path.join(self.dir, "results.ndjson")) as f:
            self.assertEqual([json.loads(l)["t"] for l in f], ["summary", "annotation", "finding", "test", "mask"])

    def test_a_wrong_type_fails_instead_of_defaulting(self):
        s = self.open({"who": 5, "flag": 1})
        with self.assertRaises(lw.Failed):
            s.string("who")
        with self.assertRaises(lw.Failed):
            s.boolean("flag")

    def test_environ_leaves_out_what_lets_a_tool_write_results(self):
        self.open({})
        env = lw.environ()
        self.assertNotIn("LAUNCHWAY_STEP_RESULTS", env)
        self.assertNotIn("LAUNCHWAY_STEP_OUTPUTS", env)

    def test_exit_codes(self):
        self.assertEqual((lw.Failed.code, lw.Allowed.code, lw.NeedsPerson.code), (1, 64, 65))


if __name__ == "__main__":
    unittest.main()
