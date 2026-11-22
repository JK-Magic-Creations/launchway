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

// EnvIgnoredValues is set by the chart (comma-separated dotted keys) when the values file held keys
// this chart version does not know.
const EnvIgnoredValues = "LAUNCHWAY_IGNORED_VALUES"

// IgnoredValuesNotice says which values the chart ignored, as one plain sentence; empty when none.
func IgnoredValuesNotice(getenv func(string) string) string {
	var keys []string
	for _, k := range strings.Split(getenv(EnvIgnoredValues), ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	noun := "values were"
	if len(keys) == 1 {
		noun = "value was"
	}
	return fmt.Sprintf("%d %s ignored by this chart version: %s: a typo, or a setting for a newer chart. They change nothing here; fix the name or remove them (strictValues: true makes the chart refuse them)",
		len(keys), noun, strings.Join(keys, ", "))
}

// EnvNetworkPolicy is set to "chart" by the chart when it renders its NetworkPolicies.
const EnvNetworkPolicy = "LAUNCHWAY_NETWORK_POLICY"

// NetworkPolicyNotice is what the server says at start when the chart's NetworkPolicies are on. A
// server cannot list the cluster's policy objects (it has no cluster rights), so it cannot say
// whether another system also holds policy; it names the check that can.
func NetworkPolicyNotice(getenv func(string) string) string {
	if getenv(EnvNetworkPolicy) != "chart" {
		return ""
	}
	return "the chart's NetworkPolicies are on for this installation. If the cluster's network plugin has its own policy objects " +
		"(CiliumNetworkPolicy, GlobalNetworkPolicy, ...) that add to Kubernetes ones, let ONE system be the authority: " +
		"run `launchway doctor network --namespace <namespace>` where you installed from, and set networkPolicy.enabled: false if they overlap"
}
