package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/localauth"
)

// PostgresImage is the bundled database's container image (the chart's default).
const PostgresImage = "docker.io/library/postgres:17.12-alpine"

// serverLayout is where a server install keeps things, for a system install (root) or a user
// install (an ordinary account: a systemd user unit, no new account, nothing outside the home).
type serverLayout struct {
	system          bool
	user            string // the account the service runs as
	conf, data, bin string
	unitDir         string
	ctl             []string // systemctl arguments prefix
}

func (l serverLayout) configFile() string { return filepath.Join(l.conf, "launchway.yaml") }
func (l serverLayout) envFile() string    { return filepath.Join(l.conf, "launchway.env") }
func (l serverLayout) rootKey() string    { return filepath.Join(l.conf, "root.key") }
func (l serverLayout) setupCode() string  { return filepath.Join(l.conf, "setup-code") }
func (l serverLayout) dbPassword() string { return filepath.Join(l.conf, "postgres-password") }

func runServerMode(ctx context.Context, e *Env, a *Asker, o Options, t *Transcript, res Result) (Result, error) {
	if e.GOOS != "linux" {
		return res, fmt.Errorf("server mode installs a systemd service and so needs Linux (this is %s). On this machine choose the trial, or a Kubernetes cluster", e.GOOS)
	}
	if !e.has("systemctl") && e.Root == "" {
		return res, fmt.Errorf("server mode needs systemd (systemctl was not found). Run `launchway server --config FILE` under your own supervisor, or choose Kubernetes")
	}
	scope := "user"
	if e.UID == 0 {
		scope = "system"
	}
	scope, err := a.Ask(Question{Key: "server.scope", Prompt: "Install as a system service or as a service of your own account?", Default: scope, Choices: []Choice{
		{Value: "system", Label: "A system service under its own account `launchway` (needs root; starts at boot)"},
		{Value: "user", Label: "A service of my own account (no root; starts when I log in, or at boot if lingering is on)"},
	}})
	if err != nil {
		return res, err
	}
	l := serverLayout{system: scope == "system", user: "launchway", bin: e.path("/usr/local/bin/launchway"),
		conf: e.path("/etc/launchway"), data: e.path("/var/lib/launchway"), unitDir: e.path("/etc/systemd/system"), ctl: []string{"systemctl"}}
	if !l.system {
		l.user = filepath.Base(e.Home)
		l.conf = filepath.Join(e.Home, ".config", "launchway")
		l.data = filepath.Join(e.Home, ".local", "share", "launchway", "data")
		l.bin = filepath.Join(e.Home, ".local", "bin", "launchway")
		l.unitDir = filepath.Join(e.Home, ".config", "systemd", "user")
		l.ctl = []string{"systemctl", "--user"}
	}
	canDo := !l.system || e.UID == 0 || e.Root != ""
	sa, err := askServerSettings(a, e, &l)
	if err != nil {
		return res, err
	}
	return planServer(ctx, e, a, o, t, res, l, sa, canDo)
}

// serverAnswers are the answers that decide what is written.
type serverAnswers struct {
	external, listen, tls, db, dbURL string
	certFile, keyFile                string
	trustProxy                       bool
}

func askServerSettings(a *Asker, e *Env, l *serverLayout) (sa serverAnswers, err error) {
	if sa.external, err = a.Ask(Question{Key: "server.url", Prompt: "The address people will open (scheme and host, no path)", Required: true,
		Help: "For example https://launchway.example.com or http://192.168.1.20:8080. People are sent back to this address after sign-in."}); err != nil {
		return sa, err
	}
	if !strings.HasPrefix(sa.external, "http://") && !strings.HasPrefix(sa.external, "https://") {
		return sa, fmt.Errorf("the address %q must start with http:// or https://", sa.external)
	}
	if sa.tls, err = a.Ask(Question{Key: "server.tls", Prompt: "How is the connection protected (TLS)?", Default: tlsDefault(sa.external), Choices: []Choice{
		{Value: "proxy", Label: "Behind my own reverse proxy that serves https (Launchway listens on this machine only)"},
		{Value: "files", Label: "I have a certificate and key: write me a ready proxy configuration to use them"},
		{Value: "http", Label: "Plain http on a private network I trust (a warning is shown; passwords cross it unprotected)"},
	}}); err != nil {
		return sa, err
	}
	switch sa.tls {
	case "proxy", "files":
		sa.listen, sa.trustProxy = "127.0.0.1:8080", true
		if sa.tls == "files" {
			if sa.certFile, err = a.Ask(Question{Key: "server.tls_cert_file", Prompt: "Path of the certificate file (PEM)", Required: true}); err != nil {
				return sa, err
			}
			if sa.keyFile, err = a.Ask(Question{Key: "server.tls_key_file", Prompt: "Path of the private key file (PEM)", Required: true}); err != nil {
				return sa, err
			}
		}
	default:
		sa.listen, sa.trustProxy = "0.0.0.0:8080", false
	}
	if sa.listen, err = a.Ask(Question{Key: "server.listen", Prompt: "Address and port Launchway listens on", Default: sa.listen}); err != nil {
		return sa, err
	}
	engineName := ""
	for _, n := range []string{"podman", "docker"} {
		if e.has(n) {
			engineName = n
			break
		}
	}
	bundled := Choice{Value: "bundled", Label: "A PostgreSQL I do not have to look after: run one in a container on this machine"}
	if engineName == "" {
		bundled.Unavailable = "no container engine (podman or docker) was found on this machine"
	}
	if !l.system {
		bundled.Unavailable = "a container unit for your own account is not built yet; choose the system service, or an existing PostgreSQL"
	}
	if sa.db, err = a.Ask(Question{Key: "server.database", Prompt: "Which database?", Default: dbDefault(bundled), Choices: []Choice{
		bundled,
		{Value: "existing", Label: "A PostgreSQL I already have (give its address)"},
		{Value: "builtin", Label: "The built-in file database (one server, small installations; no high availability)"},
	}}); err != nil {
		return sa, err
	}
	if sa.db == "bundled" && bundled.Unavailable != "" {
		return sa, fmt.Errorf("the bundled database is not available: %s", bundled.Unavailable)
	}
	if sa.db == "existing" {
		if sa.dbURL, err = a.Ask(Question{Key: "server.database_url", Prompt: "The PostgreSQL address (postgres://USER:PASSWORD@HOST/DB)", Required: true, Secret: true}); err != nil {
			return sa, err
		}
	}
	l.conf = a.answerOr("server.conf_dir", l.conf)
	l.data = a.answerOr("server.data_dir", l.data)
	return sa, nil
}

func (a *Asker) answerOr(key, def string) string {
	if v, ok := a.answers[key]; ok {
		a.used[key] = true
		return v
	}
	return def
}

func tlsDefault(url string) string {
	if strings.HasPrefix(url, "https://") {
		return "proxy"
	}
	return "http"
}

func dbDefault(bundled Choice) string {
	if bundled.Unavailable == "" {
		return "bundled"
	}
	return "builtin"
}

func planServer(ctx context.Context, e *Env, a *Asker, o Options, t *Transcript, res Result, l serverLayout, sa serverAnswers, canDo bool) (Result, error) {
	unit := serverUnit(l, sa)
	plan := Plan{Keeps: []string{"the root key and the data folder are never removed by the installer, and re-running never makes a second root key"}}
	steps := &plan.Steps
	add := func(title, shell string, do func(context.Context) error) {
		*steps = append(*steps, Step{Title: title, Shell: shell, Do: do})
	}
	ctl := strings.Join(l.ctl, " ")
	if l.system {
		add("Make the account `launchway` that runs the service (no login, no shell; kept if it exists)",
			"useradd --system --home-dir "+l.data+" --shell /usr/sbin/nologin launchway", func(ctx context.Context) error {
				if out, err := e.run(ctx, "id", "-u", l.user); err == nil && out != "" {
					return nil
				}
				if out, err := e.run(ctx, "useradd", "--system", "--home-dir", l.data, "--shell", "/usr/sbin/nologin", l.user); err != nil {
					return fmt.Errorf("useradd: %v %s", err, out)
				}
				return nil
			})
	}
	add("Put the program at "+l.bin, "install -m 0755 "+e.Binary+" "+l.bin, func(context.Context) error {
		b, err := os.ReadFile(e.Binary)
		if err != nil {
			return err
		}
		return writeFile(l.bin, b, 0o755)
	})
	add("Make the data folder "+l.data+" (only the service's account may use it)", "install -d -m 0750 -o "+l.user+" "+l.data, func(ctx context.Context) error {
		if err := os.MkdirAll(l.data, 0o750); err != nil {
			return err
		}
		return e.chown(ctx, l, l.data)
	})
	add("Make the root key once, in "+l.rootKey()+" (mode 0600; if it exists it is NOT touched)", "head -c 32 /dev/urandom | base64 > "+l.rootKey()+" && chmod 600 "+l.rootKey(), makeRootKey(e, l))
	if sa.db == "bundled" {
		add("Make the database password once, in "+l.dbPassword()+" (kept if it exists)", "", func(context.Context) error {
			p, err := randomToken(24)
			if err != nil {
				return err
			}
			_, err = writeIfAbsent(l.dbPassword(), []byte(p+"\n"), 0o600)
			return err
		})
	}
	var code string
	add("Make a one-time setup code for the first administrator in "+l.setupCode()+" (kept if it exists)", "", makeSetupCode(e, l, &code))
	add("Write the settings "+l.configFile()+" and "+l.envFile(), "", writeSettings(e, l, sa))
	if sa.db == "bundled" {
		add("Write the database service "+filepath.Join(l.unitDir, "launchway-postgres.service"), "", func(context.Context) error {
			return writeFile(filepath.Join(l.unitDir, "launchway-postgres.service"), []byte(postgresUnit(e, l)), 0o644)
		})
	}
	add("Write the service "+filepath.Join(l.unitDir, "launchway.service"), "", func(context.Context) error {
		return writeFile(filepath.Join(l.unitDir, "launchway.service"), []byte(unit), 0o644)
	})
	enable := "launchway.service"
	if sa.db == "bundled" {
		enable = "launchway-postgres.service launchway.service"
	}
	add("Start it and have it start again by itself", ctl+" daemon-reload && "+ctl+" enable --now "+enable, enableUnits(e, l, enable))
	plan.Summarize(e, "Here is what I am about to do:")
	if sa.tls == "http" {
		e.Printf("\nWARNING: plain http. Passwords and sessions cross the network unprotected. Use this only on a private network you trust, and put TLS in front of it before anyone else uses it.\n")
	}
	if sa.tls == "files" {
		e.Printf("\nA proxy configuration for your certificate (Caddy) to save as a Caddyfile:\n\n%s\n", caddySnippet(sa))
	}
	res.Steps = len(plan.Steps)
	res.ConfigPath = l.configFile()
	if !canDo {
		e.Printf("\nThis installer is not running as root, so it cannot do the steps above itself. It does NOT fail: run these commands as root (for example with sudo), in this order:\n\n")
		for _, s := range plan.Steps {
			if s.Shell != "" {
				e.Printf("    %s\n", s.Shell)
			}
		}
		e.Printf("\nThe files it would write (%s, %s, the unit files) are shown by running this installer with --dry-run as root, or run it again with sudo and it does all of it.\n", l.configFile(), l.envFile())
		return res, nil
	}
	ok, err := goAhead(a, o, o.DryRun)
	if err != nil || !ok {
		if o.DryRun {
			e.Printf("\nDry run: nothing was changed.\n")
		}
		return res, err
	}
	if err := plan.Execute(ctx, e, t); err != nil {
		return res, err
	}
	url := sa.external
	res.Address = url
	t.Hide(code)
	e.Printf("\nLaunchway is installed as a service and starting. Its address is %s\n", url)
	if code != "" {
		e.Printf("Open it and enter this one-time setup code to make the first administrator (valid 24 hours, works once):\n\n    %s\n\n", code)
	} else {
		e.Printf("A setup code was made earlier; if the first administrator is not made yet and it expired, run:  %s users setup-code --config %s\n", l.bin, l.configFile())
	}
	finish(ctx, e, a, o, t, url, DoctorInput{URL: url, ConfigPath: l.configFile(), Facts: map[string]string{"sign_in": "accounts", "tls": map[bool]string{true: "yes", false: "no"}[strings.HasPrefix(url, "https://")]}})
	return res, nil
}

// chown gives files to the service's account, for a system install.
func (e *Env) chown(ctx context.Context, l serverLayout, paths ...string) error {
	if !l.system {
		return nil
	}
	if e.Root != "" { // a test root: there is no such account to give files to
		return nil
	}
	for _, p := range paths {
		if out, err := e.run(ctx, "chown", l.user+":"+l.user, p); err != nil {
			return fmt.Errorf("chown %s: %v %s", p, err, out)
		}
	}
	return nil
}

func serverConfig(l serverLayout, s serverAnswers) string {
	var b strings.Builder
	b.WriteString("# Written by `launchway install`. Safe to edit; settings kept in the database are changed in the web interface.\n")
	fmt.Fprintf(&b, "listen: %s\ndata: %s\nauth:\n  mode: local\n  local:\n    setup_code_file: %s\n", s.listen, l.data, l.setupCode())
	if s.trustProxy {
		b.WriteString("server:\n  trust_proxy: true   # a reverse proxy in front sets the real client address; keep Launchway reachable only through it\n")
	}
	return b.String()
}

func serverEnv(l serverLayout, s serverAnswers) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "LAUNCHWAY_ROOT_KEY_FILE=%s\n", l.rootKey())
	switch s.db {
	case "existing":
		fmt.Fprintf(&b, "LAUNCHWAY_DATABASE=%s\n", s.dbURL)
	case "bundled":
		p, err := os.ReadFile(l.dbPassword())
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "LAUNCHWAY_DATABASE=postgres://launchway:%s@127.0.0.1:5432/launchway?sslmode=disable\n", strings.TrimSpace(string(p))) //nolint:gosec // G101: the password is the one the installer made, written to a file only the service reads
	}
	return b.String(), nil
}

func serverUnit(l serverLayout, s serverAnswers) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=Launchway\nAfter=network-online.target\nWants=network-online.target\n")
	if s.db == "bundled" {
		b.WriteString("After=launchway-postgres.service\nRequires=launchway-postgres.service\n")
	}
	b.WriteString("\n[Service]\n")
	if l.system {
		fmt.Fprintf(&b, "User=%s\nGroup=%s\n", l.user, l.user)
	}
	fmt.Fprintf(&b, "EnvironmentFile=%s\nExecStart=%s server --config %s\nRestart=on-failure\nRestartSec=5\n", l.envFile(), l.bin, l.configFile())
	b.WriteString("NoNewPrivileges=true\nProtectSystem=strict\nProtectHome=read-only\nPrivateTmp=true\nProtectKernelTunables=true\nProtectControlGroups=true\nRestrictSUIDSGID=true\n")
	fmt.Fprintf(&b, "ReadWritePaths=%s\n", l.data)
	wanted := "multi-user.target"
	if !l.system {
		wanted = "default.target"
	}
	fmt.Fprintf(&b, "\n[Install]\nWantedBy=%s\n", wanted)
	return b.String()
}

func postgresUnit(e *Env, l serverLayout) string {
	engine := "podman"
	if !e.has("podman") {
		engine = "docker"
	}
	return fmt.Sprintf(`[Unit]
Description=Launchway's PostgreSQL (container)
After=network-online.target

[Service]
ExecStartPre=-/usr/bin/%[1]s rm -f launchway-postgres
ExecStart=/usr/bin/%[1]s run --rm --name launchway-postgres -p 127.0.0.1:5432:5432 -v launchway-pgdata:/var/lib/postgresql/data -e POSTGRES_USER=launchway -e POSTGRES_DB=launchway -e POSTGRES_PASSWORD_FILE=/run/secrets/pw -v %[2]s:/run/secrets/pw:ro %[3]s
ExecStop=/usr/bin/%[1]s stop launchway-postgres
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, engine, l.dbPassword(), PostgresImage)
}

func caddySnippet(s serverAnswers) string {
	host := strings.TrimPrefix(strings.TrimPrefix(s.external, "https://"), "http://")
	return fmt.Sprintf("%s {\n\ttls %s %s\n\treverse_proxy %s {\n\t\tflush_interval -1\n\t}\n}\n", host, s.certFile, s.keyFile, s.listen)
}

// makeRootKey makes the root key once: if the file exists it is left exactly as it is.
func makeRootKey(e *Env, l serverLayout) func(context.Context) error {
	return func(ctx context.Context) error {
		k, err := randomKey()
		if err != nil {
			return err
		}
		wrote, err := writeIfAbsent(l.rootKey(), []byte(k+"\n"), 0o600)
		if err != nil {
			return err
		}
		if wrote {
			e.Printf("      A new root key was made. Keep a copy of %s somewhere safe and apart from the database: without it stored secrets cannot be read.\n", l.rootKey())
		} else {
			e.Printf("      The root key already exists and was left untouched.\n")
		}
		return e.chown(ctx, l, l.rootKey())
	}
}

// makeSetupCode makes the one-time setup code for the first administrator, only if none exists.
func makeSetupCode(e *Env, l serverLayout, code *string) func(context.Context) error {
	return func(ctx context.Context) error {
		c := localauth.NewSetupCode()
		wrote, err := writeIfAbsent(l.setupCode(), []byte(c+"\n"), 0o600)
		if err != nil {
			return err
		}
		if wrote {
			*code = c
		}
		return e.chown(ctx, l, l.setupCode())
	}
}

// writeSettings writes the service's settings and its connection settings.
func writeSettings(e *Env, l serverLayout, sa serverAnswers) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := writeFile(l.configFile(), []byte(serverConfig(l, sa)), 0o640); err != nil {
			return err
		}
		env, err := serverEnv(l, sa)
		if err != nil {
			return err
		}
		if err := writeFile(l.envFile(), []byte(env), 0o600); err != nil {
			return err
		}
		return e.chown(ctx, l, l.configFile(), l.envFile())
	}
}

// enableUnits starts the services and has them start again by themselves.
func enableUnits(e *Env, l serverLayout, enable string) func(context.Context) error {
	return func(ctx context.Context) error {
		if out, err := e.run(ctx, l.ctl[0], append(append([]string{}, l.ctl[1:]...), "daemon-reload")...); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %v %s", err, out)
		}
		for _, u := range strings.Fields(enable) {
			if out, err := e.run(ctx, l.ctl[0], append(append([]string{}, l.ctl[1:]...), "enable", "--now", u)...); err != nil {
				return fmt.Errorf("systemctl enable --now %s: %v %s", u, err, out)
			}
		}
		return nil
	}
}
