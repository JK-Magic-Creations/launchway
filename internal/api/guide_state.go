package api

// The first-run guide reads this once: for each step, whether it is done, what it says, and the
// actions that can be performed NOW by this person, each with a plain reason when it cannot. The web
// interface draws buttons only for actions listed available (ruling 21: no button that does not
// work), and the closing step shows the security checklist (internal/securitycheck).

import (
	"net/http"
	"os"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/securitycheck"
)

// GuideStep is one step of the guide.
type GuideStep struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	State   string          `json:"state"` // done, todo or unavailable
	Summary string          `json:"summary"`
	Actions []engine.Action `json:"actions"`
}

// SetupGuide is the answer of GET /api/v1/setup-guide.
type SetupGuide struct {
	Steps     []GuideStep          `json:"steps"`
	Checklist []securitycheck.Item `json:"checklist"`
	// Restart says what is true of this server: it reads some settings only when it starts.
	Kubernetes bool `json:"kubernetes"`
}

func act(name string, ok bool, reason, would string) engine.Action {
	a := engine.Action{Name: name, Available: ok, Who: "admin"}
	if !ok {
		a.Reason, a.WouldHelp = reason, would
	}
	return a
}

func (s *Server) setupGuide(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	g := SetupGuide{Steps: []GuideStep{}, Kubernetes: os.Getenv("KUBERNETES_SERVICE_HOST") != ""}
	localOn := s.localOn()
	oidcOn := s.Auth != nil && s.Auth.OIDC != nil

	// 3: sign-in
	signin := GuideStep{ID: "sign-in", Title: "How people sign in", State: "todo"}
	switch {
	case oidcOn:
		signin.State = "done"
		signin.Summary = "People sign in with your identity provider" + map[bool]string{true: " and built-in accounts", false: ""}[localOn] + "."
	case localOn:
		signin.Summary = "People sign in with built-in accounts (a password and an authenticator). That is a complete, secure choice; connect an identity provider only if your organization has one."
	default:
		signin.Summary = "Sign-in is off on this server: anyone who can reach it has full access."
	}
	signin.Actions = []engine.Action{
		act("test_provider", true, "", ""),
		act("keep_built_in", localOn, "Built-in accounts are not on for this server.", "Turn on auth.mode: local in the server's settings."),
		act("switch_provider_on", false, "Switching the provider on is a setting in the server's own file (auth.mode and auth.oidc) and takes a restart; this server cannot change its own sign-in while it runs, so that cannot be done safely from here.",
			"Test the provider here; when the test works, the guide shows the exact setting to add, and the built-in administrator stays as the way back in."),
	}
	g.Steps = append(g.Steps, signin)

	// 4: encryption
	src := currentRootKeySource()
	enc := GuideStep{ID: "encryption", Title: "Encryption and the root key", State: "todo"}
	taken, takenAt := s.rootKeyCopyTaken()
	switch {
	case src.Available() && taken:
		enc.State = "done"
		enc.Summary = "Stored secrets are encrypted under a root key held outside the database. A recovery copy of the key was taken."
	case src.Available():
		enc.Summary = "Stored secrets are encrypted under a root key held outside the database. No recovery copy of the key has been taken yet: without the key, nothing stored can be read after a loss."
	default:
		enc.Summary = "This server has no root key configured, so stored secrets are not encrypted at rest."
	}
	canExport, why, would := s.canExportRootKey(r)
	enc.Actions = []engine.Action{act("download_root_key_copy", canExport, why, would)}
	_ = takenAt
	g.Steps = append(g.Steps, enc)

	// 5: a runner
	runners, _ := s.Engine.Runners()
	online := 0
	for _, rn := range runners {
		if rn.Online {
			online++
		}
	}
	run := GuideStep{ID: "runner", Title: "A runner", State: "todo"}
	if online > 0 {
		run.State = "done"
		run.Summary = plural(online, "runner is", "runners are") + " connected and can take work."
	} else {
		run.Summary = "No runner is connected, so no step can run yet."
	}
	run.Actions = []engine.Action{act("make_join_token", true, "", "")}
	g.Steps = append(g.Steps, run)

	// 6: Git hosting
	host := GuideStep{ID: "git-hosting", Title: "Git hosting", State: "todo"}
	if s.Forge != nil {
		host.State = "done"
		host.Summary = "Launchway hosts Git repositories."
	} else {
		host.Summary = "Git hosting is off. Turning it on lets Launchway keep your repositories itself; it takes a restart, because the server reads this setting only when it starts."
	}
	host.Actions = []engine.Action{
		act("turn_on_hosting", s.Forge == nil && s.Settings != nil && s.Settings.Available(), orElse(s.Forge != nil, "Git hosting is already on.", "This server keeps its settings in its file, not in the database."), "Turn it on in the server's settings file (forge.hosting: true) and restart."),
	}
	g.Steps = append(g.Steps, host)

	// 7: import
	imp := GuideStep{ID: "import", Title: "Bring in your repositories", State: "todo"}
	conns := 0
	if s.Forge != nil && s.Forge.Mirrors != nil {
		if cs, err := s.Forge.Mirrors.Connections(r.Context()); err == nil {
			conns = len(cs)
		}
	}
	switch {
	case s.Forge == nil:
		imp.State = "unavailable"
		imp.Summary = "Importing needs Git hosting, which is off."
	case conns > 0:
		imp.State = "done"
		imp.Summary = plural(conns, "connection to another Git server exists", "connections to other Git servers exist") + ". Import from it, or add another."
	default:
		imp.Summary = "Copy repositories from another Git server as mirrors; each is checked ref by ref against its source."
	}
	paste := pasteUnavailable()
	imp.Actions = []engine.Action{
		act("add_connection", s.Forge != nil, "Git hosting is off.", "Turn on Git hosting first (step 6) and restart."),
		act("paste_token", s.Forge != nil && paste == "", orElse(s.Forge == nil, "Git hosting is off.", paste), "Give the name of an environment variable or the path of a file that holds the token."),
		act("import", s.Forge != nil && conns > 0, orElse(s.Forge == nil, "Git hosting is off.", "There is no connection to import from yet."), "Add a connection first."),
	}
	g.Steps = append(g.Steps, imp)

	// 8: a first pipeline
	pipe := GuideStep{ID: "pipeline", Title: "Your first pipeline", State: "todo"}
	projects := 0
	if s.Projects != nil {
		projects = len(s.Projects.Projects())
	}
	if projects > 0 {
		pipe.State = "done"
		pipe.Summary = plural(projects, "project is", "projects are") + " set up."
	} else {
		pipe.Summary = "Make a first pipeline from a starter for one of your repositories."
	}
	pipe.Actions = []engine.Action{act("start_wizard", s.Settings != nil && s.Settings.Available(), "This server keeps its settings in its file, not in the database.", "Add the project to the server's settings file.")}
	g.Steps = append(g.Steps, pipe)

	g.Checklist = securitycheck.Check(s.securityFacts(taken, g.Kubernetes, id.User != ""))
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) securityFacts(copied, kube, _ bool) securitycheck.Facts {
	f := securitycheck.Facts{Kubernetes: kube}
	switch {
	case s.Auth == nil:
		f.SignIn, f.SignInKind = securitycheck.No, "none"
	case s.Auth.OIDC != nil:
		f.SignIn, f.SignInKind = securitycheck.Yes, "identity provider"
	default:
		f.SignIn, f.SignInKind = securitycheck.Yes, "built-in accounts"
	}
	f.Encrypted = securitycheck.Of(currentRootKeySource().Available())
	f.RootKeyCopied = securitycheck.Of(copied)
	if f.Encrypted == securitycheck.No {
		f.RootKeyCopied = securitycheck.Unknown
	}
	if s.Auth != nil && s.Auth.SecureCookies {
		f.TLS = securitycheck.Yes
	} else if s.TrustProxy {
		f.TLS = securitycheck.Unknown
	}
	return f
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func orElse(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
