package pipeline

// What a step's when: { upstream: … } may say.
const (
	UpstreamSucceeded = "succeeded"
	UpstreamFailed    = "failed"
	UpstreamAlways    = "always"
)

// Upstreams lists them, in the order they are documented.
var Upstreams = []string{UpstreamSucceeded, UpstreamFailed, UpstreamAlways}

// UpstreamRule is what a condition says about the steps a step needs:
// UpstreamSucceeded when it says nothing.
func (c *Condition) UpstreamRule() string {
	if c == nil || c.Upstream == "" {
		return UpstreamSucceeded
	}
	return c.Upstream
}
