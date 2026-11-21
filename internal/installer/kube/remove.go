package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// inventory lists what uninstall would remove (kind/name) and what it deliberately leaves.
func (in *Installer) inventory(ctx context.Context, cl Cluster, ns, release string) (remove, keep []string, err error) {
	sel := metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + release}
	c := cl.Clientset()
	add := func(kind string, names []string) {
		for _, n := range names {
			remove = append(remove, kind+"/"+n)
		}
	}
	if l, e := c.AppsV1().Deployments(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("deployment", []string{i.Name})
		}
	}
	if l, e := c.AppsV1().StatefulSets(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("statefulset", []string{i.Name})
		}
	}
	if l, e := c.CoreV1().Services(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("service", []string{i.Name})
		}
	}
	if l, e := c.CoreV1().ConfigMaps(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("configmap", []string{i.Name})
		}
	}
	if l, e := c.NetworkingV1().NetworkPolicies(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("networkpolicy", []string{i.Name})
		}
	}
	if l, e := c.NetworkingV1().Ingresses(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("ingress", []string{i.Name})
		}
	}
	if l, e := c.PolicyV1().PodDisruptionBudgets(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("poddisruptionbudget", []string{i.Name})
		}
	}
	if l, e := c.CoreV1().ServiceAccounts(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("serviceaccount", []string{i.Name})
		}
	}
	if l, e := c.RbacV1().RoleBindings(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("rolebinding", []string{i.Name})
		}
	}
	if l, e := c.RbacV1().Roles(ns).List(ctx, sel); e == nil {
		for _, i := range l.Items {
			add("role", []string{i.Name})
		}
	}
	return remove, keep, nil
}

func (in *Installer) remove(ctx context.Context, cl Cluster, ns, release string) error {
	c := cl.Clientset()
	sel := metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + release}
	del := metav1.DeleteOptions{}
	type step struct {
		what string
		fn   func() error
	}
	steps := []step{
		{"deployments", func() error { return c.AppsV1().Deployments(ns).DeleteCollection(ctx, del, sel) }},
		{"statefulsets", func() error { return c.AppsV1().StatefulSets(ns).DeleteCollection(ctx, del, sel) }},
		{"ingresses", func() error { return c.NetworkingV1().Ingresses(ns).DeleteCollection(ctx, del, sel) }},
		{"network policies", func() error { return c.NetworkingV1().NetworkPolicies(ns).DeleteCollection(ctx, del, sel) }},
		{"pod disruption budgets", func() error { return c.PolicyV1().PodDisruptionBudgets(ns).DeleteCollection(ctx, del, sel) }},
		{"config maps", func() error { return c.CoreV1().ConfigMaps(ns).DeleteCollection(ctx, del, sel) }},
		{"role bindings", func() error { return c.RbacV1().RoleBindings(ns).DeleteCollection(ctx, del, sel) }},
		{"roles", func() error { return c.RbacV1().Roles(ns).DeleteCollection(ctx, del, sel) }},
	}
	for _, s := range steps {
		if err := s.fn(); err != nil {
			return fmt.Errorf("removing %s: %w", s.what, err)
		}
	}
	svcs, err := c.CoreV1().Services(ns).List(ctx, sel)
	if err == nil {
		for _, s := range svcs.Items {
			_ = c.CoreV1().Services(ns).Delete(ctx, s.Name, del)
		}
	}
	sas, err := c.CoreV1().ServiceAccounts(ns).List(ctx, sel)
	if err == nil {
		for _, s := range sas.Items {
			_ = c.CoreV1().ServiceAccounts(ns).Delete(ctx, s.Name, del)
		}
	}
	return nil
}
