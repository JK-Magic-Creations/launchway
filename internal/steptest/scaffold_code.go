package steptest

// The code a new step starts with, in each language: the logic is a plain
// function the step's own test calls, and main reads the inputs, calls it, and
// reports through the kit.

const goMod = `module steps/[[.Name]]

go 1.25
`

const goMain = `package main

import (
	"fmt"
	"strings"

	"steps/[[.Name]]/lwstep"
)

// greet is the step's logic: a plain function, tested without Launchway.
func greet(who string, shout bool) string {
	g := "Hello, " + who + "!"
	if shout {
		g = strings.ToUpper(g)
	}
	return g
}

func main() {
	lwstep.Run(func(s *lwstep.Step) error {
		g := greet(s.String("who"), s.Bool("shout"))
		fmt.Println(g)
		s.Output("greeting", g)
		s.Output("length", len(g))
		s.Summary(fmt.Sprintf("Greeted **%s** (%d characters)", s.String("who"), len(g)))
		if len(g) > 40 {
			s.Annotate("warning", "README.md", 1, "that is a long greeting")
		}
		return nil
	})
}
`

const goTest = `package main

import "testing"

func TestGreet(t *testing.T) {
	if got := greet("Ada", false); got != "Hello, Ada!" {
		t.Errorf("got %q", got)
	}
	if got := greet("Ada", true); got != "HELLO, ADA!" {
		t.Errorf("got %q", got)
	}
}
`

const pyMain = `import launchway_step as lw


def greet(who, shout=False):
    """The step's logic: a plain function, tested without Launchway."""
    g = "Hello, %s!" % who
    return g.upper() if shout else g


def main(step):
    g = greet(step.string("who"), step.boolean("shout"))
    print(g)
    step.output("greeting", g)
    step.output("length", len(g))
    step.summary("Greeted **%s** (%d characters)" % (step.string("who"), len(g)))
    if len(g) > 40:
        step.annotate("warning", "README.md", 1, "that is a long greeting")


if __name__ == "__main__":
    lw.run(main)
`

const pyTest = `import unittest

from main import greet


class GreetTest(unittest.TestCase):
    def test_greet(self):
        self.assertEqual(greet("Ada"), "Hello, Ada!")
        self.assertEqual(greet("Ada", True), "HELLO, ADA!")


if __name__ == "__main__":
    unittest.main()
`

const tsMain = `import { run } from "./[[.Kit]]";
import type { Step } from "./[[.Kit]]";

// greet is the step's logic: a plain function, tested without Launchway.
export function greet(who: string, shout = false): string {
  const g = ` + "`Hello, ${who}!`" + `;
  return shout ? g.toUpperCase() : g;
}

if (process.env.LAUNCHWAY_STEP_INPUTS) {
  run((step: Step) => {
    const g = greet(step.string("who"), step.boolean("shout"));
    console.log(g);
    step.output("greeting", g);
    step.output("length", g.length);
    step.summary(` + "`Greeted **${step.string(\"who\")}** (${g.length} characters)`" + `);
    if (g.length > 40) step.annotate("warning", "README.md", 1, "that is a long greeting");
  });
}
`

const tsTest = `import { test } from "node:test";
import assert from "node:assert/strict";
import { greet } from "./main.ts";

test("greet", () => {
  assert.equal(greet("Ada"), "Hello, Ada!");
  assert.equal(greet("Ada", true), "HELLO, ADA!");
});
`

const shMain = `#!/bin/sh
# The step's logic is plain shell; test.sh tests it without Launchway.
. "$(dirname "$0")/[[.Kit]]"

greet() { # greet WHO SHOUT
  if [ "$2" = "true" ]; then printf 'HELLO, %s!' "$(printf '%s' "$1" | tr 'a-z' 'A-Z')"; else printf 'Hello, %s!' "$1"; fi
}

if [ -n "${LAUNCHWAY_STEP_INPUTS-}" ]; then
  g=$(greet "$(lw_input who)" "$(lw_input shout)")
  echo "$g"
  lw_output_string greeting "$g"
  lw_output_number length "${#g}"
  lw_summary "Greeted **$(lw_input who)** (${#g} characters)"
  if [ "${#g}" -gt 40 ]; then lw_annotate warning README.md 1 "that is a long greeting"; fi
  lw_finish
fi
`

const shTest = `#!/bin/sh
# Tests the step's logic without Launchway: sh test.sh
dir=$(dirname "$0")
. "$dir/main.sh"
[ "$(greet Ada false)" = "Hello, Ada!" ] || { echo "greet Ada false: got $(greet Ada false)" >&2; exit 1; }
[ "$(greet Ada true)" = "HELLO, ADA!" ] || { echo "greet Ada true: got $(greet Ada true)" >&2; exit 1; }
echo ok
`
