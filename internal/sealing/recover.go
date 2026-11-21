package sealing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// RecoverReport is what `launchway encryption recover` found and did.
type RecoverReport struct {
	Orphans []keyring.Orphan `json:"orphan_keys"`
	// Sealed counts every value sealed in the database, by column. Which data
	// key sealed a value is not asked: any sealed value makes discarding unsafe.
	Sealed   int            `json:"sealed_values"`
	ByColumn map[string]int `json:"sealed_by_column,omitempty"`
	// Discardable says nothing is sealed, so the orphan keys hold nothing.
	Discardable bool   `json:"discardable"`
	Discarded   bool   `json:"discarded"`
	Message     string `json:"message"`
}

// Recoverable explains, for a database whose key the configured root key cannot open,
// whether `launchway encryption recover` can help. It reads only.
func Recoverable(ctx context.Context, db DB, holder keyring.Holder, previous []keyring.Holder) (RecoverReport, error) {
	var r RecoverReport
	var err error
	if r.Orphans, err = keyring.FindOrphans(ctx, db, holder, previous); err != nil {
		return r, err
	}
	s := &Service{db: db, kdb: locked{db}, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rep, err := s.Report(ctx)
	if err != nil {
		return r, err
	}
	r.ByColumn = map[string]int{}
	for _, c := range rep.Columns {
		if c.Sealed > 0 {
			r.ByColumn[c.Table+"."+c.Column] = c.Sealed
		}
	}
	r.Sealed = rep.Sealed
	r.Discardable = len(r.Orphans) > 0 && r.Sealed == 0
	switch {
	case len(r.Orphans) == 0:
		r.Message = "every key-encryption key in this database opens with the configured root key: there is nothing to recover"
	case r.Discardable:
		r.Message = fmt.Sprintf("%d key-encryption key(s) cannot be opened by the configured root key, and no secret in this database is sealed under any key: they hold nothing and can be discarded; the next start makes a proper one under the configured root key", len(r.Orphans))
	default:
		r.Message = fmt.Sprintf("%d key-encryption key(s) cannot be opened by the configured root key, and %d secret(s) are sealed in this database, so discarding would make them unreadable for good. "+
			"The only ways out: supply the root key this database was set up with (encryption.key_file, or encryption.previous_key_file with `launchway encryption rewrap` after a replacement), or restore the database from a backup made before the wrong key was used", len(r.Orphans), r.Sealed)
	}
	return r, nil
}

// wrongKeyHelp adds to a wrong-key refusal the one fact an operator needs
// when the cause is a key made by mistake: that `encryption recover` applies.
func (s *Service) wrongKeyHelp(ctx context.Context, h keyring.Holder, previous []keyring.Holder, err error) error {
	var wrong *keyring.ErrWrongKey
	if !errors.As(err, &wrong) {
		return err
	}
	r, rerr := Recoverable(ctx, s.db, h, previous)
	if rerr != nil || !r.Discardable {
		return err
	}
	return fmt.Errorf("%w. Nothing is sealed under the key-encryption key this root key cannot open (it was probably made by an earlier start that had no root key, for example an image upgraded under an older chart): "+
		"run `launchway encryption recover` to see it, and `launchway encryption recover --confirm-discard` to discard it; the next start then makes a proper one", err)
}

// Recover discards key-encryption keys the configured root key cannot open,
// when nothing is sealed. It takes the encryption lease so that no server is
// sealing at the same moment, and records an audit event. Without confirm it
// only reports.
func Recover(ctx context.Context, o Options, confirm bool) (RecoverReport, error) {
	if o.Holder == nil {
		return RecoverReport{}, keyring.ErrNoRootKey
	}
	r, err := Recoverable(ctx, o.DB, o.Holder, o.Previous)
	if err != nil || !confirm || !r.Discardable {
		return r, err
	}
	s := &Service{db: o.DB, kdb: locked{o.DB}, now: time.Now, instance: o.Instance, auditFn: o.Audit, log: o.Log}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	got, err := s.acquire(ctx)
	if err != nil {
		return r, err
	}
	if !got {
		return r, errors.New("another server or command holds the encryption lease right now (it is sealing or recovering); wait for it to finish (up to 30 seconds) and run this again")
	}
	defer s.release(ctx)
	// Look again under the lease: what was found must still be true.
	if r, err = Recoverable(ctx, o.DB, o.Holder, o.Previous); err != nil || !r.Discardable {
		return r, err
	}
	var vs []string
	for _, orp := range r.Orphans {
		if err := keyring.DiscardOrphan(ctx, s.kdb, orp.Version); err != nil {
			return r, err
		}
		vs = append(vs, fmt.Sprint(orp.Version))
	}
	for _, k := range []string{"sealed_complete", "migration_completed_at", "migration_started_at", "migration_last_error"} {
		_, _ = s.kdb.Exec(ctx, `DELETE FROM encryption_state WHERE key = ?`, k)
	}
	s.audit(ctx, keyring.Event{Action: "encryption.recovered", Target: "kek/" + strings.Join(vs, ","), Outcome: "ok",
		Detail: "key-encryption key(s) the configured root key could not open were discarded; nothing was sealed under them"})
	r.Discarded = true
	r.Message = "discarded key-encryption key version(s) " + strings.Join(vs, ", ") + ". Restart the servers: the first start with the configured root key makes a proper key and seals the stored secrets"
	return r, nil
}
