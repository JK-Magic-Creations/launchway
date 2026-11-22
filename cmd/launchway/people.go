package main

// cmdPeople runs the commands about a person's own signing keys, inbox and
// notification choices (signing.go, notices.go).
func cmdPeople(name string, args []string) error {
	switch name {
	case "signing":
		return cmdSigning(args)
	case "inbox":
		return cmdInbox(args)
	}
	return cmdNotices(args)
}
