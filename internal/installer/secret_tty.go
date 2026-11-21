package installer

import (
	"os"

	"golang.org/x/term"
)

// readSecret reads a line without echo when input is a terminal.
func readSecret(e *Env) (string, bool) {
	f, ok := e.In.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", false
	}
	b, err := term.ReadPassword(int(f.Fd()))
	_, _ = e.Out.Write([]byte("\n"))
	if err != nil {
		return "", false
	}
	return string(b), true
}
