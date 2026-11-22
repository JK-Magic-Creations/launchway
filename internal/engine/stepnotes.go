package engine

// What a custom step says besides its outputs (see internal/stepkit): a
// summary in Markdown, and notes anchored to a line of a file. They are kept
// with the attempt that wrote them (schema 68).

// FeatureCustomSteps is what a runner that speaks the step contract says it
// has. Steps that came from custom steps are given only to such runners.
const FeatureCustomSteps = "custom-steps"

// Kinds of StepNote.
const (
	NoteSummary    = "summary"
	NoteAnnotation = "annotation"
)

// Limits on what is kept per attempt.
const (
	MaxStepNotes     = 500
	MaxStepNoteBytes = 128 << 10
)

// StepNote is one summary or annotation of a step attempt.
type StepNote struct {
	Kind  string `json:"kind"`
	Level string `json:"level,omitempty"` // annotations: note, warning or error
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
	// Text is the message of an annotation, or the Markdown of a summary.
	// The runner has masked secret values in it; the web page shows
	// Markdown through the product's sanitizing renderer only.
	Text string `json:"text"`
}
