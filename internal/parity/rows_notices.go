package parity

// The rows of notifications people receive (internal/notices, internal/mail),
// in a file of their own.

func init() {
	const tf = "not needed by terraform"
	rows = append(rows,
		Row{Route: "GET /api/v1/inbox", MCP: "get_inbox", CLI: "inbox list", Terraform: tf, Status: Equal},
		Row{Route: "GET /api/v1/inbox/count", MCP: "get_inbox", CLI: "inbox count", Terraform: tf, Status: Equal},
		Row{Route: "PUT /api/v1/inbox/read", MCP: "mark_inbox", CLI: "inbox read", Terraform: tf, Status: Equal},
		Row{Route: "PUT /api/v1/inbox/done", MCP: "mark_inbox", CLI: "inbox done", Terraform: tf, Status: Equal},
		Row{Route: "GET /api/v1/notices/preferences", MCP: "get_notice_preferences", CLI: "notices prefs", Terraform: tf, Status: Equal},
		Row{Route: "PUT /api/v1/notices/preferences", MCP: "set_notice_preferences", CLI: "notices set, notices watch, notices quiet, notices digest", Terraform: tf, Status: Equal},
		Row{Route: "GET /api/v1/mail", MCP: "get_mail_status", CLI: "notices mail", Terraform: "the mail section of launchway_installation", Status: Equal},
		Row{Route: "POST /api/v1/mail/test", MCP: "send_test_mail", CLI: "notices mail-test", Terraform: tf, Status: Equal},
		Row{Route: "GET /notices/unsubscribe", MCP: "set_notice_preferences", CLI: "notices set", Status: Exempt,
			Reason: "the page behind the mute link in a message: for a person who is not signed in, signed so that it can only mute one kind of mail for the person it was made for; signed in, the preferences do the same and more"},
		Row{Route: "POST /notices/unsubscribe", MCP: "set_notice_preferences", CLI: "notices set", Status: Exempt,
			Reason: "the mute link in a message (also what a mail program's one-click unsubscribe sends): signed, for a person who is not signed in"},
	)
}
