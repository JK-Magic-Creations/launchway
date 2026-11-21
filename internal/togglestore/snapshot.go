package togglestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// Build returns the ruleset of a project's environment: every toggle the
// project sees (the nearest level that defines a key wins; a lock at a wider
// level keeps it), each with its state in the environment, and the segments.
// An environment where a toggle was never set has it off, serving its default.
func (s *Service) Build(ctx context.Context, project, env string) (*toggles.Ruleset, error) {
	defs, err := listDefs(ctx, s.d.DB, s.d.UnitOf(project), project)
	if err != nil {
		return nil, err
	}
	chosen := map[string]Definition{}
	for _, d := range defs {
		cur, ok := chosen[d.Key.Key]
		if ok && cur.Lock != nil {
			continue
		}
		if !ok || levelRank(d.Level) > levelRank(cur.Level) {
			chosen[d.Key.Key] = d
		}
	}
	rs := &toggles.Ruleset{Schema: toggles.SchemaVersion, Environment: env, Toggles: map[string]toggles.Flag{}, Segments: map[string]toggles.Segment{}}
	for k, d := range chosen {
		d := d
		st := offState(&d)
		if e, err := getEnv(ctx, s.d.DB, d.Key, env); err != nil {
			return nil, err
		} else if e != nil {
			st = e.State
		}
		rs.Toggles[k] = toggles.Flag{Key: k, Type: d.Type, Kind: d.Kind, ClientVisible: d.ClientVisible, Salt: d.Salt, State: st}
	}
	segs, err := s.Segments(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, sg := range segs {
		rs.Segments[sg.Name] = sg.Segment
	}
	return rs, nil
}

// Snapshot is an environment's ruleset, built once and kept in memory until
// something changes: what applications fetch, with its ETag.
type Snapshot struct {
	Ruleset *toggles.Ruleset
	Body    []byte
	ETag    string
	Rev     int64
}

func snapKey(project, env string) string { return project + "\x00" + env }

// revision returns the newest revision this server knows, looking at the
// database at most once per CheckEvery however many requests come.
func (s *Service) revision(ctx context.Context) (int64, error) {
	s.revMu.Lock()
	defer s.revMu.Unlock()
	if !s.revAt.IsZero() && s.d.Now().Sub(s.revAt) < s.d.CheckEvery {
		return s.rev, nil
	}
	return s.readRevLocked(ctx)
}

func (s *Service) readRevLocked(ctx context.Context) (int64, error) {
	rev, err := currentRev(ctx, s.d.DB)
	if err != nil {
		return s.rev, err
	}
	s.revAt = s.d.Now()
	if rev != s.rev {
		s.rev = rev
		s.keyCache.clear()
		close(s.change)
		s.change = make(chan struct{})
	}
	return s.rev, nil
}

// Poll looks at the revision now. Servers call it on a timer (Run) so that
// streams hear about a change another server made; Current also calls it
// lazily, so a server that is not polling is behind by at most CheckEvery.
func (s *Service) Poll(ctx context.Context) error {
	s.revMu.Lock()
	defer s.revMu.Unlock()
	_, err := s.readRevLocked(ctx)
	return err
}

// noticeLocal is called after this server wrote something: the revision is
// read at once, so that its own snapshots and streams change without waiting.
func (s *Service) noticeLocal(ctx context.Context) {
	_ = s.Poll(ctx)
	s.keyCache.clear()
}

// Changed returns a channel that is closed when the revision moves; ask again
// after it fires.
func (s *Service) Changed() <-chan struct{} {
	s.revMu.Lock()
	defer s.revMu.Unlock()
	return s.change
}

// Run polls the revision until ctx ends and flushes evaluation counts: start
// it once per server.
func (s *Service) Run(ctx context.Context) {
	poll := time.NewTicker(s.d.CheckEvery)
	flush := time.NewTicker(30 * time.Second)
	sched := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer flush.Stop()
	defer sched.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.Flush(context.WithoutCancel(ctx))
			return
		case <-poll.C:
			_ = s.Poll(ctx)
		case <-flush.C:
			_ = s.Flush(ctx)
		case <-sched.C:
			_, _ = s.RunDue(ctx)
		}
	}
}

var snapBuild sync.Mutex

// Current returns the snapshot of an environment, rebuilding it from the
// database only when the revision has moved since it was built: a request
// that finds it current touches no database.
func (s *Service) Current(ctx context.Context, project, env string) (*Snapshot, error) {
	rev, err := s.revision(ctx)
	if err != nil {
		return nil, err
	}
	k := snapKey(project, env)
	s.snapMu.Lock()
	sn := s.snaps[k]
	s.snapMu.Unlock()
	if sn != nil && sn.Rev == rev {
		return sn, nil
	}
	snapBuild.Lock() // one rebuild at a time per server; a burst after a change builds once
	defer snapBuild.Unlock()
	s.snapMu.Lock()
	sn = s.snaps[k]
	s.snapMu.Unlock()
	if sn != nil && sn.Rev == rev {
		return sn, nil
	}
	rs, err := s.Build(ctx, project, env)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(rs)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	sn = &Snapshot{Ruleset: rs, Body: body, ETag: fmt.Sprintf(`"%s"`, hex.EncodeToString(sum[:8])), Rev: rev}
	s.snapMu.Lock()
	s.snaps[k] = sn
	s.snapMu.Unlock()
	return sn, nil
}

// Evaluated is one toggle's value for a context.
type Evaluated struct {
	Value  any    `json:"value"`
	Reason string `json:"reason"`
	RuleID string `json:"rule_id,omitempty"`
}

// Evaluate answers for a context, from the snapshot, for the toggles named
// (all of them when none are), counting each evaluation. With visibleOnly it
// leaves out toggles that are not visible to client applications, and says
// nothing about their existence.
func (s *Service) Evaluate(ctx context.Context, project, env string, c toggles.Context, names []string, visibleOnly bool) (map[string]Evaluated, error) {
	sn, err := s.Current(ctx, project, env)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		for k, f := range sn.Ruleset.Toggles {
			if !visibleOnly || f.ClientVisible {
				names = append(names, k)
			}
		}
	}
	out := make(map[string]Evaluated, len(names))
	for _, n := range names {
		f, ok := sn.Ruleset.Toggles[n]
		if !ok || (visibleOnly && !f.ClientVisible) {
			out[n] = Evaluated{Reason: toggles.ReasonNotFound}
			continue
		}
		r := toggles.Evaluate(sn.Ruleset, n, c)
		out[n] = Evaluated{Value: r.Value, Reason: r.Reason, RuleID: r.RuleID}
		s.Count(project, env, n, r.Value, r.Reason, 1)
	}
	return out, nil
}
