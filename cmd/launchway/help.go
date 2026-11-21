package main

import (
	"errors"
	"fmt"
	"os"
)

// Help on every command, with no server and no sign-in (docs/development/interface-quality.md).

// prepareHelp handles a request for help: the commands that have their own text print it and exit 0; the others
// get stand-in credentials so that they get as far as printing their flags. It says whether help was asked for.
func prepareHelp(command string, args []string) bool {
	if !helpRequested(command, args) {
		return false
	}
	if text, ok := helpTexts[command]; ok {
		fmt.Print(text)
		os.Exit(0)
	}
	allowHelpWithoutCredentials()
	return true
}

// exitAfter ends the program with the error's exit code. Asking for help is not a mistake: a flag set that printed
// its help asks for exit 2, which is for a command written wrongly, so help exits 0.
func exitAfter(err error, helping bool) {
	var ex exitErr
	if helping && errors.As(err, &ex) && int(ex) == exitUsage {
		os.Exit(0)
	}
	exitOn(err)
}

// helpTexts are the commands whose arguments are not parsed as flags only: asking for help prints this, and runs nothing.
var helpTexts = map[string]string{
	"validate": `Usage: launchway validate [file] [--project NAME]

Checks a pipeline file (default .launchway/pipeline.yaml) and prints each problem with its file, line and
path, one per line. Exits 0 if the file is valid, 1 if it has problems. With --project NAME the server you
are signed in to checks it for that project as well: its libraries, the limits the installation sets, and
what the project must have for the pipeline to work.
`,
	"graph": `Usage: launchway graph [file]

Prints a pipeline's graph (steps and what each needs) as JSON. The file defaults to .launchway/pipeline.yaml.
Exits 0, or 1 if the file has problems.
`,
}

// helpRequested says whether the arguments ask for help: -h, -help or --help before any "--". The commands that
// start a server or a runner, and those that already answer help themselves, are left alone.
func helpRequested(command string, args []string) bool {
	switch command {
	case "server", "runner", "run", "help", "--help", "-h", "version", "--version", "-v":
		return false
	}
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "-help" || a == "--help" {
			return true
		}
	}
	return false
}

// allowHelpWithoutCredentials lets a client command get as far as printing its flags: the stand-ins are never
// used, because help makes no request.
func allowHelpWithoutCredentials() {
	if os.Getenv("LAUNCHWAY_SERVER") == "" {
		_ = os.Setenv("LAUNCHWAY_SERVER", "http://127.0.0.1:1")
	}
	if os.Getenv("LAUNCHWAY_TOKEN") == "" {
		_ = os.Setenv("LAUNCHWAY_TOKEN", "help")
	}
}
