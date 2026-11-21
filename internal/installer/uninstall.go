package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// runUninstall is `launchway install --uninstall` for a machine install. It says what it removes and
// what it KEEPS before doing anything. The root key and the data are kept unless the person says,
// in so many words, to delete the data; the root key is never deleted by the installer.
func runUninstall(ctx context.Context, e *Env, a *Asker, o Options, t *Transcript, res Result) (Result, error) {
	mode, err := a.Ask(Question{Key: "local.mode", Prompt: "What did you install?", Default: "trial", Choices: []Choice{
		{Value: "trial", Label: "A trial or personal install on this machine"},
		{Value: "server", Label: "A server (a systemd service)"},
	}})
	if err != nil {
		return res, err
	}
	var plan Plan
	var dataDir string
	if mode == "trial" {
		dir, err := a.Ask(Question{Key: "local.dir", Prompt: "Which folder did it use?", Default: defaultTrialDir(e)})
		if err != nil {
			return res, err
		}
		l := trialLayout{base: dir}
		dataDir = l.data()
		plan.Steps = append(plan.Steps,
			Step{Title: "Stop Launchway if it is running", Do: func(ctx context.Context) error { return stopPID(l.pid()) }},
			Step{Title: "Remove its settings " + l.config() + " and its run folder " + l.run(), Do: func(context.Context) error {
				_ = os.Remove(l.config())
				return os.RemoveAll(l.run())
			}})
		plan.Keeps = append(plan.Keeps, "your data in "+dataDir+" (the database and everything in it)")
	} else {
		scope := "user"
		if e.UID == 0 {
			scope = "system"
		}
		scope, err = a.Ask(Question{Key: "server.scope", Prompt: "Which kind of service?", Default: scope, Choices: []Choice{{Value: "system", Label: "System service"}, {Value: "user", Label: "Service of my own account"}}})
		if err != nil {
			return res, err
		}
		l := serverLayout{system: scope == "system", bin: e.path("/usr/local/bin/launchway"), conf: e.path("/etc/launchway"), data: e.path("/var/lib/launchway"),
			unitDir: e.path("/etc/systemd/system"), ctl: []string{"systemctl"}}
		if !l.system {
			l.conf = filepath.Join(e.Home, ".config", "launchway")
			l.data = filepath.Join(e.Home, ".local", "share", "launchway", "data")
			l.bin = filepath.Join(e.Home, ".local", "bin", "launchway")
			l.unitDir = filepath.Join(e.Home, ".config", "systemd", "user")
			l.ctl = []string{"systemctl", "--user"}
		}
		l.conf, l.data = a.answerOr("server.conf_dir", l.conf), a.answerOr("server.data_dir", l.data)
		dataDir = l.data
		units := []string{"launchway.service", "launchway-postgres.service"}
		plan.Steps = append(plan.Steps, Step{Title: "Stop and disable the services (" + strings.Join(units, ", ") + ")", Shell: strings.Join(l.ctl, " ") + " disable --now " + strings.Join(units, " "),
			Do: func(ctx context.Context) error {
				for _, u := range units {
					if _, err := os.Stat(filepath.Join(l.unitDir, u)); err == nil {
						if out, err := e.run(ctx, l.ctl[0], append(append([]string{}, l.ctl[1:]...), "disable", "--now", u)...); err != nil {
							return fmt.Errorf("systemctl disable --now %s: %v %s", u, err, out)
						}
					}
				}
				return nil
			}})
		plan.Steps = append(plan.Steps, Step{Title: "Remove the unit files, the program " + l.bin + " and the connection settings " + l.envFile(), Do: func(ctx context.Context) error {
			for _, u := range units {
				_ = os.Remove(filepath.Join(l.unitDir, u))
			}
			_ = os.Remove(l.bin)
			_ = os.Remove(l.envFile())
			_, err := e.run(ctx, l.ctl[0], append(append([]string{}, l.ctl[1:]...), "daemon-reload")...)
			return err
		}})
		plan.Keeps = append(plan.Keeps,
			"the ROOT KEY "+l.rootKey()+" (without it stored secrets in a kept database cannot be read; the installer never deletes it)",
			"the settings and passwords in "+l.conf+" (so installing again finds everything as it was)",
			"the data folder "+l.data+" and the bundled database's volume",
			"the account `launchway`")
	}
	del, err := a.Ask(Question{Key: "uninstall.delete_data", Prompt: "Also delete the data folder " + dataDir + "? This cannot be undone.", Default: "no", Choices: []Choice{
		{Value: "no", Label: "No, keep my data (recommended)"}, {Value: "yes", Label: "Yes, delete the data folder"}}})
	if err != nil {
		return res, err
	}
	if del == "yes" {
		plan.Steps = append(plan.Steps, Step{Title: "DELETE the data folder " + dataDir, Shell: "rm -rf " + dataDir, Do: func(context.Context) error { return os.RemoveAll(dataDir) }})
		plan.Keeps = keepOnly(plan.Keeps, dataDir)
	}
	plan.Summarize(e, "Here is what I am about to remove:")
	res.Steps = len(plan.Steps)
	ok, err := goAhead(a, o, o.DryRun)
	if err != nil || !ok {
		if o.DryRun {
			e.Printf("\nDry run: nothing was removed.\n")
		}
		return res, err
	}
	if err := plan.Execute(ctx, e, t); err != nil {
		return res, err
	}
	e.Printf("\nDone. What was kept is listed above; installing again finds it.\n")
	return res, nil
}

func keepOnly(keeps []string, drop string) []string {
	var out []string
	for _, k := range keeps {
		if !strings.Contains(k, drop) {
			out = append(out, k)
		}
	}
	return out
}

func stopPID(pidFile string) error {
	pid := readPID(pidFile)
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %s did not stop within 10 seconds", strconv.Itoa(pid))
}
