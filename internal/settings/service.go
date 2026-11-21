package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// OriginBootstrap is the origin of the one-time import of the file a server
// starts with.
const OriginBootstrap = "bootstrap-file"

// ErrUnavailable: this server or its database does not keep configuration
// in the database (a build limited to an older schema).
var ErrUnavailable = errors.New("configuration in the database needs schema 23")

// EgressPolicy is what a configuration allows the server to reach.
func EgressPolicy(c *config.Config) egress.Policy {
	return egress.Policy{AllowLocal: c.Egress.AllowLocal, Allow: c.Egress.Allow}
}

// checkEgress refuses addresses in changed objects that the server would
// not be allowed to fetch from: a path on the server unless the server was
// started allowing them, and an internal address unless an administrator
// allowed it. Names are not resolved here (a lookup does not belong in a
// validation that runs on every keystroke of a wizard); the fetch does it.
func checkEgress(c *config.Config, objs []engine.ConfigObject) error {
	g := egress.New(EgressPolicy(c))
	if err := g.Set(EgressPolicy(c)); err != nil {
		return err
	}
	var errs []error
	for _, o := range objs {
		k := kindByName(o.Kind)
		if k == nil || o.Deleted() {
			continue
		}
		v, _, err := decodeSpec(k, o.Name, o.Spec)
		if err != nil {
			continue
		}
		var e error
		switch t := v.(type) {
		case *config.ProjectConfig:
			e = g.CheckRepoSyntax(t.Repo)
		case *config.LibraryConfig:
			e = g.CheckRepoSyntax(t.Repo)
		case *ai.Model:
			if u, err := url.Parse(t.Endpoint); err == nil {
				e = g.CheckHostSyntax(u.Hostname())
			}
		}
		if e != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w; an administrator can add an internal host to the allowed hosts in the installation settings (egress)", o.Kind, o.Name, e))
		}
	}
	return errors.Join(errs...)
}

// Applier puts a checked configuration into effect on this server: the
// engine's, the projects' and the API's live settings.
type Applier func(c *config.Config, lay *Layout) error

// Service is the configuration in the database, as one server sees it.
type Service struct {
	eng  *engine.Engine
	boot *config.Config
	log  *slog.Logger
	// Poll is how often the revision is read, in case a notification was
	// missed; default 2s.
	Poll time.Duration

	mu    sync.Mutex // one reload at a time
	cur   *config.Config
	lay   *Layout
	rev   int64
	apply Applier
	// safe is set when this server started in safe mode.
	safe *Safe
	// server names this server in the audit trail's record of which
	// revision each server applied; recorded is the last state written.
	server    string
	recorded  string
	lastPrune time.Time
	// lastErr is why the newest configuration was not applied, if it was
	// not.
	lastErr string
	// writeMu keeps this server's writes in turn; other servers' are kept
	// apart by the revision.
	writeMu sync.Mutex
}

// New returns the service for a server that started with boot.
func New(eng *engine.Engine, boot *config.Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "server"
	}
	return &Service{eng: eng, boot: boot, log: log, Poll: 2 * time.Second, server: host}
}

// Available reports whether configuration is kept in the database.
func (s *Service) Available() bool { return s != nil && s.eng != nil && s.eng.ConfigKnown() }

// Boot is the file the server started with.
func (s *Service) Boot() *config.Config { return s.boot }

// Engine is the engine the service stores in.
func (s *Service) Engine() *engine.Engine { return s.eng }

func (s *Service) check() error {
	if !s.Available() {
		return fmt.Errorf("%w: %w", engine.ErrConflict, ErrUnavailable)
	}
	return nil
}

// ImportResult says what the start-up import did.
type ImportResult struct {
	// Import is the import of the file, by this server or an earlier one.
	Import *engine.ConfigImport
	// Done: this server made it now.
	Done bool
	// Ignored are the keys of the file that moved to the database and that
	// the file sets: from now on they are not read.
	Ignored []string
}

// ImportBootstrap imports, once per installation, the settings of the file
// the server started with that now live in the database. Every server of
// an installation may call it as it starts; exactly one import happens,
// recorded with what it brought. Afterwards the file's copies are ignored.
func (s *Service) ImportBootstrap() (*ImportResult, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	res := &ImportResult{Ignored: movedKeys(s.boot)}
	digest := ""
	if f := s.boot.File(); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			sum := sha256.Sum256(b)
			digest = hex.EncodeToString(sum[:])
		}
	}
	for attempt := 0; attempt < 10; attempt++ {
		prior, err := s.eng.ConfigImportByOrigin(OriginBootstrap)
		if err != nil {
			return nil, err
		}
		if prior != nil {
			res.Import = prior
			return res, nil
		}
		rev, objs, err := s.eng.ConfigSnapshot()
		if err != nil {
			return nil, err
		}
		file, err := FromConfig(s.boot)
		if err != nil {
			return nil, fmt.Errorf("importing the configuration file: %w", err)
		}
		for _, o := range file {
			k := kindByName(o.Kind)
			v, _, err := decodeSpec(k, o.Name, o.Spec)
			if err == nil {
				err = checkOwn(k, v)
			}
			if err != nil {
				return nil, fmt.Errorf("importing the configuration file: %s %s: %w", o.Kind, o.Name, err)
			}
		}
		writes := make([]engine.ConfigWrite, 0, len(file))
		after := mergeObjects(objs, file)
		cp, err := s.composeChecked(after)
		if err != nil {
			return nil, fmt.Errorf("importing the configuration file: %w", err)
		}
		if err := checkEgress(cp.cfg, file); err != nil {
			return nil, fmt.Errorf("importing the configuration file: %w", err)
		}
		counts := map[string]int{}
		var names []string
		for _, o := range file {
			writes = append(writes, engine.ConfigWrite{Kind: o.Kind, Name: o.Name, Unit: o.Unit, Spec: o.Spec, Action: engine.ConfigUpdate,
				IfVersion: -1, ManagedBy: engine.ManagedByImport, Comment: "imported from the configuration file"})
			counts[o.Kind]++
			names = append(names, o.Kind+"/"+o.Name)
		}
		sum, _ := json.Marshal(map[string]any{"file": s.boot.File(), "keys": res.Ignored, "counts": counts, "objects": names})
		imp := &engine.ConfigImport{Origin: OriginBootstrap, Digest: digest, Summary: sum}
		_, _, err = s.eng.WriteConfig(rev, "launchway (import of "+displayFile(s.boot.File())+")", writes, imp)
		if errors.Is(err, engine.ErrConfigMoved) {
			continue
		}
		if err != nil {
			return nil, err
		}
		res.Import, res.Done = imp, true
		return res, nil
	}
	return nil, errors.New("importing the configuration file: other servers kept changing the configuration; start again")
}

func displayFile(f string) string {
	if f == "" {
		return "no file"
	}
	return f
}

// mergeObjects returns objs with puts applied (deleted ones made live).
func mergeObjects(objs []engine.ConfigObject, puts []engine.ConfigObject) []engine.ConfigObject {
	out := make([]engine.ConfigObject, 0, len(objs)+len(puts))
	idx := map[string]int{}
	for _, o := range objs {
		idx[o.Kind+"\x00"+o.Name] = len(out)
		out = append(out, o)
	}
	for _, p := range puts {
		if i, ok := idx[p.Kind+"\x00"+p.Name]; ok {
			o := out[i]
			o.Spec, o.Unit, o.DeletedAt = p.Spec, p.Unit, 0
			out[i] = o
			continue
		}
		idx[p.Kind+"\x00"+p.Name] = len(out)
		out = append(out, p)
	}
	return out
}

// composeChecked composes and checks.
func (s *Service) composeChecked(objs []engine.ConfigObject) (*composed, error) {
	c, lay, err := Compose(s.boot, objs)
	if err != nil {
		return nil, err
	}
	if err := Check(c, s.eng); err != nil {
		return nil, err
	}
	return &composed{cfg: c, lay: lay}, nil
}

type composed struct {
	cfg *config.Config
	lay *Layout
}

// Check holds a composed configuration to every rule a server applies
// before using one: the configuration file's (config.Config.Validate),
// the projects' and libraries' (source.CheckProjects), the runner pools'
// and the engine's.
func Check(c *config.Config, eng *engine.Engine) error {
	if err := c.Validate("configuration"); err != nil {
		return err
	}
	if err := egress.CheckAllowList(c.Egress.Allow); err != nil {
		return fmt.Errorf("egress.allow: %w", err)
	}
	projects, err := c.SourceProjects()
	if err != nil {
		return err
	}
	if err := source.CheckProjects(projects, c.SourceLibraries()); err != nil {
		return err
	}
	live, err := c.EngineLive()
	if err != nil {
		return err
	}
	if eng != nil {
		return eng.CheckLive(live)
	}
	return nil
}

// Load reads the configuration, checks it and applies it with apply, which
// is then used for every later change. A configuration that does not pass
// stops the server, as a mistake in the file always has.
func (s *Service) Load(apply Applier) error {
	if err := s.check(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apply = apply
	err := s.reloadLocked(true)
	if err == nil {
		return nil
	}
	// A stored configuration that cannot be used must not stop every server
	// with no way back: run with the last one that worked (or the file
	// alone), say so, and keep trying (Watch).
	fb, using := s.fallback()
	if aerr := apply(fb.cfg, fb.lay); aerr != nil {
		return fmt.Errorf("the stored configuration cannot be used (%v) and neither can %s: %w", err, using, aerr)
	}
	s.cur, s.lay = fb.cfg, fb.lay
	if s.safe == nil {
		s.safe = &Safe{Reason: s.lastErr, Using: using}
		s.log.Error("SAFE MODE: the stored configuration cannot be used, so this server runs with "+using, "err", err)
	}
	return nil
}

// Current returns the configuration in effect, its layout and revision.
func (s *Service) Current() (*config.Config, *Layout, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.lay, s.rev
}

// Refresh applies the stored configuration if it changed, or if the last
// try failed. It returns why it could not.
func (s *Service) Refresh() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.apply == nil {
		return nil
	}
	return s.reloadLocked(false)
}

func (s *Service) reloadLocked(first bool) error {
	rev, err := s.eng.ConfigRevision()
	if err != nil {
		return err
	}
	if !first && rev == s.rev && s.lastErr == "" {
		return nil
	}
	rev, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return err
	}
	cp, err := s.composeChecked(objs)
	if err == nil {
		err = checkAdministrable(cp.cfg)
	}
	if err == nil {
		err = s.projectStepResources(objs)
	}
	if err == nil {
		err = s.apply(cp.cfg, cp.lay)
	}
	if err != nil {
		s.recordState(rev, err)
		msg := fmt.Sprintf("revision %d: %v", rev, err)
		if msg != s.lastErr {
			s.log.Error("the stored configuration cannot be used here; keeping the configuration in effect", "revision", rev, "err", err)
		}
		s.lastErr = msg // tried again at the next poll, and when the revision changes
		return err
	}
	if !first && s.rev != rev {
		s.log.Info("applied a change to the configuration", "revision", rev)
	}
	s.cur, s.lay, s.rev, s.lastErr = cp.cfg, cp.lay, rev, ""
	s.recordState(rev, nil)
	if s.safe != nil {
		s.log.Warn("leaving safe mode: the stored configuration is in effect", "revision", rev)
		s.safe = nil
	}
	warnUnknown(s.log, objs)
	s.saveLastGood(rev, objs)
	_ = egress.Default.Set(EgressPolicy(cp.cfg))
	return nil
}

// Watch applies changes made by any server until ctx ends: at once when
// this server or a notification says so, and otherwise within Poll.
func (s *Service) Watch(ctx context.Context) {
	kick := make(chan struct{}, 1)
	s.eng.OnConfigChange(func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	})
	t := time.NewTicker(s.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
		_ = s.Refresh()
		s.pruneHistory()
	}
}

// ---- reading ----

// ---- changing ----

// Change is one change asked for.
type Change struct {
	Kind *Kind
	Name string
	// Action is engine.ConfigUpdate (create or replace), ConfigDelete or
	// ConfigRestore.
	Action string
	Unit   string
	Spec   json.RawMessage
	// IfVersion: -1 any; 0 create only; otherwise the version expected.
	IfVersion int64
	ManagedBy string
	Comment   string
	// TakeOwnership lets a change be made to an object another manager owns
	// (Terraform, Git), and makes the caller its owner; recorded in the
	// history and the audit trail.
	TakeOwnership bool
	// Force deletes an object that active runs depend on (see ConfigInUse);
	// without it such a delete is refused. A forced delete is recorded in
	// the object's history and the audit trail.
	Force bool
}

// Result is what a change did.
type Result struct {
	Object  *engine.ConfigObject `json:"object"`
	Change  *engine.ConfigChange `json:"change,omitempty"`
	Changed bool                 `json:"changed"`
	// Forced lists what a forced delete cut short.
	Forced []string `json:"forced,omitempty"`
	// TookOwnership says whose the object was, when the change took it.
	TookOwnership string `json:"took_ownership,omitempty"`
}

// Problem is a reason a change is refused.
type Problem struct {
	Message string `json:"message"`
}

// InvalidError is a change the rules refuse; Problems says why.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string { return strings.Join(e.Problems, "; ") }

// Is makes an InvalidError an engine.ErrInvalid.
func (e *InvalidError) Is(target error) bool { return target == engine.ErrInvalid }

func invalid(err error) error {
	var ie *InvalidError
	if errors.As(err, &ie) {
		return err
	}
	var parts []string
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, e := range joined.Unwrap() {
			parts = append(parts, e.Error())
		}
	} else {
		parts = strings.Split(err.Error(), "\n")
	}
	return &InvalidError{Problems: parts}
}

// prepare checks a change on its own and makes the write.
func (s *Service) prepare(c Change) (engine.ConfigWrite, *engine.ConfigObject, error) {
	k := c.Kind
	w := engine.ConfigWrite{Kind: k.Name, Name: c.Name, Action: c.Action, Unit: c.Unit, IfVersion: c.IfVersion, ManagedBy: c.ManagedBy, Comment: c.Comment}
	switch {
	case k.Singleton != "" && c.Name != k.Singleton:
		return w, nil, invalid(fmt.Errorf("there is one %s, named %s", strings.ToLower(k.Title), k.Singleton))
	case !ValidName(c.Name):
		return w, nil, invalid(fmt.Errorf("%q: names are letters, digits, '.', '_' and '-', starting with a letter or digit, at most 100", c.Name))
	case c.Unit != "" && !k.Levels:
		return w, nil, invalid(fmt.Errorf("a %s belongs to the whole Base, not to a Squadron", strings.ToLower(k.Title)))
	case k.Singleton != "" && (c.Action == engine.ConfigDelete || c.Action == engine.ConfigRestore):
		return w, nil, invalid(fmt.Errorf("the %s cannot be deleted; change it instead", strings.ToLower(k.Title)))
	case len(c.Comment) > 500:
		return w, nil, invalid(errors.New("a comment is at most 500 characters"))
	}
	if c.ManagedBy != "" && c.ManagedBy != engine.ManagedByAPI && c.ManagedBy != engine.ManagedByTerraform && c.ManagedBy != engine.ManagedByImport && c.ManagedBy != engine.ManagedByGit {
		return w, nil, invalid(fmt.Errorf("managed by %q: api, terraform, git or import", c.ManagedBy))
	}
	if c.Action == engine.ConfigUpdate || c.Action == engine.ConfigCreate {
		v, stored, err := decodeSpec(k, c.Name, c.Spec)
		if err != nil {
			return w, nil, invalid(err)
		}
		if err := checkOwn(k, v); err != nil {
			return w, nil, invalid(err)
		}
		w.Spec = stored
		return w, &engine.ConfigObject{Kind: k.Name, Name: c.Name, Unit: c.Unit, Spec: stored}, nil
	}
	return w, nil, nil
}

// checkOwn checks what only this kind's objects say (the whole
// configuration is checked after).
func checkOwn(k *Kind, v any) error {
	switch t := v.(type) {
	case *Installation:
		if t.History.Retain != "" {
			if d, ok := pipeline.ParseRetain(t.History.Retain); !ok || d < 7*24*time.Hour {
				return fmt.Errorf("history.retain %q: a length of time such as 90d or 52w, at least 7d; leave it empty to keep every change", t.History.Retain)
			}
		}
		if t.Reports.Origin != "" {
			return errors.New("reports.origin stays in the configuration file: it is part of how the server is reached")
		}
		if err := t.Terms.check(); err != nil {
			return err
		}
		return t.Levels.check()
	case *config.ProjectConfig:
		if err := checkAddress("repo", t.Repo); err != nil {
			return err
		}
		return t.Normalize()
	case *config.LibraryConfig:
		return checkAddress("repo", t.Repo)
	case *ai.Model:
		if err := checkAddress("endpoint", t.Endpoint); err != nil {
			return err
		}
		return checkHeaders("headers", t.Headers)
	case *StepResources:
		return checkStepResources(t)
	case *SignIn:
		if t.Enabled {
			return errors.New("enabled: sign-in cannot be switched on from here yet: that needs a successful test sign-in by the acting administrator first. Save the settings with enabled: false; the servers keep the sign-in of their bootstrap file")
		}
		if err := checkAddress("issuer", t.Issuer); err != nil {
			return err
		}
		a := config.AuthConfig{Mode: config.AuthOIDC, OIDC: config.OIDCConfig{Issuer: t.Issuer, ClientID: t.ClientID, RedirectURL: t.RedirectURL,
			ClientSecretEnv: t.ClientSecretEnv, GroupsClaim: t.GroupsClaim, UsernameClaim: t.UsernameClaim}, Admins: t.Admins}
		return a.Check()
	case *SecretStore:
		if !containsString(SecretStoreTypes, t.Type) {
			return fmt.Errorf("type %q: one of %s", t.Type, strings.Join(SecretStoreTypes, ", "))
		}
		for _, p := range t.Projects {
			if err := checkPattern(p); err != nil {
				return err
			}
		}
	case *Unit:
		if len(t.DisplayName) > 80 {
			return errors.New("display_name is at most 80 characters")
		}
	}
	return nil
}

func checkPattern(p string) error {
	if p == "" {
		return errors.New("an empty project pattern")
	}
	if _, err := pathMatch(p, ""); err != nil {
		return fmt.Errorf("%q is not a valid project pattern", p)
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Apply makes changes, all or none, after checking the whole configuration
// they would make, and puts it into effect on this server before returning
// (other servers follow within seconds). actor is who asked.
func (s *Service) Apply(actor string, changes []Change) ([]Result, error) {
	return s.apply1(actor, changes, nil, false)
}

// DryRun checks changes as Apply would, and stores nothing.
func (s *Service) DryRun(changes []Change) error {
	_, err := s.apply1("", changes, nil, true)
	return err
}

func (s *Service) apply1(actor string, changes []Change, imp *engine.ConfigImport, dry bool) ([]Result, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	writes, puts, forced, err := s.prepareAll(changes)
	if err != nil {
		return nil, err
	}
	if !dry {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	for attempt := 0; attempt < 10; attempt++ {
		rev, objs, err := s.eng.ConfigSnapshot()
		if err != nil {
			return nil, err
		}
		took, err := s.checkWhole(objs, changes, writes, puts, imp)
		if err != nil {
			return nil, err
		}
		if dry {
			return nil, nil
		}
		stored, recorded, err := s.eng.WriteConfig(rev, actor, writes, imp)
		if errors.Is(err, engine.ErrConfigMoved) {
			continue
		}
		if err != nil {
			return nil, err
		}
		applyErr := s.Refresh()
		_, now, err := s.eng.ConfigSnapshot()
		if err != nil {
			return nil, err
		}
		// This server's tables follow at once, whether or not it applies
		// configuration to a running engine (the others follow within seconds).
		if err := s.projectStepResources(now); err != nil {
			return nil, err
		}
		results := resultsOf(writes, now, recorded, forced, took)
		if applyErr != nil {
			return results, &NotAppliedError{Revision: stored, Server: s.server, Cause: applyErr}
		}
		return results, nil
	}
	return nil, fmt.Errorf("%w: other changes kept coming first; try again", engine.ErrConflict)
}

// prepareAll checks each change on its own, and refuses deleting what active
// runs use unless forced.
func (s *Service) prepareAll(changes []Change) (writes []engine.ConfigWrite, puts []engine.ConfigObject, forced map[string][]string, err error) {
	writes = make([]engine.ConfigWrite, 0, len(changes))
	forced = map[string][]string{}
	for _, c := range changes {
		w, obj, err := s.prepare(c)
		if err != nil {
			return nil, nil, nil, err
		}
		if c.Action == engine.ConfigDelete {
			use, err := s.eng.ConfigInUse(c.Kind.Name, c.Name)
			if err != nil {
				return nil, nil, nil, err
			}
			switch {
			case len(use) > 0 && !c.Force:
				return nil, nil, nil, invalid(fmt.Errorf("%s %s is in use: %s. Wait for these to finish, or delete it anyway with force (the runs in flight then lose what it gave them, and the forced delete is recorded)",
					c.Kind.Name, c.Name, strings.Join(use, "; ")))
			case len(use) > 0:
				forced[c.Kind.Name+"\x00"+c.Name] = use
				w.Comment = clipText(strings.TrimSpace(w.Comment+" [forced while in use: "+strings.Join(use, "; ")+"]"), 500)
			}
		}
		writes = append(writes, w)
		if obj != nil {
			puts = append(puts, *obj)
		}
	}
	return writes, puts, forced, nil
}

// checkWhole holds the configuration the changes would make to every rule:
// who owns what, files on the server, the whole configuration's checks,
// addresses, and that somebody can still administer. It returns whose
// objects were taken over.
func (s *Service) checkWhole(objs []engine.ConfigObject, changes []Change, writes []engine.ConfigWrite, puts []engine.ConfigObject, imp *engine.ConfigImport) (map[string]string, error) {
	took, err := checkOwnership(objs, changes, writes)
	if err != nil {
		return nil, invalid(err)
	}
	after, err := applyLocally(objs, writes)
	if err != nil {
		return nil, err
	}
	changed := putObjects(after, puts)
	if imp == nil || imp.Origin != OriginBootstrap {
		if err := checkServerPaths(objs, changed); err != nil {
			return nil, invalid(err)
		}
	}
	cp, err := s.composeChecked(after)
	if err != nil {
		return nil, invalid(err)
	}
	if err := checkEgress(cp.cfg, changed); err != nil {
		return nil, invalid(err)
	}
	if err := checkAdministrable(cp.cfg); err != nil {
		return nil, invalid(err)
	}
	return took, nil
}

// resultsOf pairs each write with the object as it is now and the change
// recorded for it.
func resultsOf(writes []engine.ConfigWrite, now []engine.ConfigObject, recorded []engine.ConfigChange, forced map[string][]string, took map[string]string) []Result {
	results := make([]Result, 0, len(writes))
	for _, w := range writes {
		r := Result{Forced: forced[w.Kind+"\x00"+w.Name], TookOwnership: took[w.Kind+"\x00"+w.Name]}
		for i := range now {
			if now[i].Kind == w.Kind && now[i].Name == w.Name {
				o := now[i]
				r.Object = &o
			}
		}
		for i := range recorded {
			if recorded[i].Kind == w.Kind && recorded[i].Name == w.Name {
				c := recorded[i]
				r.Change, r.Changed = &c, true
			}
		}
		results = append(results, r)
	}
	return results
}

// protectedOwner: a manager whose objects another writer must not change
// without saying so, because its next run would undo the change.
func protectedOwner(m string) bool { return m == engine.ManagedByTerraform || m == engine.ManagedByGit }

// checkOwnership refuses a change to an object that Terraform (or Git) owns
// from anyone who is not that owner, unless they explicitly take ownership,
// and a claim of an object that this server's API owns the same way. The
// declared manager is only a label the client sends: it never lets a change
// through by itself. It returns whose objects were taken, by object.
func checkOwnership(objs []engine.ConfigObject, changes []Change, writes []engine.ConfigWrite) (map[string]string, error) {
	cur := map[string]engine.ConfigObject{}
	for _, o := range objs {
		cur[o.Kind+"\x00"+o.Name] = o
	}
	took := map[string]string{}
	var errs []error
	for i, w := range writes {
		o, ok := cur[w.Kind+"\x00"+w.Name]
		if !ok || o.Deleted() && w.Action != engine.ConfigRestore {
			continue
		}
		owner, declared := o.ManagedBy, w.ManagedBy
		if declared == "" {
			declared = engine.ManagedByAPI
		}
		if owner == "" || owner == declared {
			continue
		}
		claim := protectedOwner(declared) && owner == engine.ManagedByAPI
		if !protectedOwner(owner) && !claim {
			continue // an imported or unowned object is anyone's
		}
		if !changes[i].TakeOwnership {
			what := fmt.Sprintf("%s %s is managed by %s", w.Kind, w.Name, owner)
			if claim {
				what = fmt.Sprintf("%s %s is managed here (the API, web UI and CLI)", w.Kind, w.Name)
			}
			errs = append(errs, fmt.Errorf("%s: a change from %s would be undone or would take it over. Change it where it is managed, or take ownership explicitly (take_ownership, or --take-ownership), which is recorded", what, declared))
			continue
		}
		took[w.Kind+"\x00"+w.Name] = owner
		writes[i].Comment = clipText(strings.TrimSpace(w.Comment+" [took ownership from "+owner+"]"), 500)
	}
	return took, errors.Join(errs...)
}

// putObjects returns the objects of after that puts describe.
func putObjects(after, puts []engine.ConfigObject) []engine.ConfigObject {
	var out []engine.ConfigObject
	for _, p := range puts {
		for _, o := range after {
			if o.Kind == p.Kind && o.Name == p.Name {
				out = append(out, o)
			}
		}
	}
	return out
}

// NotAppliedError: the change is stored, but this server could not put it
// into effect. It is applied by every server as soon as it can: this one is
// tried again at every poll. The results of the change are still returned.
type NotAppliedError struct {
	Revision int64
	Server   string
	Cause    error
}

func (e *NotAppliedError) Error() string {
	return fmt.Sprintf("the change is stored (revision %d) but server %s could not apply it: %v. It is tried again every few seconds; the servers' state is in the status of GET /api/v1/config",
		e.Revision, e.Server, redactText(e.Cause.Error()))
}

func (e *NotAppliedError) Unwrap() error { return e.Cause }

// applyLocally works out the objects after writes, as the store will (it
// uses the store's own rules, engine.PlanConfigWrite, so a dry run says the
// same).
func applyLocally(objs []engine.ConfigObject, writes []engine.ConfigWrite) ([]engine.ConfigObject, error) {
	out := append([]engine.ConfigObject(nil), objs...)
	for _, w := range writes {
		i := -1
		for j := range out {
			if out[j].Kind == w.Kind && out[j].Name == w.Name {
				i = j
			}
		}
		var cur *engine.ConfigObject
		if i >= 0 {
			cur = &out[i]
		}
		next, _, changed, err := engine.PlanConfigWrite(cur, w)
		if err != nil {
			return nil, err
		}
		switch {
		case !changed:
		case i < 0:
			out = append(out, next)
		default:
			out[i] = next
		}
	}
	return out, nil
}

// ---- history ----

// ---- levels ----
