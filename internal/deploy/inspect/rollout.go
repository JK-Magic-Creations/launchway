package inspect

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// rollout builds the progressive delivery's view from the first Rollout
// (or, failing that, Deployment) of the tree.
func (bd *build) rollout(_ context.Context) {
	var root *Node
	for _, id := range sortedKeys(bd.nodes) {
		n := bd.nodes[id]
		if n.Parent == "" && n.Change != ChangeLeftover && (n.Kind == "Rollout" || n.Kind == "Deployment" && root == nil) {
			if root == nil || n.Kind == "Rollout" {
				root = n
			}
		}
	}
	if root == nil || bd.objs[root.ID] == nil {
		return
	}
	o := bd.objs[root.ID]
	v := &RolloutView{Name: root.Name, Namespace: root.Namespace, Kind: root.Kind, Phase: digS(o, "status", "phase"), Message: root.Message,
		Current: -1, DesiredWeight: -1, ActualWeight: -1}
	if root.Kind == "Deployment" {
		v.Strategy = "rolling"
		v.Phase = string(root.Health)
		v.Paused = digS(o, "spec", "paused") == "true"
		v.UserPaused = v.Paused
		bd.tree.Rollout = v
		return
	}
	v.Paused = digS(o, "spec", "paused") == "true" || len(digL(o, "status", "pauseConditions")) > 0
	v.UserPaused = digS(o, "spec", "paused") == "true"
	v.Aborted = digS(o, "status", "abort") == "true"
	v.Services = map[string]string{}
	switch {
	case has(o, "spec", "strategy", "blueGreen"):
		v.Strategy = "blueGreen"
		bd.blueGreen(v, o)
	default:
		v.Strategy = "canary"
		bd.canary(v, o)
	}
	bd.analysis(v, root)
	bd.tree.Rollout = v
}

func (bd *build) replicaRatio(root *Node) (canary, total int) {
	for _, n := range bd.nodes {
		if n.Kind == "ReplicaSet" && n.Parent == root.ID && n.Replicas != nil {
			total += n.Replicas.Desired
			if n.Role == RoleCanary || n.Role == RolePreview {
				canary += n.Replicas.Desired
			}
		}
	}
	return
}

func (bd *build) canary(v *RolloutView, o map[string]any) {
	c := digM(o, "spec", "strategy", "canary")
	for k, p := range map[string]string{"stable": "stableService", "canary": "canaryService"} {
		if s := digS(c, p); s != "" {
			v.Services[k] = s
		}
	}
	cur := -1
	if has(o, "status", "currentStepIndex") {
		cur = digI(o, "status", "currentStepIndex")
	}
	steps := digL(c, "steps")
	v.Current = cur
	desired := bd.canarySteps(v, o, steps, cur)
	if digS(o, "status", "phase") == "Healthy" && len(steps) > 0 {
		v.Current = -1
		for i := range v.Steps {
			v.Steps[i].State = "done"
		}
		desired = 100
	}
	v.DesiredWeight = desired
	if len(steps) == 0 {
		v.DesiredWeight = -1
	}
	if w := dig(o, "status", "canary", "weights", "canary", "weight"); w != nil {
		v.ActualWeight, v.ActualSource = toInt(w), "traffic router"
	} else if root := bd.nodes[Ref{Kind: "Rollout", Namespace: v.Namespace, Name: v.Name}.ID()]; root != nil {
		if cn, tot := bd.replicaRatio(root); tot > 0 {
			v.ActualWeight, v.ActualSource = cn*100/tot, "replica ratio"
		}
	}
	// Pauses.
	for _, pc := range digL(o, "status", "pauseConditions") {
		if st := ts(digS(m(pc), "startTime")); st > 0 {
			v.PauseStarted = st
		}
	}
	if v.PauseStarted > 0 && cur >= 0 && cur < len(v.Steps) && v.Steps[cur].Seconds > 0 {
		v.PauseEnds = v.PauseStarted + int64(v.Steps[cur].Seconds)*1000
	}
	// Pre-traffic: the new version is up and takes none of the real traffic.
	if v.Services["canary"] != "" && v.ActualWeight == 0 && !v.Aborted && digS(o, "status", "phase") != "Healthy" {
		v.PreTraffic = &PreTraffic{Address: fmt.Sprintf("Service %s/%s (not used by real users)", v.Namespace, v.Services["canary"]), LiveWeight: 0}
	}
	if hr := digS(m(firstOf(steps, "setHeaderRoute")), "setHeaderRoute", "name"); hr != "" && v.PreTraffic == nil && v.ActualWeight == 0 {
		v.PreTraffic = &PreTraffic{Address: "header route " + hr, LiveWeight: 0}
	}
}

func firstOf(steps []any, key string) any {
	for _, s := range steps {
		if m(s)[key] != nil {
			return s
		}
	}
	return nil
}

func (bd *build) blueGreen(v *RolloutView, o map[string]any) {
	b := digM(o, "spec", "strategy", "blueGreen")
	for k, p := range map[string]string{"active": "activeService", "preview": "previewService"} {
		if s := digS(b, p); s != "" {
			v.Services[k] = s
		}
	}
	current := digS(o, "status", "currentPodHash")
	active := digS(o, "status", "blueGreen", "activeSelector")
	switched := current != "" && current == active
	done := digS(o, "status", "phase") == "Healthy" && switched
	paused := v.Paused
	pre := has(b, "prePromotionAnalysis")
	post := has(b, "postPromotionAnalysis")
	delay := 30
	if has(b, "scaleDownDelaySeconds") {
		delay = digI(b, "scaleDownDelaySeconds")
	}
	labels := []struct{ kind, label string }{{"preview", "New version up on the preview service"}}
	if pre {
		labels = append(labels, struct{ kind, label string }{"analysis", "Tests against the preview (before any real traffic)"})
	}
	if digS(b, "autoPromotionEnabled") == "false" {
		labels = append(labels, struct{ kind, label string }{"pause", "Wait for someone to promote"})
	} else if s := digI(b, "autoPromotionSeconds"); s > 0 {
		labels = append(labels, struct{ kind, label string }{"pause", fmt.Sprintf("Promote automatically after %ds", s)})
	}
	labels = append(labels, struct{ kind, label string }{"switch", "Switch the active service to the new version"})
	if post {
		labels = append(labels, struct{ kind, label string }{"analysis", "Tests after the switch"})
	}
	labels = append(labels, struct{ kind, label string }{"scaledown", fmt.Sprintf("Keep the old version %ds for an instant return, then scale it down", delay)})
	// Where the rollout is.
	cur := 0
	switch {
	case done:
		cur = len(labels)
	case switched:
		for i, s := range labels {
			if s.kind == "switch" {
				cur = i + 1
			}
		}
	case paused:
		for i, s := range labels {
			if s.kind == "pause" {
				cur = i
			}
		}
		if cur == 0 {
			cur = 1
		}
	default:
		cur = 1
		if pre {
			cur = 1
		}
	}
	for i, s := range labels {
		st := Step{Index: i, Kind: s.kind, Label: s.label, State: "next"}
		switch {
		case i < cur:
			st.State = "done"
		case i == cur && !done:
			st.State = "current"
		}
		v.Steps = append(v.Steps, st)
	}
	v.Current = cur
	if done {
		v.Current = -1
	}
	if switched {
		v.DesiredWeight, v.ActualWeight = 100, 100
	} else {
		v.DesiredWeight, v.ActualWeight = 0, 0
		if cur >= 1 {
			v.PreTraffic = &PreTraffic{Address: fmt.Sprintf("Service %s/%s (the preview service; real users use %s)", v.Namespace, v.Services["preview"], v.Services["active"]), LiveWeight: 0}
		}
	}
	v.ActualSource = "service selector"
}

func (bd *build) analysis(v *RolloutView, root *Node) {
	var runs []*Node
	for _, n := range bd.nodes {
		if n.Kind == "AnalysisRun" {
			runs = append(runs, n)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Created < runs[j].Created })
	for _, n := range runs {
		o := bd.objs[n.ID]
		a := Analysis{Name: n.Name, Phase: digS(o, "status", "phase"), Message: n.Message, Created: n.Created}
		specs := map[string]map[string]any{}
		for _, ms := range digL(o, "spec", "metrics") {
			mm := m(ms)
			specs[digS(mm, "name")] = mm
		}
		for _, r := range digL(o, "status", "metricResults") {
			rm := m(r)
			sp := specs[digS(rm, "name")]
			mt := Metric{Name: digS(rm, "name"), Phase: digS(rm, "phase"), Success: bd.red.Text(digS(sp, "successCondition")), Failure: bd.red.Text(digS(sp, "failureCondition"))}
			prov := digM(sp, "provider")
			for _, k := range sortedKeys(prov) {
				mt.Provider = k
				switch k {
				case "prometheus":
					mt.Query = bd.red.Text(digS(prov, k, "query"))
				case "web":
					mt.Query = "GET " + bd.red.Text(digS(prov, k, "url"))
				case "job":
					mt.Query = "Job running " + bd.red.Text(firstImage(m(prov["job"])))
				case "datadog":
					mt.Query = bd.red.Text(digS(prov, k, "query"))
				}
			}
			for _, p := range digL(rm, "measurements") {
				pm := m(p)
				at := ts(digS(pm, "finishedAt"))
				if at == 0 {
					at = ts(digS(pm, "startedAt"))
				}
				mt.Points = append(mt.Points, Measurement{At: at, Phase: digS(pm, "phase"), Value: bd.red.Text(clip(digS(pm, "value"), 200)), Note: bd.red.Text(clip(digS(pm, "message"), 200))})
				if jn := digS(pm, "metadata", "job-name"); jn != "" {
					a.Jobs = append(a.Jobs, Ref{Kind: "Job", Namespace: n.Namespace, Name: jn}.ID())
				}
			}
			a.Metrics = append(a.Metrics, mt)
		}
		v.Analysis = append(v.Analysis, a)
	}
	_ = root
}

func templateNames(ts []any) string {
	var out []string
	for _, t := range ts {
		out = append(out, digS(m(t), "templateName"))
	}
	return strings.Join(out, ", ")
}

func durationSeconds(d string) int {
	n := 0
	cur := 0
	for _, r := range d {
		switch {
		case r >= '0' && r <= '9':
			cur = cur*10 + int(r-'0')
		case r == 's':
			n += cur
			cur = 0
		case r == 'm':
			n += cur * 60
			cur = 0
		case r == 'h':
			n += cur * 3600
			cur = 0
		}
	}
	if cur > 0 { // a bare number is seconds
		n += cur
	}
	return n
}

// canarySteps fills the view with the canary strategy's steps and returns the weight the current step wants.
func (bd *build) canarySteps(v *RolloutView, o map[string]any, steps []any, cur int) int {
	desired := 0
	for i, s := range steps {
		sm := m(s)
		st := Step{Index: i}
		switch {
		case has(sm, "setWeight"):
			w := toInt(sm["setWeight"])
			st.Kind, st.Weight, st.Label = "setWeight", &w, fmt.Sprintf("Send %d%% of traffic to the new version", w)
			if i <= cur {
				desired = w
			}
		case sm["pause"] != nil:
			st.Kind = "pause"
			if d := digS(sm, "pause", "duration"); d != "" {
				st.Seconds = durationSeconds(d)
				st.Label = "Pause " + d
			} else {
				st.Label = "Pause until someone promotes"
			}
		case sm["analysis"] != nil:
			st.Kind, st.Label = "analysis", "Analysis: "+templateNames(digL(sm, "analysis", "templates"))
		case sm["experiment"] != nil:
			st.Kind, st.Label = "experiment", "Experiment"
		case sm["setCanaryScale"] != nil:
			st.Kind, st.Label = "setCanaryScale", "Set the canary's scale"
		case sm["setHeaderRoute"] != nil:
			st.Kind, st.Label = "setHeaderRoute", "Route requests with header "+digS(sm, "setHeaderRoute", "name")+" to the new version only"
		case sm["setMirrorRoute"] != nil:
			st.Kind, st.Label = "setMirrorRoute", "Mirror requests to the new version"
		default:
			st.Kind, st.Label = "step", "Step"
		}
		switch {
		case cur < 0 || i > cur:
			st.State = "next"
		case i == cur && digS(o, "status", "phase") != "Healthy":
			st.State = "current"
		default:
			st.State = "done"
		}
		v.Steps = append(v.Steps, st)
	}
	return desired
}
