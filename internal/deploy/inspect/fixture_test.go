package inspect

import (
	"encoding/base64"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
)

// Canary strings: planted in ConfigMaps, logs and manifests; none may
// come out of a Redactor.
const (
	canaryPassword = "hunter2-LWCANARY-1"
	canarySecret   = "s3cr3t-LWCANARY-2-value"
	canaryToken    = "ghp_LWCANARYabcdefghijklmnopqrstuvwxyz0123"
)

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func stamp(d time.Duration) string { return t0.Add(d).Format(time.RFC3339) }

type kv = map[string]any

func mk(api, kind, ns, name, uid string, rest kv) *unstructured.Unstructured {
	meta := kv{"name": name, "uid": uid, "creationTimestamp": stamp(-time.Hour)}
	if ns != "" {
		meta["namespace"] = ns
	}
	o := kv{"apiVersion": api, "kind": kind, "metadata": meta}
	for k, v := range rest {
		if k == "metadata" {
			for mk, mv := range v.(kv) {
				meta[mk] = mv
			}
			continue
		}
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func owner(kind, name, uid string) []any {
	return []any{kv{"kind": kind, "name": name, "uid": uid, "controller": true}}
}

func podOf(ns, name, uid, rsName, rsUID, hash string, ready bool, extra kv) *unstructured.Unstructured {
	cs := kv{"name": "app", "image": "shop:" + hash, "ready": ready, "restartCount": int64(0), "state": kv{"running": kv{}}}
	phase := "Running"
	for k, v := range extra {
		cs[k] = v
	}
	return mk("v1", "Pod", ns, name, uid, kv{
		"metadata": kv{"ownerReferences": owner("ReplicaSet", rsName, rsUID), "labels": kv{"rollouts-pod-template-hash": hash, "app": "shop"}},
		"spec":     kv{"containers": []any{kv{"name": "app", "image": "shop:" + hash}}},
		"status": kv{"phase": phase, "conditions": []any{kv{"type": "Ready", "status": map[bool]string{true: "True", false: "False"}[ready]}},
			"containerStatuses": []any{cs}},
	})
}

// canaryFixture is a canary rollout of "shop" in namespace shop at step 2
// (weight 20, paused): stable RS "aaa" with 3 pods, canary RS "bbb" with 1
// pod (crash-looping when broken), services, a route, config, a secret, an
// analysis run with a job and a pod, a leftover ConfigMap and objects in a
// namespace the Airfield does not list.
func canaryFixture(broken bool) *dynfake.FakeDynamicClient {
	rollout := mk("argoproj.io/v1alpha1", "Rollout", "shop", "shop", "u-ro", kv{
		"metadata": kv{"annotations": kv{"rollout.argoproj.io/revision": "7"},
			"managedFields": []any{kv{"time": stamp(-time.Minute), "manager": "argocd"}}},
		"spec": kv{"replicas": int64(4), "strategy": kv{"canary": kv{"stableService": "shop-stable", "canaryService": "shop-canary",
			"steps": []any{kv{"setWeight": int64(10)}, kv{"setWeight": int64(20)}, kv{"pause": kv{}}, kv{"analysis": kv{"templates": []any{kv{"templateName": "success-rate"}}}}, kv{"setWeight": int64(50)}, kv{"pause": kv{"duration": "5m"}}}}},
			"template": kv{"spec": kv{"containers": []any{kv{"name": "app", "image": "shop:bbb", "env": []any{kv{"name": "DB_PASSWORD", "value": canaryPassword}, kv{"name": "MODE", "value": "prod"}}}},
				"volumes": []any{kv{"name": "c", "configMap": kv{"name": "shop-config"}}, kv{"name": "s", "secret": kv{"secretName": "shop-creds"}}}}}},
		"status": kv{"phase": "Paused", "message": "CanaryPauseStep", "currentStepIndex": int64(2), "stableRS": "aaa", "currentPodHash": "bbb",
			"pauseConditions": []any{kv{"reason": "CanaryPauseStep", "startTime": stamp(-30 * time.Second)}},
			"canary":          kv{"weights": kv{"canary": kv{"weight": int64(20)}, "stable": kv{"weight": int64(80)}}}},
	})
	rsA := mk("apps/v1", "ReplicaSet", "shop", "shop-aaa", "u-rsa", kv{
		"metadata": kv{"ownerReferences": owner("Rollout", "shop", "u-ro"), "labels": kv{"rollouts-pod-template-hash": "aaa"}, "annotations": kv{"rollout.argoproj.io/revision": "6"}},
		"spec":     kv{"replicas": int64(3), "template": kv{"metadata": kv{"labels": kv{"app": "shop"}}, "spec": kv{"containers": []any{kv{"name": "app", "image": "shop:aaa"}}}}},
		"status":   kv{"replicas": int64(3), "readyReplicas": int64(3), "availableReplicas": int64(3)}})
	rsB := mk("apps/v1", "ReplicaSet", "shop", "shop-bbb", "u-rsb", kv{
		"metadata": kv{"ownerReferences": owner("Rollout", "shop", "u-ro"), "labels": kv{"rollouts-pod-template-hash": "bbb"}, "annotations": kv{"rollout.argoproj.io/revision": "7"}},
		"spec":     kv{"replicas": int64(1), "template": kv{"metadata": kv{"labels": kv{"app": "shop"}}, "spec": kv{"containers": []any{kv{"name": "app", "image": "shop:bbb"}}}}},
		"status":   kv{"replicas": int64(1), "readyReplicas": int64(1), "availableReplicas": int64(1)}})
	canaryPod := podOf("shop", "shop-bbb-1", "u-pb1", "shop-bbb", "u-rsb", "bbb", true, nil)
	if broken {
		canaryPod = podOf("shop", "shop-bbb-1", "u-pb1", "shop-bbb", "u-rsb", "bbb", false, kv{"restartCount": int64(5),
			"state":     kv{"waiting": kv{"reason": "CrashLoopBackOff"}},
			"lastState": kv{"terminated": kv{"reason": "Error", "exitCode": int64(1)}}})
	}
	objs := []runtime.Object{rollout, rsA, rsB, canaryPod,
		podOf("shop", "shop-aaa-1", "u-pa1", "shop-aaa", "u-rsa", "aaa", true, nil),
		podOf("shop", "shop-aaa-2", "u-pa2", "shop-aaa", "u-rsa", "aaa", true, nil),
		podOf("shop", "shop-aaa-3", "u-pa3", "shop-aaa", "u-rsa", "aaa", true, nil),
		mk("v1", "Service", "shop", "shop-stable", "u-s1", kv{"spec": kv{"selector": kv{"app": "shop", "rollouts-pod-template-hash": "aaa"}, "ports": []any{kv{"port": int64(80), "protocol": "TCP"}}}}),
		mk("v1", "Service", "shop", "shop-canary", "u-s2", kv{"spec": kv{"selector": kv{"app": "shop", "rollouts-pod-template-hash": "bbb"}, "ports": []any{kv{"port": int64(80)}}}}),
		mk("gateway.networking.k8s.io/v1", "HTTPRoute", "shop", "shop", "u-hr", kv{"spec": kv{"hostnames": []any{"shop.example.com"}, "rules": []any{kv{"backendRefs": []any{kv{"name": "shop-stable", "weight": int64(80)}, kv{"name": "shop-canary", "weight": int64(20)}}}}}}),
		mk("v1", "ConfigMap", "shop", "shop-config", "u-cm", kv{"data": kv{"LOG_LEVEL": "info", "db.conf": "host=db\npassword=" + canaryPassword + "\n", "note": "token " + canaryToken, "plain": "hello " + canarySecret}}),
		mk("v1", "Secret", "shop", "shop-creds", "u-sec", kv{"type": "Opaque", "data": kv{"password": base64.StdEncoding.EncodeToString([]byte(canarySecret)), "user": base64.StdEncoding.EncodeToString([]byte("shopuser"))}}),
		mk("v1", "ServiceAccount", "shop", "shop", "u-sa", nil),
		mk("autoscaling/v2", "HorizontalPodAutoscaler", "shop", "shop", "u-hpa", kv{"spec": kv{"minReplicas": int64(2), "maxReplicas": int64(8)}, "status": kv{"currentReplicas": int64(4), "desiredReplicas": int64(4)}}),
		mk("argoproj.io/v1alpha1", "AnalysisRun", "shop", "shop-bbb-2-3", "u-ar", kv{
			"metadata": kv{"ownerReferences": owner("Rollout", "shop", "u-ro")},
			"spec":     kv{"metrics": []any{kv{"name": "success-rate", "successCondition": "result[0] >= 0.95", "provider": kv{"prometheus": kv{"query": "sum(rate(http_requests_total{code!~\"5..\"}[1m]))"}}}}},
			"status": kv{"phase": "Running", "metricResults": []any{kv{"name": "success-rate", "phase": "Running", "measurements": []any{
				kv{"phase": "Successful", "value": "[0.99]", "finishedAt": stamp(-20 * time.Second)}, kv{"phase": "Failed", "value": "[0.91]", "finishedAt": stamp(-10 * time.Second)}}}}}}),
		mk("batch/v1", "Job", "shop", "shop-bbb-2-3-push", "u-job", kv{"metadata": kv{"ownerReferences": owner("AnalysisRun", "shop-bbb-2-3", "u-ar")},
			"spec": kv{"template": kv{"spec": kv{"containers": []any{kv{"name": "push", "image": "tester:1"}}}}}, "status": kv{"active": int64(1)}}),
		mk("v1", "Pod", "shop", "shop-bbb-2-3-push-x", "u-jp", kv{"metadata": kv{"ownerReferences": owner("Job", "shop-bbb-2-3-push", "u-job")},
			"spec": kv{"containers": []any{kv{"name": "push", "image": "tester:1"}}}, "status": kv{"phase": "Running", "conditions": []any{kv{"type": "Ready", "status": "True"}}, "containerStatuses": []any{kv{"name": "push", "image": "tester:1", "ready": true, "state": kv{"running": kv{}}}}}}),
		// Leftover from an earlier render: has the release's label, is not listed.
		mk("v1", "ConfigMap", "shop", "shop-old-config", "u-old", kv{"metadata": kv{"labels": kv{"app.kubernetes.io/instance": "shop"}}, "data": kv{"a": "b"}}),
		// Something else in the same namespace, not of this release.
		mk("v1", "ConfigMap", "shop", "someone-elses", "u-else", kv{"data": kv{"a": "b"}}),
		// A warning event on the canary pod.
		mk("v1", "Event", "shop", "ev1", "u-ev1", kv{"type": "Warning", "reason": "BackOff", "message": "Back-off restarting failed container app password=" + canaryPassword, "count": int64(9),
			"lastTimestamp": stamp(-5 * time.Second), "involvedObject": kv{"uid": "u-pb1", "kind": "Pod", "name": "shop-bbb-1"}}),
		// Outside the Airfield's namespaces.
		mk("v1", "ConfigMap", "other", "elsewhere", "u-oth", kv{"data": kv{"a": "b"}}),
	}
	sc := runtime.NewScheme()
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(sc, KindLists(), objs...)
}

// listed is what the application lists.
func listed() []Ref {
	r := func(k, n string) Ref { return Ref{Kind: k, Namespace: "shop", Name: n} }
	return []Ref{r("Rollout", "shop"), r("Service", "shop-stable"), r("Service", "shop-canary"), r("HTTPRoute", "shop"), r("ConfigMap", "shop-config"),
		r("Secret", "shop-creds"), r("ServiceAccount", "shop"), r("HorizontalPodAutoscaler", "shop"),
		{Kind: "ConfigMap", Namespace: "other", Name: "elsewhere"}, {Kind: "Namespace", Name: "shop"}, {Kind: "MysteryKind", Namespace: "shop", Name: "x"}}
}

func base64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
