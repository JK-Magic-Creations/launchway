package argocd

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Sync windows. Argo CD lets an AppProject allow or deny syncs on a
// schedule. Launchway never overrides them, and it must read them itself:
// tested on Argo CD v3.3.7, an operation set while a deny window is active
// waits ("Sync operation blocked by sync window"), but when the window ends
// it is marked Succeeded without applying anything; and an application that
// syncs by itself shows nothing in its status while a window holds it. So
// the deploy runner checks the windows before asking for a sync, never sets
// an operation while one blocks it, and reports a hold it observes.

var appProjects = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}

// Window is one of an AppProject's sync windows.
type Window struct {
	Kind         string // allow or deny
	Schedule     string // cron: minute hour day-of-month month day-of-week
	Duration     time.Duration
	Applications []string
	Namespaces   []string
	Clusters     []string
	ManualSync   bool
	TimeZone     string
}

// windowsOf reads the sync windows of an AppProject object.
func windowsOf(proj map[string]any) ([]Window, error) {
	var out []Window
	for _, w := range listOf(mapOf(proj, "spec"), "syncWindows") {
		wm, _ := w.(map[string]any)
		win := Window{}
		win.Kind, _ = wm["kind"].(string)
		win.Schedule, _ = wm["schedule"].(string)
		win.TimeZone, _ = wm["timeZone"].(string)
		win.ManualSync, _ = wm["manualSync"].(bool)
		ds, _ := wm["duration"].(string)
		d, err := time.ParseDuration(ds)
		if err != nil {
			return nil, fmt.Errorf("sync window %q: duration %q: %v", win.Schedule, ds, err)
		}
		win.Duration = d
		for _, k := range []struct {
			key string
			dst *[]string
		}{{"applications", &win.Applications}, {"namespaces", &win.Namespaces}, {"clusters", &win.Clusters}} {
			for _, v := range listOf(wm, k.key) {
				if s, ok := v.(string); ok {
					*k.dst = append(*k.dst, s)
				}
			}
		}
		out = append(out, win)
	}
	return out, nil
}

// matches reports whether a window applies to an application, by its name,
// destination namespace, or destination cluster (server or name).
func (w Window) matches(app, namespace string, clusters []string) bool {
	match := func(patterns []string, v string) bool {
		for _, p := range patterns {
			if ok, _ := path.Match(p, v); ok {
				return true
			}
		}
		return false
	}
	if match(w.Applications, app) || match(w.Namespaces, namespace) {
		return true
	}
	for _, c := range clusters {
		if c != "" && match(w.Clusters, c) {
			return true
		}
	}
	return false
}

// active reports whether a window is open at now: its schedule fired within
// the last Duration.
func (w Window) active(now time.Time) (bool, error) {
	loc := time.UTC
	if w.TimeZone != "" {
		l, err := time.LoadLocation(w.TimeZone)
		if err != nil {
			return false, fmt.Errorf("sync window %q: time zone %q: %v", w.Schedule, w.TimeZone, err)
		}
		loc = l
	}
	c, err := parseCron(w.Schedule)
	if err != nil {
		return false, fmt.Errorf("sync window %q: %v", w.Schedule, err)
	}
	if w.Duration <= 0 {
		return false, nil
	}
	if w.Duration > 31*24*time.Hour {
		return false, fmt.Errorf("sync window %q: a duration over 31 days is not supported", w.Schedule)
	}
	// A minute at which the schedule fired, in (now - duration, now].
	t := now.In(loc).Truncate(time.Minute)
	stop := now.In(loc).Add(-w.Duration)
	for ; t.After(stop); t = t.Add(-time.Minute) {
		if c.fires(t) {
			return true, nil
		}
	}
	return false, nil
}

// WindowVerdict says whether the windows let an application sync now.
type WindowVerdict struct {
	Allowed bool
	Reason  string // why not, in words
}

// CanSync applies Argo CD's rules: an active deny window blocks (a manual
// sync passes if every active deny window allows manual syncs); if there are
// allow windows, a sync needs one of them active (a manual sync passes an
// inactive one that allows manual syncs).
func CanSync(windows []Window, app, namespace string, clusters []string, manual bool, now time.Time) (WindowVerdict, error) {
	var activeDeny, activeAllow, inactiveAllow []Window
	for _, w := range windows {
		if !w.matches(app, namespace, clusters) {
			continue
		}
		on, err := w.active(now)
		if err != nil {
			return WindowVerdict{}, err
		}
		switch {
		case w.Kind == "deny" && on:
			activeDeny = append(activeDeny, w)
		case w.Kind == "allow" && on:
			activeAllow = append(activeAllow, w)
		case w.Kind == "allow":
			inactiveAllow = append(inactiveAllow, w)
		}
	}
	if len(activeDeny) > 0 {
		manualOK := manual
		for _, w := range activeDeny {
			manualOK = manualOK && w.ManualSync
		}
		if manualOK {
			return WindowVerdict{Allowed: true}, nil
		}
		w := activeDeny[0]
		return WindowVerdict{Reason: fmt.Sprintf("a deny sync window (%s for %s) is active", w.Schedule, w.Duration)}, nil
	}
	if len(activeAllow) > 0 || len(inactiveAllow) == 0 {
		return WindowVerdict{Allowed: true}, nil
	}
	if manual {
		for _, w := range inactiveAllow {
			if w.ManualSync {
				return WindowVerdict{Allowed: true}, nil
			}
		}
	}
	w := inactiveAllow[0]
	return WindowVerdict{Reason: fmt.Sprintf("no allow sync window is open (the next is %s for %s)", w.Schedule, w.Duration)}, nil
}

// ErrNoProjectRight means the runner may not read the AppProject.
var ErrNoProjectRight = errors.New("the deploy runner may not read the application's AppProject")

// SyncWindow reads the application's AppProject and says whether its
// windows let it sync now.
func (c *Cluster) SyncWindow(ctx context.Context, app map[string]any, manual bool, now time.Time) (WindowVerdict, error) {
	spec := mapOf(app, "spec")
	project, _ := spec["project"].(string)
	if project == "" {
		project = "default"
	}
	proj, err := c.get(ctx, appProjects, c.argoNS, project)
	if err != nil {
		var se *apierrors.StatusError
		if errors.As(errors.Unwrap(err), &se) && apierrors.IsForbidden(se) || strings.Contains(err.Error(), "may not read") {
			return WindowVerdict{}, fmt.Errorf("%w %s (grant it get on appprojects): %v", ErrNoProjectRight, project, err)
		}
		return WindowVerdict{}, err
	}
	if proj == nil {
		return WindowVerdict{}, fmt.Errorf("the application's AppProject %s does not exist", project)
	}
	windows, err := windowsOf(proj)
	if err != nil {
		return WindowVerdict{}, err
	}
	dest := mapOf(spec, "destination")
	ns, _ := dest["namespace"].(string)
	server, _ := dest["server"].(string)
	name, _ := dest["name"].(string)
	appName, _ := mapOf(app, "metadata")["name"].(string)
	return CanSync(windows, appName, ns, []string{server, name}, manual, now)
}

// ---- a small cron: minute hour day-of-month month day-of-week ----

type cron struct {
	min, hour, dom, month, dow [64]bool
	domStar, dowStar           bool
}

var cronNames = map[string]string{
	"jan": "1", "feb": "2", "mar": "3", "apr": "4", "may": "5", "jun": "6", "jul": "7", "aug": "8", "sep": "9", "oct": "10", "nov": "11", "dec": "12",
	"sun": "0", "mon": "1", "tue": "2", "wed": "3", "thu": "4", "fri": "5", "sat": "6",
}

var cronMacros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *", "@weekly": "0 0 * * 0",
	"@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

func parseCron(s string) (*cron, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if m, ok := cronMacros[s]; ok {
		s = m
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, fmt.Errorf("a schedule has five fields (minute hour day month weekday)")
	}
	c := &cron{domStar: f[2] == "*" || f[2] == "?", dowStar: f[4] == "*" || f[4] == "?"}
	for i, spec := range []struct {
		dst    *[64]bool
		lo, hi int
	}{{&c.min, 0, 59}, {&c.hour, 0, 23}, {&c.dom, 1, 31}, {&c.month, 1, 12}, {&c.dow, 0, 7}} {
		if err := parseField(f[i], spec.lo, spec.hi, spec.dst); err != nil {
			return nil, fmt.Errorf("field %d (%q): %v", i+1, f[i], err)
		}
	}
	if c.dow[7] {
		c.dow[0] = true // 7 is Sunday too
	}
	return c, nil
}

func parseField(f string, lo, hi int, dst *[64]bool) error {
	for _, part := range strings.Split(f, ",") {
		step := 1
		if r, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return fmt.Errorf("bad step %q", s)
			}
			part, step = r, n
		}
		a, b := lo, hi
		switch {
		case part == "*" || part == "?":
		default:
			x, y, isRange := strings.Cut(part, "-")
			var err error
			if a, err = cronNum(x); err != nil {
				return err
			}
			b = a
			if isRange {
				if b, err = cronNum(y); err != nil {
					return err
				}
			} else if step > 1 {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return fmt.Errorf("%q is out of range %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			dst[v] = true
		}
	}
	return nil
}

func cronNum(s string) (int, error) {
	if n, ok := cronNames[s]; ok {
		s = n
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return n, nil
}

// fires reports whether the schedule fires at minute t. As in standard
// cron, when both day fields are restricted, either may match.
func (c *cron) fires(t time.Time) bool {
	if !c.min[t.Minute()] || !c.hour[t.Hour()] || !c.month[int(t.Month())] {
		return false
	}
	dom, dow := c.dom[t.Day()], c.dow[int(t.Weekday())]
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dow
	case c.dowStar:
		return dom
	}
	return dom || dow
}
