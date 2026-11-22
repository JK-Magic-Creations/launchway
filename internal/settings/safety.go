package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Keeping the way back. Two things protect an installation from a stored
// configuration that locks everyone out or cannot be loaded:
//
//   - a change that would leave nobody able to administer everything is
//     refused (checkAdministrable);
//   - a server that cannot load the stored configuration at start-up does not
//     exit: it starts in safe mode with the last configuration that worked,
//     kept in its data directory, or with the bootstrap file alone, says so
//     loudly, and keeps trying the stored one. Offline, an operator on the
//     server host restores an earlier revision (RestoreRevision) or starts
//     with --ignore-database-config.

// checkAdministrable refuses a configuration in which sign-in is on and
// nobody can administer everything: such a change cannot be undone through
// the API, since nobody could make the undoing.
func checkAdministrable(c *config.Config) error {
	if c.Auth.Mode != config.AuthOIDC {
		return nil // without sign-in everyone administers
	}
	p, err := c.Auth.Policy()
	if err != nil {
		return nil // reported by the other checks
	}
	if !p.HasEverywhereAdmin() {
		return errors.New("this change would leave nobody able to administer everything: keep at least one role binding that gives the admin role on every Flight (no Flight list), or name administrators in the bootstrap file's auth.admins")
	}
	return nil
}

// ---- last known good ----

// lastGood is the configuration a server applied last, kept in its data
// directory.
type lastGood struct {
	Revision int64                 `json:"revision"`
	SavedAt  int64                 `json:"saved_at"`
	Objects  []engine.ConfigObject `json:"objects"`
}

func (s *Service) lastGoodPath() string {
	if s.boot == nil || s.boot.Data == "" {
		return ""
	}
	return filepath.Join(s.boot.Data, "config-last-good.json")
}

// saveLastGood keeps the objects of a configuration that was applied.
func (s *Service) saveLastGood(rev int64, objs []engine.ConfigObject) {
	path := s.lastGoodPath()
	if path == "" {
		return
	}
	b, err := json.Marshal(lastGood{Revision: rev, SavedAt: time.Now().UnixMilli(), Objects: objs})
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		s.log.Warn("could not keep the last working configuration", "path", path, "err", err)
	}
}

// Safe says why this server runs in safe mode, if it does.
type Safe struct {
	// Reason is why the stored configuration could not be used.
	Reason string `json:"reason"`
	// Using says what runs instead: "last working configuration, revision N"
	// or "the bootstrap file only".
	Using string `json:"using"`
}

// fallback composes the configuration safe mode runs with.
func (s *Service) fallback() (*composed, string) {
	if path := s.lastGoodPath(); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var lg lastGood
			if json.Unmarshal(b, &lg) == nil {
				if c, lay, err := Compose(s.boot, lg.Objects); err == nil && Check(c, s.eng) == nil {
					return &composed{cfg: c, lay: lay}, fmt.Sprintf("the last working configuration, revision %d", lg.Revision)
				}
			}
		}
	}
	c, lay, _ := Compose(s.boot, nil)
	return &composed{cfg: c, lay: lay}, "the bootstrap file only (no Flights, environments or policies)"
}

// StartConfig returns the configuration a starting server runs with: the
// stored one, or, if that cannot be used, the safe-mode configuration,
// with the reason logged and kept for the status. The file's own settings
// are imported first, once.
func (s *Service) StartConfig() (*config.Config, error) {
	rev, objs, err := s.eng.ConfigSnapshot()
	if err != nil {
		return nil, err
	}
	cp, err := s.composeChecked(objs)
	if err == nil {
		err = checkAdministrable(cp.cfg)
	}
	if err == nil {
		return cp.cfg, nil
	}
	fb, using := s.fallback()
	s.mu.Lock()
	s.safe = &Safe{Reason: fmt.Sprintf("revision %d: %v", rev, err), Using: using}
	s.mu.Unlock()
	s.log.Error("SAFE MODE: the configuration stored in the database cannot be used, so this server starts with "+using+
		". Fix the configuration (web UI, API or launchway config) and the server picks it up; or restore an earlier revision on this host with launchway config restore --offline --revision N; or start with --ignore-database-config",
		"revision", rev, "err", err)
	return fb.cfg, nil
}

// SafeMode reports whether this server runs in safe mode.
func (s *Service) SafeMode() *Safe {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.safe
}

// RestoreRevision puts the objects back as they were at revision rev, as a
// new revision (the history keeps everything), by an operator on the server
// host who cannot use the API. It does not check the result against the
// running configuration: it is the way out of one that cannot be used.
func RestoreRevision(eng *engine.Engine, rev int64, actor string) (newRev int64, changes int, err error) {
	want, err := eng.ConfigAtRevision(rev)
	if err != nil {
		return 0, 0, err
	}
	for attempt := 0; attempt < 10; attempt++ {
		cur, objs, err := eng.ConfigSnapshot()
		if err != nil {
			return 0, 0, err
		}
		have := map[string]engine.ConfigObject{}
		for _, o := range objs {
			have[o.Kind+"\x00"+o.Name] = o
		}
		var writes []engine.ConfigWrite
		seen := map[string]bool{}
		for _, w := range want {
			key := w.Kind + "\x00" + w.Name
			seen[key] = true
			h, ok := have[key]
			switch {
			case w.Deleted() && (!ok || h.Deleted()):
			case w.Deleted():
				writes = append(writes, engine.ConfigWrite{Kind: w.Kind, Name: w.Name, Action: engine.ConfigDelete, IfVersion: -1, ManagedBy: engine.ManagedByAPI,
					Comment: fmt.Sprintf("offline restore of revision %d", rev)})
			default:
				writes = append(writes, engine.ConfigWrite{Kind: w.Kind, Name: w.Name, Action: engine.ConfigUpdate, Unit: w.Unit, Spec: w.Spec, IfVersion: -1,
					ManagedBy: engine.ManagedByAPI, Comment: fmt.Sprintf("offline restore of revision %d", rev)})
			}
		}
		for _, h := range objs {
			if !seen[h.Kind+"\x00"+h.Name] && !h.Deleted() {
				writes = append(writes, engine.ConfigWrite{Kind: h.Kind, Name: h.Name, Action: engine.ConfigDelete, IfVersion: -1, ManagedBy: engine.ManagedByAPI,
					Comment: fmt.Sprintf("offline restore of revision %d", rev)})
			}
		}
		if len(writes) == 0 {
			return cur, 0, nil
		}
		next, recorded, err := eng.WriteConfig(cur, actor, writes, nil)
		if errors.Is(err, engine.ErrConfigMoved) {
			continue
		}
		return next, len(recorded), err
	}
	return 0, 0, errors.New("other servers kept changing the configuration; try again")
}

// warnUnknown logs objects of kinds this binary does not know: a newer
// binary wrote them. They are ignored, not fatal, so that a rollback of the
// binary does not stop the server.
func warnUnknown(log *slog.Logger, objs []engine.ConfigObject) {
	seen := map[string]bool{}
	for _, o := range objs {
		if kindByName(o.Kind) == nil && !o.Deleted() && !seen[o.Kind] {
			seen[o.Kind] = true
			log.Warn("the stored configuration has objects of a kind this version does not know (written by a newer version); they are ignored", "kind", o.Kind)
		}
	}
}
