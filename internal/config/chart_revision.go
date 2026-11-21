package config

import (
	"fmt"
	"strconv"
	"strings"
)

// EnvChartRevision is set by the Helm chart on every server pod to the
// chart's own revision (the annotation launchway.io/chart-revision in
// Chart.yaml).
const EnvChartRevision = "LAUNCHWAY_CHART_REVISION"

// ChartRevision is the revision of the chart this binary expects: the chart
// gives the server things (a mounted root key, environment, network
// policies) that a newer binary may rely on. It moves with the chart's
// annotation; TestChartRevisionMatchesTheChart keeps the two equal.
const ChartRevision = 2

// ChartNotice says what the server should print about the chart that
// rendered it: a plain line and whether it is a warning. A server that is not
// run by the chart (no variable at all and not in Kubernetes) says nothing.
func ChartNotice(getenv func(string) string, inKubernetes bool) (msg string, warn bool) {
	v := strings.TrimSpace(getenv(EnvChartRevision))
	if v == "" {
		if !inKubernetes {
			return "", false
		}
		return fmt.Sprintf("this server expects chart revision %d, but the chart that rendered it did not say which it is (%s is not set): it was rendered by an older chart, "+
			"so encryption and network settings from the new chart are not applied yet. Image and chart must move together: deploy the chart that belongs to this image (see the upgrade guide)",
			ChartRevision, EnvChartRevision), true
	}
	n, err := strconv.Atoi(v)
	switch {
	case err != nil:
		return fmt.Sprintf("%s is %q, which is not a number; ignoring it", EnvChartRevision, v), true
	case n < ChartRevision:
		return fmt.Sprintf("this server was rendered by chart revision %d but expects %d: it was rendered by an older chart, so encryption and network settings from the new chart are not applied yet. "+
			"Image and chart must move together: deploy the chart that belongs to this image (see the upgrade guide)", n, ChartRevision), true
	case n > ChartRevision:
		return fmt.Sprintf("this server was rendered by chart revision %d, newer than the %d it knows; that is fine (the chart is meant to work with the previous image)", n, ChartRevision), false
	}
	return fmt.Sprintf("rendered by chart revision %d, as this server expects", n), false
}
