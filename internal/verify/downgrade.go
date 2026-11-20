package verify

import (
	"fmt"
	"strings"
)

// Downgrade applies a model's opinion to a verdict, when the pipeline lets
// a model take part in the decision. A model can only make a verdict
// worse: a pass may become inconclusive or failed, and an inconclusive
// verdict may become failed. A failure stays a failure, nothing ever
// becomes a pass, and a verdict still running is left alone. When the
// model changes the outcome, the summary says which model changed it, from
// what, and why; otherwise the verdict is returned as it was.
func Downgrade(v Verdict, model, modelOutcome, reason string) Verdict {
	allowed := false
	switch v.Outcome {
	case Passed:
		allowed = modelOutcome == Inconclusive || modelOutcome == Failed
	case Inconclusive:
		allowed = modelOutcome == Failed
	}
	if !allowed {
		return v
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "the model"
	} else {
		model = "model " + model
	}
	reason = sentence(clip(strings.TrimSpace(reason), MaxErrorBytes))
	if reason == "" {
		reason = "It gave no reason."
	}
	was := v.Outcome
	v.Outcome = modelOutcome
	v.Summary = strings.TrimSpace(fmt.Sprintf("%s The statistics said %s; %s changed it to %s: %s", sentence(v.Summary), was, model, modelOutcome, reason))
	return v
}
