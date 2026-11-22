package stepkit

// ContractVersion is the version of the step contract this build speaks.
// A manifest names the version it was written for; a Station refuses a
// version it does not know and accepts every older one it still supports.
const ContractVersion = 1

// MinContract is the oldest contract version still accepted.
const MinContract = 1

// The variables of the contract.
const (
	EnvContract  = "LAUNCHWAY_STEP_CONTRACT"
	EnvName      = "LAUNCHWAY_STEP_NAME"
	EnvDir       = "LAUNCHWAY_STEP_DIR"
	EnvWorkspace = "LAUNCHWAY_WORKSPACE"
	EnvInputs    = "LAUNCHWAY_STEP_INPUTS"
	EnvOutputs   = "LAUNCHWAY_STEP_OUTPUTS"
	EnvResults   = "LAUNCHWAY_STEP_RESULTS"
	EnvArtifacts = "LAUNCHWAY_STEP_ARTIFACTS"
	EnvReports   = "LAUNCHWAY_STEP_REPORTS"
	// EnvWithPrefix starts the variable of a scalar input, LAUNCHWAY_WITH_<NAME>.
	EnvWithPrefix = "LAUNCHWAY_WITH_"
)

// IODir is where a step's contract files are, relative to the workspace.
const IODir = ".launchway/step-io"

// The names inside IODir.
const (
	InputsFile   = "inputs.json"
	OutputsFile  = "outputs.json"
	ResultsFile  = "results.ndjson"
	ArtifactsDir = "artifacts"
	ReportsDir   = "reports"
	ArtifactName = "step"
)

// Exit codes with a meaning.
const (
	ExitPassed        = 0
	ExitFailed        = 1
	ExitFailedAllowed = 64
	ExitNeedsPerson   = 65
)

// Limits that keep a hostile or broken step from filling the server.
const (
	MaxInputsBytes   = 256 << 10
	MaxOutputsBytes  = 64 << 10
	MaxResultsBytes  = 4 << 20
	MaxResultLine    = 64 << 10
	MaxResults       = 5000
	MaxOutputValue   = 4096
	MaxSummaryBytes  = 64 << 10
	MaxManifestBytes = 64 << 10
)

// Outcome says how a step's exit code counts.
type Outcome int

const (
	OutcomePassed Outcome = iota
	OutcomeFailed
	OutcomeAllowed
	OutcomeNeedsPerson
	OutcomeCrashed
)

// OutcomeOf gives the meaning of an exit code.
func OutcomeOf(code int) Outcome {
	switch {
	case code == ExitPassed:
		return OutcomePassed
	case code == ExitFailedAllowed:
		return OutcomeAllowed
	case code == ExitNeedsPerson:
		return OutcomeNeedsPerson
	case code > ExitNeedsPerson || code < 0:
		return OutcomeCrashed
	default:
		return OutcomeFailed
	}
}

func (o Outcome) String() string {
	return [...]string{"passed", "failed", "failed but allowed", "needs a person", "crashed"}[o]
}
