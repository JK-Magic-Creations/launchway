// Package stepkit is the step contract: what a custom step receives, what it
// returns, and how Launchway checks that a step keeps to it. It has no
// dependency on the rest of Launchway, so the command line, a Station and the
// tests all read a manifest, build the input document and read results the
// same way.
//
// # The contract (version 1)
//
// A custom step is any program. A Station starts it in the step's directory
// with these variables set:
//
//	LAUNCHWAY_STEP_CONTRACT  the contract version the Station speaks ("1")
//	LAUNCHWAY_STEP_NAME      the step's name
//	LAUNCHWAY_STEP_DIR       the directory of the step's code
//	LAUNCHWAY_WORKSPACE      the checkout the step works on
//	LAUNCHWAY_STEP_INPUTS    path of a JSON file with the typed inputs
//	LAUNCHWAY_STEP_OUTPUTS   path where the step writes its outputs (JSON)
//	LAUNCHWAY_STEP_RESULTS   path where the step appends results, one JSON object per line
//	LAUNCHWAY_STEP_ARTIFACTS directory whose files become the step's artifact "step"
//	LAUNCHWAY_STEP_REPORTS   directory for report files (SARIF, JUnit, HTML)
//	LAUNCHWAY_WITH_<NAME>    each scalar input (string, number, boolean) as text
//
// Inputs: {"contract":1,"inputs":{"name":value,...}}. A string, number,
// boolean, list (of strings) or record (an object) is its JSON value; a
// secret is {"secret":"ENV_NAME"}: the value is in that environment variable
// (the Station looked it up by reference) and is masked in the log.
//
// Outputs: one JSON object, {"key":value,...}, keys and types as the manifest
// declares. An output of type secret is masked and kept only as ***.
//
// Results are appended to LAUNCHWAY_STEP_RESULTS as lines of JSON, each with
// a "t" (type): summary, annotation, finding, test, mask, needs_person. They
// are never read from the step's output, so ordinary log lines cannot forge
// them. The file is created by the Station and handed to the step only; a tool
// the step starts inherits it only if the step passes the variable on (the
// kits remove it from what they start).
//
// Exit codes: 0 passed; 1 failed (any other number from 2 to 63 too); 64
// failed but allowed (recorded as a pass with a warning); 65 needs a person
// (recorded as failed, with the question the step wrote as its needs_person
// result); anything above 65 is a crash and fails the step.
package stepkit
