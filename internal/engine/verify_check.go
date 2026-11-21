package engine

import (
	"strings"

	"github.com/jk-magic-creations/launchway/internal/verify"
)

// clipAttempts keeps the calls of a health check the report may carry:
// those that started inside the interval (a little slack), at most
// verify.MaxAttempts, with their texts cut.
func clipAttempts(as []verify.Attempt, start, end int64) []verify.Attempt {
	var out []verify.Attempt
	for _, a := range as {
		if a.T < start-60_000 || a.T > end+60_000 {
			continue
		}
		a.LatencyMS = max(a.LatencyMS, 0)
		a.Error = clipText(strings.ToValidUTF8(a.Error, "?"), 300)
		out = append(out, a)
		if len(out) == verify.MaxAttempts {
			break
		}
	}
	return out
}

func clipPoints(ps []verify.Point, start, end int64) []verify.Point {
	var out []verify.Point
	for _, p := range ps {
		if p.T < start-60_000 || p.T > end+60_000 { // a little slack for the source's alignment
			continue
		}
		out = append(out, p)
		if len(out) == verify.MaxPointsPerSeries {
			break
		}
	}
	return out
}

func clipClusters(cs []verify.Cluster) []verify.Cluster {
	if len(cs) > verify.MaxClusters {
		cs = cs[:verify.MaxClusters]
	}
	out := make([]verify.Cluster, 0, len(cs))
	for _, c := range cs {
		c.Template = clipText(c.Template, verify.MaxTemplateBytes)
		c.Count = max(c.Count, 0)
		if len(c.Samples) > verify.MaxSamples {
			c.Samples = c.Samples[:verify.MaxSamples]
		}
		for i := range c.Samples {
			c.Samples[i] = clipText(strings.ToValidUTF8(c.Samples[i], "?"), verify.MaxSampleBytes)
		}
		out = append(out, c)
	}
	return out
}
