package inspect

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Request says whose objects to show.
type Request struct {
	Application string
	// Namespaces are the namespaces the Airfield lists: nothing outside them
	// is read.
	Namespaces []string
	// Resources are what the application lists (its status.resources).
	Resources []Ref
	// Rollout is the workload the deploy step names, shown even when the
	// application does not list it.
	Rollout *Ref
	// Since is when the Launch began; "new" and "changed" are measured from it.
	Since int64
	// Limit bounds the nodes; 0 means 3000.
	Limit int
}

// Builder builds trees.
type Builder struct {
	R   Reader
	Red *Redactor
	Now func() time.Time
}

type build struct {
	b      *Builder
	req    Request
	tree   *Tree
	nodes  map[string]*Node
	objs   map[string]map[string]any // by node id
	byUID  map[string]*Node
	owned  map[string][]map[string]any // owner uid -> owned objects (RS, Pod, Job, AnalysisRun, Experiment)
	events map[string][]Event          // by uid
	refs   map[string][]string         // ConfigMap/Secret id -> workload ids using it
	notes  map[string]bool
	red    *Redactor
}

func (b *Builder) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (bd *build) note(f string, a ...any) {
	s := fmt.Sprintf(f, a...)
	if !bd.notes[s] {
		bd.notes[s] = true
		bd.tree.Notes = append(bd.tree.Notes, s)
	}
}

func (bd *build) allowed(ns string) bool { return slices.Contains(bd.req.Namespaces, ns) }

// readErr words a failed read: a missing permission names what to grant.
func (bd *build) readErr(kind, ns string, err error) {
	msg := err.Error()
	if strings.Contains(msg, "forbidden") || strings.Contains(msg, "Forbidden") {
		bd.note("The deploy runner may not read %s in namespace %s, so they are left out; an administrator can grant it with the chart value delivery.deployRunner.rbac.inspect (see the reference page for the verbs).", plural(kind), ns)
		return
	}
	bd.note("Could not read %s in namespace %s: %s", plural(kind), ns, clip(msg, 160))
}

func plural(kind string) string {
	if strings.HasSuffix(kind, "y") {
		return strings.TrimSuffix(kind, "y") + "ies"
	}
	if strings.HasSuffix(kind, "s") {
		return kind + "es"
	}
	return kind + "s"
}

// Build reads the release's objects and builds the tree. Scope: it starts
// from the objects the application lists (and the rollout the step names),
// in the allowed namespaces, and follows owner references down to
// ReplicaSets, Pods, Jobs, analysis runs and experiments. Objects that
// carry the application's label but are not listed any more are added as
// leftovers. Nothing else is read into the tree.
func (b *Builder) Build(ctx context.Context, req Request) *Tree {
	if req.Limit <= 0 {
		req.Limit = 3000
	}
	bd := &build{b: b, req: req, nodes: map[string]*Node{}, objs: map[string]map[string]any{}, byUID: map[string]*Node{},
		owned: map[string][]map[string]any{}, events: map[string][]Event{}, refs: map[string][]string{}, notes: map[string]bool{},
		tree: &Tree{Application: req.Application, Namespaces: req.Namespaces, At: b.now().UnixMilli(), Since: req.Since}}
	roots := slices.Clone(req.Resources)
	if req.Rollout != nil && !slices.ContainsFunc(roots, func(r Ref) bool { return r.ID() == req.Rollout.ID() }) {
		roots = append(roots, *req.Rollout)
	}
	// The values of the release's own Secrets are read here, to be hidden
	// wherever they might turn up (a log line, a ConfigMap): they are
	// kept in memory for this build and never put in the tree.
	var known []string
	for _, r := range roots {
		if r.Kind != "Secret" || !bd.allowed(r.Namespace) {
			continue
		}
		if o, err := b.R.Get(ctx, "Secret", r.Namespace, r.Name); err == nil && o != nil {
			for _, v := range digM(o, "data") {
				if s, ok := v.(string); ok {
					if dec, err := base64.StdEncoding.DecodeString(s); err == nil {
						known = append(known, string(dec))
					}
				}
			}
			for _, v := range digM(o, "stringData") {
				if s, ok := v.(string); ok {
					known = append(known, s)
				}
			}
		}
	}
	bd.red = b.Red.With(known...)
	// Index what can be owned, once per namespace.
	used := map[string]bool{}
	for _, r := range roots {
		if r.Namespace != "" && bd.allowed(r.Namespace) {
			used[r.Namespace] = true
		}
	}
	for _, n := range req.Namespaces {
		if used[n] {
			bd.index(ctx, n)
		}
	}
	// The listed objects.
	for _, r := range roots {
		if r.Namespace == "" {
			continue // cluster-scoped objects are never read
		}
		if !bd.allowed(r.Namespace) {
			bd.note("%s %s/%s is outside the namespaces this Airfield lists (%s), so it is not read.", r.Kind, r.Namespace, r.Name, strings.Join(req.Namespaces, ", "))
			continue
		}
		bd.root(ctx, r)
	}
	bd.roles()
	bd.services()
	bd.usedBy()
	bd.attachEvents()
	bd.leftovers(ctx, used)
	bd.rollout(ctx)
	// Order: sections, then names.
	ids := sortedKeys(bd.nodes)
	for _, id := range ids {
		if len(bd.tree.Nodes) >= req.Limit {
			bd.tree.Truncated = true
			bd.note("The tree was cut at %d objects.", req.Limit)
			break
		}
		bd.tree.Nodes = append(bd.tree.Nodes, *bd.nodes[id])
	}
	bd.tree.red = bd.red
	return bd.tree
}

// index lists what can be owned in a namespace and keeps it by owner. A
// failure is noted, not fatal.
func (bd *build) index(ctx context.Context, ns string) {
	for _, kind := range []string{"ReplicaSet", "Pod", "Job", "AnalysisRun", "Experiment"} {
		items, err := bd.b.R.List(ctx, kind, ns)
		if err != nil {
			if kind == "AnalysisRun" || kind == "Experiment" {
				if strings.Contains(err.Error(), "could not find the requested resource") {
					continue // Argo Rollouts is not installed
				}
			}
			bd.readErr(kind, ns, err)
			continue
		}
		for _, it := range items {
			for _, o := range ownerUIDs(it) {
				bd.owned[o] = append(bd.owned[o], it)
			}
		}
	}
	evs, err := bd.b.R.List(ctx, "Event", ns)
	if err != nil {
		bd.readErr("Event", ns, err)
		return
	}
	for _, e := range evs {
		u := digS(e, "involvedObject", "uid")
		if u == "" {
			continue
		}
		last := ts(digS(e, "lastTimestamp"))
		if last == 0 {
			last = ts(digS(e, "eventTime"))
		}
		if last == 0 {
			last = ts(digS(e, "metadata", "creationTimestamp"))
		}
		cnt := digI(e, "count")
		if cnt == 0 {
			cnt = 1
		}
		bd.events[u] = append(bd.events[u], Event{Type: digS(e, "type"), Reason: digS(e, "reason"),
			Message: bd.red.Text(clip(digS(e, "message"), 400)), Count: cnt, Last: last})
	}
}

// root reads one listed object and everything under it.
func (bd *build) root(ctx context.Context, r Ref) {
	if _, ok := kinds[r.Kind]; !ok {
		bd.addStub(r, "not inspected: the inspector does not read this kind")
		return
	}
	if _, dup := bd.nodes[r.ID()]; dup {
		return
	}
	obj, err := bd.b.R.Get(ctx, r.Kind, r.Namespace, r.Name)
	switch {
	case err != nil:
		bd.readErr(r.Kind, r.Namespace, err)
		bd.addStub(r, "not read")
		return
	case obj == nil:
		n := bd.addStub(r, "listed by the application, but not in the cluster")
		n.Health = Missing
		return
	}
	n := bd.addObject(r.Kind, obj, "")
	bd.descend(n, obj, 0)
}

func (bd *build) addStub(r Ref, msg string) *Node {
	n := &Node{ID: r.ID(), Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, Health: Unknown, Message: msg, Section: sectionOf(r.Kind)}
	bd.nodes[n.ID] = n
	return n
}

func sectionOf(kind string) string {
	if k, ok := kinds[kind]; ok && k.section != "" {
		return k.section
	}
	return "other"
}

// addObject makes a node of an object.
func (bd *build) addObject(kind string, obj map[string]any, parent string) *Node {
	r := Ref{Kind: kind, Namespace: ns(obj), Name: name(obj)}
	if n, ok := bd.nodes[r.ID()]; ok {
		return n
	}
	n := &Node{ID: r.ID(), Kind: kind, Namespace: r.Namespace, Name: r.Name, Parent: parent,
		Created: ts(digS(obj, "metadata", "creationTimestamp")), Group: strings.SplitN(digS(obj, "apiVersion"), "/", 2)[0]}
	if !strings.Contains(digS(obj, "apiVersion"), "/") {
		n.Group = ""
	}
	if parent == "" {
		n.Section = sectionOf(kind)
	}
	bd.nodes[n.ID] = n
	bd.objs[n.ID] = obj
	if u := uid(obj); u != "" {
		bd.byUID[u] = n
	}
	bd.describe(n, obj)
	n.Change = bd.change(obj)
	return n
}

func (bd *build) change(obj map[string]any) string {
	since := bd.req.Since
	if since == 0 {
		return ""
	}
	if ts(digS(obj, "metadata", "creationTimestamp")) >= since {
		return ChangeNew
	}
	for _, mf := range digL(obj, "metadata", "managedFields") {
		if mm := m(mf); mm != nil && ts(digS(mm, "time")) >= since && digS(mm, "subresource") != "status" {
			return ChangeChanged
		}
	}
	return ChangeUnchanged
}

// descend adds what a node owns.
func (bd *build) descend(n *Node, obj map[string]any, depth int) {
	if depth > 6 {
		return
	}
	for _, c := range bd.owned[uid(obj)] {
		kind := digS(c, "kind")
		if kind == "" {
			kind = kindOf(c)
		}
		if _, ok := kinds[kind]; !ok {
			continue
		}
		child := bd.addObject(kind, c, n.ID)
		bd.descend(child, c, depth+1)
	}
}

// kindOf finds an object's kind when it has none (a list item from a real
// API server has none).
func kindOf(c map[string]any) string {
	switch {
	case has(c, "spec", "template", "spec", "containers") && has(c, "spec", "selector") && digS(c, "apiVersion") == "apps/v1":
		return "ReplicaSet"
	case has(c, "spec", "containers"):
		return "Pod"
	case has(c, "spec", "metrics") && strings.HasPrefix(digS(c, "apiVersion"), "argoproj.io"):
		return "AnalysisRun"
	case has(c, "spec", "template", "spec", "containers"):
		return "Job"
	}
	return ""
}

// describe fills a node's health, message and the kind's own fields.
func (bd *build) describe(n *Node, o map[string]any) {
	red := bd.red
	switch n.Kind {
	case "Pod":
		bd.pod(n, o)
	case "ReplicaSet":
		n.Hash = labels(o)["rollouts-pod-template-hash"]
		if n.Hash == "" {
			n.Hash = labels(o)["pod-template-hash"]
		}
		n.Revision = annotation(o, "deployment.kubernetes.io/revision")
		if n.Revision == "" {
			n.Revision = annotation(o, "rollout.argoproj.io/revision")
		}
		n.Replicas = &Replicas{Desired: digI(o, "spec", "replicas"), Ready: digI(o, "status", "readyReplicas"), Available: digI(o, "status", "availableReplicas"), Updated: digI(o, "status", "replicas")}
		n.Image = firstImage(o)
		switch {
		case n.Replicas.Desired == 0:
			n.Health, n.Message = Suspended, "scaled down"
		case n.Replicas.Ready >= n.Replicas.Desired:
			n.Health = Healthy
		default:
			n.Health, n.Message = Progressing, fmt.Sprintf("%d of %d ready", n.Replicas.Ready, n.Replicas.Desired)
		}
	case "Deployment", "StatefulSet", "DaemonSet":
		bd.workload(n, o)
	case "Rollout":
		bd.workload(n, o)
		n.Revision = annotation(o, "rollout.argoproj.io/revision")
		n.Message = red.Text(digS(o, "status", "message"))
		switch digS(o, "status", "phase") {
		case "Healthy":
			n.Health = Healthy
		case "Paused":
			n.Health = Suspended
		case "Degraded":
			n.Health = Degraded
		default:
			n.Health = Progressing
		}
		if n.Message == "" {
			n.Message = strings.TrimSpace(digS(o, "status", "phase") + " " + digS(o, "status", "message"))
		}
	case "Job", "CronJob", "AnalysisRun", "Experiment":
		bd.describeBatch(n, o)
	case "Service", "ConfigMap", "Secret", "ServiceAccount", "Role", "RoleBinding":
		bd.describeConfig(n, o)
	case "HorizontalPodAutoscaler", "PodDisruptionBudget", "PersistentVolumeClaim", "NetworkPolicy", "Ingress", "HTTPRoute", "VirtualService", "DestinationRule":
		bd.describeMisc(n, o)
	}
	if n.Health == "" {
		n.Health = Unknown
	}
}

func (bd *build) describeBatch(n *Node, o map[string]any) {
	red := bd.red
	switch n.Kind {
	case "Job":
		switch {
		case digI(o, "status", "failed") > 0 && digI(o, "status", "active") == 0:
			n.Health, n.Message = Degraded, jobFailure(o, red)
		case digI(o, "status", "succeeded") > 0:
			n.Health = Completed
		default:
			n.Health = Progressing
		}
		n.Image = firstImage(o)
	case "CronJob":
		n.Health = Healthy
		n.Message = "schedule " + digS(o, "spec", "schedule")
		if digS(o, "spec", "suspend") == "true" {
			n.Health, n.Message = Suspended, "suspended; "+n.Message
		}
	case "AnalysisRun", "Experiment":
		switch digS(o, "status", "phase") {
		case "Successful":
			n.Health = Healthy
		case "Failed", "Error":
			n.Health = Degraded
		case "Inconclusive":
			n.Health = Suspended
		default:
			n.Health = Progressing
		}
		n.Message = red.Text(clip(digS(o, "status", "message"), 300))
		if n.Message == "" {
			n.Message = digS(o, "status", "phase")
		}
		if n.Parent == "" {
			n.Section = "testing"
		}
	}
}

func (bd *build) describeConfig(n *Node, o map[string]any) {
	red := bd.red
	switch n.Kind {
	case "Service":
		n.Health = Healthy
		for _, p := range digL(o, "spec", "ports") {
			pm := m(p)
			n.Ports = append(n.Ports, fmt.Sprintf("%v/%s", pm["port"], strings.ToLower(nonEmpty(digS(pm, "protocol"), "TCP"))))
		}
		n.Message = nonEmpty(digS(o, "spec", "type"), "ClusterIP")
	case "ConfigMap":
		n.Health = Healthy
		data := map[string]string{}
		for k, v := range digM(o, "data") {
			s, _ := v.(string)
			data[k] = s
			n.Keys = append(n.Keys, k)
		}
		for k := range digM(o, "binaryData") {
			n.Keys = append(n.Keys, k+" (binary)")
		}
		sort.Strings(n.Keys)
		if len(data) > 60 {
			bd.note("ConfigMap %s has %d keys; the first 60 are shown.", n.Name, len(data))
			for _, k := range sortedKeys(data)[60:] {
				delete(data, k)
			}
		}
		n.Data = red.Data(data, 4000)
	case "Secret":
		n.Health = Healthy
		n.Message = "type " + nonEmpty(digS(o, "type"), "Opaque") + "; values are never shown"
		for k := range digM(o, "data") {
			n.Keys = append(n.Keys, k)
		}
		for k := range digM(o, "stringData") {
			n.Keys = append(n.Keys, k)
		}
		sort.Strings(n.Keys)
		n.Keys = slices.Compact(n.Keys)
	case "ServiceAccount":
		n.Health = Healthy
		for _, s := range digL(o, "imagePullSecrets") {
			n.Bound = append(n.Bound, "pulls images with Secret "+digS(m(s), "name"))
		}
	case "Role":
		n.Health = Healthy
		for _, r := range digL(o, "rules") {
			rm := m(r)
			n.Bound = append(n.Bound, fmt.Sprintf("%s %s", joinAny(l(rm["verbs"])), joinAny(l(rm["resources"]))))
		}
		if len(n.Bound) > 12 {
			n.Bound = append(n.Bound[:12], "…")
		}
	case "RoleBinding":
		n.Health = Healthy
		n.Message = "binds " + digS(o, "roleRef", "kind") + " " + digS(o, "roleRef", "name")
		for _, s := range digL(o, "subjects") {
			sm := m(s)
			n.Bound = append(n.Bound, digS(sm, "kind")+" "+digS(sm, "name"))
		}
	}
}

func (bd *build) describeMisc(n *Node, o map[string]any) {
	switch n.Kind {
	case "HorizontalPodAutoscaler":
		n.Health = Healthy
		n.Message = fmt.Sprintf("min %d, max %d; now %d, wants %d", digI(o, "spec", "minReplicas"), digI(o, "spec", "maxReplicas"), digI(o, "status", "currentReplicas"), digI(o, "status", "desiredReplicas"))
		for _, c := range digL(o, "status", "conditions") {
			if cm := m(c); digS(cm, "type") == "ScalingActive" && digS(cm, "status") == "False" {
				n.Health, n.Message = Degraded, n.Message+"; not scaling: "+digS(cm, "message")
			}
		}
	case "PodDisruptionBudget":
		n.Health = Healthy
		n.Message = fmt.Sprintf("%d disruptions allowed; %d of %d healthy", digI(o, "status", "disruptionsAllowed"), digI(o, "status", "currentHealthy"), digI(o, "status", "desiredHealthy"))
	case "PersistentVolumeClaim":
		switch digS(o, "status", "phase") {
		case "Bound":
			n.Health = Healthy
		case "Lost":
			n.Health = Degraded
		default:
			n.Health = Progressing
		}
		n.Message = digS(o, "status", "phase") + " " + digS(o, "status", "capacity", "storage")
	case "NetworkPolicy":
		n.Health = Healthy
		n.Message = fmt.Sprintf("%s; %d ingress and %d egress rules", joinAny(digL(o, "spec", "policyTypes")), len(digL(o, "spec", "ingress")), len(digL(o, "spec", "egress")))
	case "Ingress", "HTTPRoute", "VirtualService", "DestinationRule":
		n.Health = Healthy
		bd.route(n, o)
	}
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func joinAny(v []any) string {
	var s []string
	for _, x := range v {
		s = append(s, fmt.Sprint(x))
	}
	return strings.Join(s, ",")
}

func firstImage(o map[string]any) string {
	for _, c := range digL(o, "spec", "template", "spec", "containers") {
		return digS(m(c), "image")
	}
	return ""
}

func jobFailure(o map[string]any, red *Redactor) string {
	for _, c := range digL(o, "status", "conditions") {
		if cm := m(c); digS(cm, "type") == "Failed" {
			return red.Text(digS(cm, "reason") + ": " + digS(cm, "message"))
		}
	}
	return "failed"
}

func (bd *build) workload(n *Node, o map[string]any) {
	n.Replicas = &Replicas{Desired: digI(o, "spec", "replicas"), Ready: digI(o, "status", "readyReplicas"), Available: digI(o, "status", "availableReplicas"), Updated: digI(o, "status", "updatedReplicas")}
	if n.Kind == "DaemonSet" {
		n.Replicas = &Replicas{Desired: digI(o, "status", "desiredNumberScheduled"), Ready: digI(o, "status", "numberReady"), Available: digI(o, "status", "numberAvailable"), Updated: digI(o, "status", "updatedNumberScheduled")}
	}
	n.Image = firstImage(o)
	n.Revision = annotation(o, "deployment.kubernetes.io/revision")
	switch {
	case n.Replicas.Ready >= n.Replicas.Desired && n.Replicas.Updated >= n.Replicas.Desired:
		n.Health = Healthy
	default:
		n.Health = Progressing
		n.Message = fmt.Sprintf("%d of %d ready, %d updated", n.Replicas.Ready, n.Replicas.Desired, n.Replicas.Updated)
	}
	for _, c := range digL(o, "status", "conditions") {
		cm := m(c)
		if digS(cm, "type") == "Progressing" && digS(cm, "status") == "False" || digS(cm, "type") == "ReplicaFailure" && digS(cm, "status") == "True" {
			n.Health, n.Message = Degraded, bd.red.Text(digS(cm, "reason")+": "+digS(cm, "message"))
		}
	}
	if digS(o, "spec", "paused") == "true" {
		n.Health, n.Message = Suspended, "paused"
	}
	// What the pod template reads.
	for _, ref := range podRefs(digM(o, "spec", "template", "spec")) {
		id := ref + "/" + n.Namespace + "/"
		_ = id
		bd.refs[n.Namespace+"|"+ref] = append(bd.refs[n.Namespace+"|"+ref], n.ID)
	}
}

// podRefs are "ConfigMap/name" and "Secret/name" a pod spec reads.
func podRefs(spec map[string]any) []string {
	var out []string
	add := func(kind, name string) {
		if name != "" {
			out = append(out, kind+"/"+name)
		}
	}
	for _, v := range digL(spec, "volumes") {
		vm := m(v)
		add("ConfigMap", digS(vm, "configMap", "name"))
		add("Secret", digS(vm, "secret", "secretName"))
		for _, s := range digL(vm, "projected", "sources") {
			add("ConfigMap", digS(m(s), "configMap", "name"))
			add("Secret", digS(m(s), "secret", "name"))
		}
	}
	for _, key := range []string{"containers", "initContainers"} {
		for _, c := range digL(spec, key) {
			cm := m(c)
			for _, e := range digL(cm, "envFrom") {
				add("ConfigMap", digS(m(e), "configMapRef", "name"))
				add("Secret", digS(m(e), "secretRef", "name"))
			}
			for _, e := range digL(cm, "env") {
				add("ConfigMap", digS(m(e), "valueFrom", "configMapKeyRef", "name"))
				add("Secret", digS(m(e), "valueFrom", "secretKeyRef", "name"))
			}
		}
	}
	return out
}

func (bd *build) usedBy() {
	for key, users := range bd.refs {
		nsName, ref, _ := strings.Cut(key, "|")
		kind, name, _ := strings.Cut(ref, "/")
		id := kind + "/" + nsName + "/" + name
		if n, ok := bd.nodes[id]; ok {
			us := slices.Clone(users)
			slices.Sort(us)
			n.UsedBy = slices.Compact(us)
		}
	}
}

func (bd *build) pod(n *Node, o map[string]any) {
	phase := digS(o, "status", "phase")
	ready := false
	for _, c := range digL(o, "status", "conditions") {
		if cm := m(c); digS(cm, "type") == "Ready" {
			ready = digS(cm, "status") == "True"
		}
	}
	add := func(statuses []any, init bool) {
		for _, s := range statuses {
			sm := m(s)
			c := Container{Name: digS(sm, "name"), Image: digS(sm, "image"), Init: init, Ready: digS(sm, "ready") == "true", Restarts: digI(sm, "restartCount")}
			st := digM(sm, "state")
			switch {
			case st["running"] != nil:
				c.State = "running"
			case st["waiting"] != nil:
				c.State, c.Reason = "waiting", digS(st, "waiting", "reason")
			case st["terminated"] != nil:
				c.State, c.Reason = "terminated", digS(st, "terminated", "reason")
				code := digI(st, "terminated", "exitCode")
				c.ExitCode = &code
			default:
				c.State = "waiting"
			}
			if lt := digM(sm, "lastState", "terminated"); lt != nil {
				c.Previous = true
				if c.Reason == "" || c.Reason == "CrashLoopBackOff" {
					c.Reason = nonEmpty(c.Reason, digS(lt, "reason"))
				}
			}
			if c.State == "terminated" {
				c.Previous = true
			}
			n.Containers = append(n.Containers, c)
		}
	}
	add(digL(o, "status", "initContainerStatuses"), true)
	add(digL(o, "status", "containerStatuses"), false)
	if len(n.Containers) == 0 { // not started yet: show the spec's
		for _, c := range digL(o, "spec", "containers") {
			n.Containers = append(n.Containers, Container{Name: digS(m(c), "name"), Image: digS(m(c), "image"), State: "waiting"})
		}
	}
	if len(n.Containers) > 0 {
		n.Image = n.Containers[len(n.Containers)-1].Image
	}
	bd.podHealth(n, o, phase, ready)
}

func (bd *build) podHealth(n *Node, o map[string]any, phase string, ready bool) {
	bad := ""
	for _, c := range n.Containers {
		switch c.Reason {
		case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "CreateContainerError", "InvalidImageName", "OOMKilled", "RunContainerError", "Error":
			if c.State != "terminated" || phase == "Failed" || c.Reason == "OOMKilled" || c.Reason == "Error" && !ready && phase != "Succeeded" {
				bad = fmt.Sprintf("container %s: %s", c.Name, c.Reason)
			}
		}
	}
	switch {
	case digS(o, "metadata", "deletionTimestamp") != "":
		n.Health, n.Message = Progressing, "terminating"
	case phase == "Succeeded":
		n.Health, n.Message = Completed, "completed"
	case phase == "Failed":
		n.Health, n.Message = Degraded, nonEmpty(digS(o, "status", "reason"), "failed")
	case bad != "":
		n.Health, n.Message = Degraded, bad
	case phase == "Running" && ready:
		n.Health = Healthy
	case phase == "Pending" || phase == "Running":
		n.Health, n.Message = Progressing, nonEmpty(digS(o, "status", "reason"), "not ready yet")
		for _, c := range n.Containers {
			if c.State == "waiting" && c.Reason != "" {
				n.Message = "container " + c.Name + ": " + c.Reason
			}
		}
	default:
		n.Health = Unknown
	}
	if n.Health != Healthy && n.Health != Completed {
		n.NotReady = &NotReady{Why: bd.whyNotReady(n, o)}
	}
}

// whyNotReady gathers status, conditions and events into one sentence (the
// last log lines are added by the caller that can read logs).
func (bd *build) whyNotReady(n *Node, o map[string]any) string {
	var parts []string
	for _, c := range n.Containers {
		if c.State == "waiting" && c.Reason != "" {
			parts = append(parts, fmt.Sprintf("container %s is waiting: %s", c.Name, c.Reason))
		}
		if c.State == "terminated" || c.Previous && c.Restarts > 0 {
			code := ""
			if c.ExitCode != nil {
				code = fmt.Sprintf(" (exit code %d)", *c.ExitCode)
			}
			parts = append(parts, fmt.Sprintf("container %s ended%s: %s; restarted %d times", c.Name, code, nonEmpty(c.Reason, "no reason given"), c.Restarts))
		}
		if c.State == "running" && !c.Ready {
			parts = append(parts, fmt.Sprintf("container %s runs but is not ready: its readiness probe is failing", c.Name))
		}
	}
	for _, c := range digL(o, "status", "conditions") {
		cm := m(c)
		if digS(cm, "status") == "False" && digS(cm, "message") != "" && (digS(cm, "type") == "PodScheduled" || digS(cm, "type") == "Initialized") {
			parts = append(parts, digS(cm, "type")+": "+bd.red.Text(digS(cm, "message")))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, nonEmpty(n.Message, "not ready yet"))
	}
	return strings.Join(parts, "; ")
}

func (bd *build) attachEvents() {
	for id, n := range bd.nodes {
		evs := bd.events[uid(bd.objs[id])]
		if len(evs) == 0 {
			continue
		}
		sort.SliceStable(evs, func(i, j int) bool {
			if (evs[i].Type == "Warning") != (evs[j].Type == "Warning") {
				return evs[i].Type == "Warning"
			}
			return evs[i].Last > evs[j].Last
		})
		if len(evs) > 10 {
			evs = evs[:10]
		}
		n.Events = evs
		// A pod's last warning explains why it is not ready.
		if n.NotReady != nil && evs[0].Type == "Warning" {
			n.NotReady.Why += "; event " + evs[0].Reason + ": " + evs[0].Message
		}
	}
}

// roles gives ReplicaSets their role in the delivery.
func (bd *build) roles() {
	for id, n := range bd.nodes {
		if n.Kind != "ReplicaSet" {
			continue
		}
		parent := bd.nodes[n.Parent]
		if parent == nil {
			continue
		}
		po := bd.objs[parent.ID]
		switch parent.Kind {
		case "Rollout":
			stable := digS(po, "status", "stableRS")
			current := digS(po, "status", "currentPodHash")
			switch {
			case has(po, "spec", "strategy", "blueGreen"):
				active := digS(po, "status", "blueGreen", "activeSelector")
				switch {
				case n.Hash != "" && n.Hash == active:
					n.Role = RoleActive
				case n.Hash != "" && n.Hash == current && n.Replicas.Desired > 0:
					n.Role = RolePreview
				default:
					n.Role = RoleOld
				}
			default:
				switch {
				case n.Hash != "" && n.Hash == stable:
					n.Role = RoleStable
				case n.Hash != "" && n.Hash == current:
					n.Role = RoleCanary
				default:
					n.Role = RoleOld
				}
			}
		case "Deployment":
			if annotation(po, "deployment.kubernetes.io/revision") == n.Revision && n.Replicas.Desired > 0 {
				n.Role = RoleCurrent
			} else {
				n.Role = RoleOld
			}
		case "Experiment":
			n.Role = RoleCanary
		}
		_ = id
	}
}

// services finds which ReplicaSet each Service selects and what role the
// strategy gives it.
func (bd *build) services() {
	for id, n := range bd.nodes {
		if n.Kind != "Service" {
			continue
		}
		o := bd.objs[id]
		sel := map[string]string{}
		for k, v := range digM(o, "spec", "selector") {
			sel[k], _ = v.(string)
		}
		for rid, rs := range bd.nodes {
			if rs.Kind != "ReplicaSet" || rs.Namespace != n.Namespace {
				continue
			}
			lab := map[string]string{}
			for k, v := range digM(bd.objs[rid], "spec", "template", "metadata", "labels") {
				lab[k], _ = v.(string)
			}
			if rs.Hash != "" {
				lab["rollouts-pod-template-hash"] = rs.Hash
			}
			if subset(sel, lab) && rs.Replicas != nil && rs.Replicas.Desired > 0 {
				n.Selects = append(n.Selects, rid)
			}
		}
		sort.Strings(n.Selects)
		// Role by name from any rollout's strategy.
		for _, ro := range bd.nodes {
			if ro.Kind != "Rollout" || ro.Namespace != n.Namespace {
				continue
			}
			ob := bd.objs[ro.ID]
			for role, path := range map[string][]string{
				RoleStable: {"canary", "stableService"}, RoleCanary: {"canary", "canaryService"},
				RoleActive: {"blueGreen", "activeService"}, RolePreview: {"blueGreen", "previewService"}} {
				if digS(ob, append([]string{"spec", "strategy"}, path...)...) == n.Name {
					n.Role = role
				}
			}
		}
		if n.Role == "" && len(n.Selects) == 1 {
			n.Role = bd.nodes[n.Selects[0]].Role
		}
		if len(n.Selects) == 0 {
			n.Message += "; selects no running ReplicaSet of this release"
		} else {
			var names []string
			for _, s := range n.Selects {
				names = append(names, bd.nodes[s].Name)
			}
			n.Message += "; selects " + strings.Join(names, ", ")
		}
	}
}

// route reads a route object's backends and weights.
func (bd *build) route(n *Node, o map[string]any) {
	switch n.Kind {
	case "HTTPRoute":
		for _, r := range digL(o, "spec", "rules") {
			rm := m(r)
			match := ""
			for _, mt := range l(rm["matches"]) {
				for _, h := range l(m(mt)["headers"]) {
					match = "header " + digS(m(h), "name") + "=" + digS(m(h), "value")
				}
			}
			for _, b := range l(rm["backendRefs"]) {
				bm := m(b)
				w := 1
				if bm["weight"] != nil {
					w = toInt(bm["weight"])
				}
				n.Weights = append(n.Weights, Weight{Backend: digS(bm, "name"), Weight: w, Match: match})
			}
		}
		if h := digL(o, "spec", "hostnames"); len(h) > 0 {
			n.Message = "hosts " + joinAny(h)
		}
	case "Ingress":
		for _, r := range digL(o, "spec", "rules") {
			for _, p := range digL(m(r), "http", "paths") {
				n.Weights = append(n.Weights, Weight{Backend: digS(m(p), "backend", "service", "name"), Weight: 100, Match: digS(m(r), "host") + digS(m(p), "path")})
			}
		}
		if w := annotation(o, "nginx.ingress.kubernetes.io/canary-weight"); w != "" {
			n.Message = "canary ingress, weight " + w + "%"
			for i := range n.Weights {
				n.Weights[i].Weight = toInt(w)
			}
		}
		if h := annotation(o, "nginx.ingress.kubernetes.io/canary-by-header"); h != "" {
			n.Message = nonEmpty(n.Message+"; ", "") + "canary by header " + h
		}
	case "VirtualService":
		for _, r := range digL(o, "spec", "http") {
			rm := m(r)
			match := ""
			for _, mt := range l(rm["match"]) {
				for h := range digM(m(mt), "headers") {
					match = "header " + h
				}
			}
			for _, d := range l(rm["route"]) {
				dm := m(d)
				n.Weights = append(n.Weights, Weight{Backend: digS(dm, "destination", "host") + nonEmptyPrefix(":", digS(dm, "destination", "subset")), Weight: toInt(dm["weight"]), Match: match})
			}
		}
	case "DestinationRule":
		for _, s := range digL(o, "spec", "subsets") {
			sm := m(s)
			var ls []string
			for k, v := range digM(sm, "labels") {
				ls = append(ls, fmt.Sprintf("%s=%v", k, v))
			}
			sort.Strings(ls)
			n.Bound = append(n.Bound, "subset "+digS(sm, "name")+": "+strings.Join(ls, ","))
		}
		n.Message = "host " + digS(o, "spec", "host")
	}
}

func nonEmptyPrefix(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}

// leftoverKinds are the kinds searched for objects that carry the
// release's label but are no longer rendered.
var leftoverKinds = []string{"ConfigMap", "Secret", "Service", "ServiceAccount", "Role", "RoleBinding", "Deployment", "Rollout", "StatefulSet",
	"CronJob", "HorizontalPodAutoscaler", "PodDisruptionBudget", "PersistentVolumeClaim", "NetworkPolicy", "Ingress", "HTTPRoute"}

func (bd *build) leftovers(ctx context.Context, used map[string]bool) {
	app := bd.req.Application
	if app == "" {
		return
	}
	for ns := range used {
		for _, kind := range leftoverKinds {
			items, err := bd.b.R.List(ctx, kind, ns)
			if err != nil {
				if !strings.Contains(err.Error(), "could not find the requested resource") {
					bd.readErr(kind, ns, err)
				}
				continue
			}
			for _, it := range items {
				id := Ref{Kind: kind, Namespace: ns, Name: name(it)}.ID()
				if _, ok := bd.nodes[id]; ok {
					continue
				}
				if labels(it)["app.kubernetes.io/instance"] != app && !strings.HasPrefix(annotation(it, "argocd.argoproj.io/tracking-id"), app+":") {
					continue
				}
				if len(ownerUIDs(it)) > 0 {
					continue // owned by something that is itself in the tree
				}
				n := bd.addObject(kind, it, "")
				n.Change = ChangeLeftover
				n.Message = strings.TrimPrefix(n.Message+"; ", "; ") + "not in what the application renders now"
			}
		}
	}
}
