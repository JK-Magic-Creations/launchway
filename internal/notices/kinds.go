package notices

// The kinds of notice.
const (
	ReviewRequested    = "review_requested"
	ReviewSubmitted    = "review_submitted"
	Comment            = "comment"
	Mention            = "mention"
	PullMerged         = "pull_merged"
	PullClosed         = "pull_closed"
	RunFailed          = "run_failed"
	RunFixed           = "run_fixed"
	ApprovalWaiting    = "approval_waiting"
	DeployFinished     = "deploy_finished"
	DeployRolledBack   = "deploy_rolled_back"
	ReleasePublished   = "release_published"
	CredentialExpiring = "credential_expiring"
)

// Channels a person may choose per kind.
const (
	InApp = "in_app"
	Email = "email"
	Off   = "off"
)

// Watch levels for a repository or a Flight.
const (
	WatchAll           = "all"
	WatchParticipating = "participating"
	WatchNone          = "none"
)

// Class says who a kind goes to.
type Class int

const (
	// Ask goes to the person named: something waits on them.
	Ask Class = iota
	// Mine goes to the person whose work it is.
	Mine
	// Activity goes to the people taking part and to those who watch everything.
	Activity
)

// KindInfo describes a kind.
type KindInfo struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Default is the channel when the person has not chosen.
	Default string `json:"default"`
	Class   Class  `json:"-"`
	// Waiting kinds are things waiting on the person: they count in the
	// shell's "waiting on you" and come by e-mail by default.
	Waiting bool `json:"waiting_on_you"`
}

// Kinds lists every kind, in the order a person sees them.
var Kinds = []KindInfo{
	{ReviewRequested, "A review is asked of you", Email, Ask, true},
	{Mention, "You are mentioned in a pull request or work item", Email, Ask, true},
	{ApprovalWaiting, "An approval is waiting for you", Email, Ask, true},
	{CredentialExpiring, "A credential of yours expires soon", Email, Mine, true},
	{ReviewSubmitted, "A review is submitted on your pull request", InApp, Activity, false},
	{Comment, "A comment on a pull request or work item you take part in", InApp, Activity, false},
	{PullMerged, "A pull request you take part in is merged", InApp, Activity, false},
	{PullClosed, "A pull request you take part in is closed", InApp, Activity, false},
	{RunFailed, "A run of yours failed", InApp, Mine, false},
	{RunFixed, "A run of yours that failed is fixed", InApp, Mine, false},
	{DeployFinished, "A deployment you approved finished", InApp, Mine, false},
	{DeployRolledBack, "A deployment you approved was rolled back", InApp, Mine, false},
	{ReleasePublished, "A release is published in a Flight you watch", InApp, Activity, false},
}

// Info returns a kind's description.
func Info(kind string) (KindInfo, bool) {
	for _, k := range Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return KindInfo{}, false
}

// phrase is what a message says happened, without any content of the thing.
func phrase(kind string) string {
	switch kind {
	case ReviewRequested:
		return "review requested"
	case ReviewSubmitted:
		return "review submitted"
	case Comment:
		return "new comment"
	case Mention:
		return "you were mentioned"
	case PullMerged:
		return "merged"
	case PullClosed:
		return "closed"
	case RunFailed:
		return "run failed"
	case RunFixed:
		return "run fixed"
	case ApprovalWaiting:
		return "approval waiting"
	case DeployFinished:
		return "deployment finished"
	case DeployRolledBack:
		return "deployment rolled back"
	case ReleasePublished:
		return "release published"
	case CredentialExpiring:
		return "credential expires soon"
	}
	return kind
}
