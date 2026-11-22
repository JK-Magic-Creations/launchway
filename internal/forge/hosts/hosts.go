// Package hosts is the connection to a Git host Launchway does not host: the
// service behind "keep your code on another host". A connection holds a
// narrow credential by reference; a Flight (a project) is attached to one
// repository on it; and through the connection Launchway checks out, reports
// commit statuses and a pull request summary, cuts tags and releases, and
// commits files. Every write is audited by the caller with the connection and
// the acting person or run. The calls themselves are the provider's
// (internal/forge/providers/github); this package owns the rows, the
// allow-list, the webhook intake and the per-Flight switches.
package hosts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/github"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	engstore "github.com/jk-magic-creations/launchway/internal/store"
)

// Kinds a connection of this service can be.
var Kinds = []forge.Kind{forge.KindGitHub}

// ErrNotAttached is wrapped (with forge.ErrNotFound) when a project has no outside repository.
var ErrNotAttached = errors.New("not attached")

// FlightStore keeps which project is attached to which repository, the
// report switches and the summary comment keys (the engine implements it).
type FlightStore interface {
	PutOutsideFlight(project, connection, repo string) error
	DeleteOutsideFlight(project string) (bool, error)
	SetOutsideReports(project string, statuses, comment bool) error
	OutsideFlightOf(project string) (engstore.FlightRow, bool, error)
	OutsideFlightsOfRepository(connection, repo string) ([]engstore.FlightRow, error)
	OutsideFlightsOfConnection(connection string) ([]engstore.FlightRow, error)
	OutsideSummaryComment(project string, number int) (string, error)
	PutOutsideSummaryComment(project string, number int, key string) error
}

// Service is the connections and the Flights attached to them.
type Service struct {
	// PublicURL is the address people open Launchway at; links in reports use it.
	PublicURL string
	st        *store.Store
	fl        FlightStore
	guard     *egress.Guard
	now       func() time.Time
}

// New makes the service. The guard is the installation's egress allow-list
// (forge.mirror_allow): a connection whose host is internal is refused unless
// it is listed there.
func New(db store.DB, fl FlightStore, g *egress.Guard) *Service {
	if g == nil {
		g, _ = egress.New(nil)
	}
	return &Service{st: store.New(db), fl: fl, guard: g, now: time.Now}
}

// SetClock replaces the clock; for tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now; s.st.SetClock(now) }

// Store returns the underlying store (delivery de-duplication).
func (s *Service) Store() *store.Store { return s.st }

// View is a connection as the API shows it: never the credential, only where it is read from.
type View struct {
	Name          string             `json:"name"`
	Kind          string             `json:"kind"`
	BaseURL       string             `json:"base_url,omitempty"`
	UploadURL     string             `json:"upload_url,omitempty"`
	Credential    string             `json:"credential"`
	WebhookSecret string             `json:"webhook_secret,omitempty"`
	AuthKind      string             `json:"auth_kind"`
	Owners        []string           `json:"owners,omitempty"`
	Enabled       bool               `json:"enabled"`
	Capabilities  forge.Capabilities `json:"capabilities"`
}

func view(c forge.Connection) View {
	v := View{Name: c.Name, Kind: string(c.Kind), BaseURL: c.BaseURL, UploadURL: c.UploadURL, Credential: c.Credential.String(),
		WebhookSecret: c.WebhookSecret.String(), AuthKind: c.AuthKind, Owners: c.Owners, Enabled: c.Enabled}
	if v.AuthKind == "" {
		v.AuthKind = "token"
	}
	if c.Kind == forge.KindGitHub {
		v.Capabilities = github.Capabilities()
	}
	return v
}

// AddInput is a request to connect a host.
type AddInput struct {
	Name          string          `json:"name"`
	Kind          forge.Kind      `json:"kind"`
	BaseURL       string          `json:"base_url,omitempty"`
	UploadURL     string          `json:"upload_url,omitempty"`
	Credential    forge.SecretRef `json:"credential"`
	WebhookSecret forge.SecretRef `json:"webhook_secret,omitzero"`
	Owners        []string        `json:"owners,omitempty"`
}

// Add records a connection. The credential is a reference that must resolve
// now: a connection that cannot read its own token is refused with a message
// that names the reference, never the value.
func (s *Service) Add(ctx context.Context, in AddInput, by string) (View, error) {
	ok := false
	for _, k := range Kinds {
		ok = ok || k == in.Kind
	}
	if !ok {
		return View{}, fmt.Errorf("%w: a connection to keep code on is of kind github, not %q", forge.ErrInvalid, in.Kind)
	}
	if in.Credential.IsZero() {
		return View{}, fmt.Errorf("%w: connection: a credential reference is required (credential.env, credential.file or sealed:NAME)", forge.ErrInvalid)
	}
	c := forge.Connection{Name: in.Name, Kind: in.Kind, BaseURL: in.BaseURL, UploadURL: in.UploadURL, Credential: in.Credential,
		WebhookSecret: in.WebhookSecret, Owners: in.Owners, AuthKind: "token", Events: forge.EventsWebhook, Enabled: true}
	if err := c.Validate(); err != nil {
		return View{}, err
	}
	ep, err := github.EndpointsOf(c.BaseURL, c.UploadURL)
	if err != nil {
		return View{}, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
	}
	for _, u := range []string{ep.API.String(), ep.Upload.String()} {
		if err := s.guard.Check(ctx, u); err != nil {
			return View{}, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
		}
	}
	if _, err := in.Credential.Resolve(); err != nil {
		return View{}, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
	}
	made, err := s.st.CreateConnection(ctx, c, by)
	if err != nil {
		return View{}, err
	}
	return view(made), nil
}

func isOurs(c forge.Connection) bool {
	for _, k := range Kinds {
		if c.Kind == k {
			return true
		}
	}
	return false
}

// List lists the connections of this service.
func (s *Service) List(ctx context.Context) ([]View, error) {
	all, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	out := []View{}
	for _, c := range all {
		if isOurs(c) {
			out = append(out, view(c))
		}
	}
	return out, nil
}

func (s *Service) conn(ctx context.Context, name string) (forge.Connection, error) {
	c, err := s.st.ConnectionByName(ctx, name)
	if err != nil {
		return c, err
	}
	if !isOurs(c) {
		return c, fmt.Errorf("%w: connection %q", forge.ErrNotFound, name)
	}
	return c, nil
}

// Get returns one connection.
func (s *Service) Get(ctx context.Context, name string) (View, error) {
	c, err := s.conn(ctx, name)
	if err != nil {
		return View{}, err
	}
	return view(c), nil
}

// Remove deletes a connection that no Flight uses.
func (s *Service) Remove(ctx context.Context, name string) (View, error) {
	c, err := s.conn(ctx, name)
	if err != nil {
		return View{}, err
	}
	used, err := s.fl.OutsideFlightsOfConnection(c.ID)
	if err != nil {
		return View{}, err
	}
	if len(used) > 0 {
		return View{}, fmt.Errorf("%w: %d Flights keep their code on %s; detach them first", forge.ErrInvalid, len(used), name)
	}
	return view(c), s.st.DeleteConnection(ctx, c.ID)
}

// Provider opens the provider of a connection.
func (s *Service) Provider(ctx context.Context, name string) (*github.Provider, forge.Connection, error) {
	c, err := s.conn(ctx, name)
	if err != nil {
		return nil, c, err
	}
	if !c.Enabled {
		return nil, c, fmt.Errorf("%w: the connection %s is turned off", forge.ErrInvalid, name)
	}
	p, err := github.New(c, s.guard)
	return p, c, err
}

// TestResult is what "test connection" answers.
type TestResult struct {
	Connection   string             `json:"connection"`
	Report       github.Report      `json:"report"`
	Capabilities forge.Capabilities `json:"capabilities"`
}

// Test asks the host who the credential is and which permissions it has.
func (s *Service) Test(ctx context.Context, name string) (TestResult, error) {
	p, _, err := s.Provider(ctx, name)
	if err != nil {
		return TestResult{}, err
	}
	rep, err := p.Probe(ctx)
	if err != nil {
		return TestResult{}, err
	}
	return TestResult{Connection: name, Report: rep, Capabilities: p.Capabilities()}, nil
}

// Repositories lists what the connection reaches, narrowed by a search text
// that is matched against the repository's path.
func (s *Service) Repositories(ctx context.Context, name, cursor, q string) (forge.Page[forge.Repository], error) {
	p, _, err := s.Provider(ctx, name)
	if err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	page, err := p.Repositories().List(ctx, cursor)
	if err != nil || q == "" {
		return page, err
	}
	q = strings.ToLower(q)
	kept := page.Items[:0]
	for _, r := range page.Items {
		if strings.Contains(strings.ToLower(r.Path), q) {
			kept = append(kept, r)
		}
	}
	page.Items = kept
	return page, nil
}

// Flight is a project attached to a repository on a connection.
type Flight struct {
	Project        string `json:"project"`
	Connection     string `json:"connection"`
	Repository     string `json:"repository"`
	ReportStatuses bool   `json:"report_statuses"`
	ReportComment  bool   `json:"report_comment"`
	// connID is the connection's ID.
	connID string
}

// Attach ties a project to a repository on a connection. The repository must
// be visible to the credential and its owner on the allow-list.
func (s *Service) Attach(ctx context.Context, project, connection, repoPath string) (Flight, error) {
	if project == "" {
		return Flight{}, fmt.Errorf("%w: a project is required", forge.ErrInvalid)
	}
	p, c, err := s.Provider(ctx, connection)
	if err != nil {
		return Flight{}, err
	}
	repo, err := p.Repositories().Get(ctx, repoPath)
	if err != nil {
		return Flight{}, err
	}
	if err := s.fl.PutOutsideFlight(project, c.ID, repo.Path); err != nil {
		return Flight{}, err
	}
	return s.Flight(ctx, project)
}

// Detach removes the attachment; the project goes back to its Git address alone.
func (s *Service) Detach(ctx context.Context, project string) error {
	ok, err := s.fl.DeleteOutsideFlight(project)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %w: project %s is not attached to an outside repository", forge.ErrNotFound, ErrNotAttached, project)
	}
	return nil
}

func (s *Service) flightOf(row engstore.FlightRow) (Flight, error) {
	c, err := s.st.Connection(context.Background(), row.ConnectionID)
	if err != nil {
		return Flight{}, err
	}
	return Flight{Project: row.Project, Connection: c.Name, Repository: row.Repository, ReportStatuses: row.ReportStatuses, ReportComment: row.ReportComment, connID: row.ConnectionID}, nil
}

// Flight returns the attachment of a project; ErrNotFound when it has none
// (its repository is hosted here, or is a plain Git address).
func (s *Service) Flight(ctx context.Context, project string) (Flight, error) {
	row, ok, err := s.fl.OutsideFlightOf(project)
	if err != nil {
		return Flight{}, err
	}
	if !ok {
		return Flight{}, fmt.Errorf("%w: %w: project %s is not attached to an outside repository", forge.ErrNotFound, ErrNotAttached, project)
	}
	return s.flightOf(row)
}

// flightsOf lists the projects attached to a repository of a connection.
func (s *Service) flightsOf(ctx context.Context, connID, repoPath string) ([]Flight, error) {
	rows, err := s.fl.OutsideFlightsOfRepository(connID, repoPath)
	if err != nil {
		return nil, err
	}
	var out []Flight
	for _, r := range rows {
		f, err := s.flightOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// SetReports switches the commit statuses and the pull request summary comment
// of a Flight; nil leaves one as it is.
func (s *Service) SetReports(ctx context.Context, project string, statuses, comment *bool) (Flight, error) {
	f, err := s.Flight(ctx, project)
	if err != nil {
		return f, err
	}
	if statuses != nil {
		f.ReportStatuses = *statuses
	}
	if comment != nil {
		f.ReportComment = *comment
	}
	if err := s.fl.SetOutsideReports(project, f.ReportStatuses, f.ReportComment); err != nil {
		return f, err
	}
	return f, nil
}

// FlightView is a Flight with what its host cannot do, in the host's own
// declared words, so a page says so plainly.
type FlightView struct {
	Flight
	Unsupported map[forge.Capability]forge.Declaration `json:"unsupported,omitempty"`
}

// View returns the attachment of a project with the features its host lacks.
func (s *Service) View(ctx context.Context, project string) (FlightView, error) {
	f, err := s.Flight(ctx, project)
	if err != nil {
		return FlightView{}, err
	}
	v := FlightView{Flight: f, Unsupported: map[forge.Capability]forge.Declaration{}}
	for k, d := range github.Capabilities() {
		if d.Support != forge.Native {
			v.Unsupported[k] = d
		}
	}
	return v, nil
}

// repoOf finds the repository of a Flight on its connection.
func (s *Service) repoOf(ctx context.Context, project string) (*github.Provider, Flight, forge.Repository, error) {
	f, err := s.Flight(ctx, project)
	if err != nil {
		return nil, f, forge.Repository{}, err
	}
	p, _, err := s.Provider(ctx, f.Connection)
	if err != nil {
		return nil, f, forge.Repository{}, err
	}
	repo, err := p.Repositories().Get(ctx, f.Repository)
	if err != nil {
		return nil, f, repo, err
	}
	return p, f, repo, nil
}
