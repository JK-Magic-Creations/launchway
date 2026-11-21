package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// FieldManager names the installer in the cluster's records of who set what.
const FieldManager = "launchway-installer"

// Applier puts objects into a cluster. The real one uses server-side apply, so running the
// installer again changes only what differs and never fights a person who edited something else.
type Applier interface {
	Apply(ctx context.Context, obj *unstructured.Unstructured) error
	// Remove deletes an object and waits until it is gone (used for a hook that must be recreated).
	Remove(ctx context.Context, obj *unstructured.Unstructured) error
}

// Live is a real cluster.
type Live struct {
	CS     kubernetes.Interface
	Dyn    dynamic.Interface
	Mapper meta.ResettableRESTMapper
	Config *rest.Config
	// Name is the kubeconfig context name; Server is the API address.
	Context, Server string
}

// Connect reads a kubeconfig (the default rules: KUBECONFIG, then ~/.kube/config) and picks a context.
// contextName "" is the kubeconfig's current context.
func Connect(kubeconfig, contextName string) (*Live, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	ov := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, ov)
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("reading the kubeconfig: %w", err)
	}
	name := contextName
	if name == "" {
		name = raw.CurrentContext
	}
	if name == "" {
		return nil, fmt.Errorf("no Kubernetes cluster is selected: there is no kubeconfig, or it has no current context. Point KUBECONFIG at one, or pass --context")
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("the kubeconfig context %q: %w", name, err)
	}
	cfg.Timeout = 30 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	m := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(cs.Discovery()))
	return &Live{CS: cs, Dyn: dyn, Mapper: m, Config: cfg, Context: name, Server: cfg.Host}, nil
}

func (l *Live) resource(obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	m, err := l.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if meta.IsNoMatchError(err) {
		l.Mapper.Reset()
		m, err = l.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	}
	if err != nil {
		return nil, fmt.Errorf("this cluster does not know %s (%s): %w", gvk.Kind, gvk.GroupVersion(), err)
	}
	if m.Scope.Name() == meta.RESTScopeNameNamespace {
		return l.Dyn.Resource(m.Resource).Namespace(obj.GetNamespace()), nil
	}
	return l.Dyn.Resource(m.Resource), nil
}

// Apply server-side applies one object.
func (l *Live) Apply(ctx context.Context, obj *unstructured.Unstructured) error {
	ri, err := l.resource(obj)
	if err != nil {
		return err
	}
	b, err := json.Marshal(obj.Object)
	if err != nil {
		return err
	}
	force := true
	_, err = ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, b, metav1.PatchOptions{FieldManager: FieldManager, Force: &force})
	if err != nil {
		return fmt.Errorf("%s %s: %w", obj.GetKind(), obj.GetName(), err)
	}
	return nil
}

// Remove deletes an object (background propagation) and waits until it is gone.
func (l *Live) Remove(ctx context.Context, obj *unstructured.Unstructured) error {
	ri, err := l.resource(obj)
	if err != nil {
		return err
	}
	prop := metav1.DeletePropagationBackground
	if err := ri.Delete(ctx, obj.GetName(), metav1.DeleteOptions{PropagationPolicy: &prop}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	for i := 0; i < 120; i++ {
		if _, err := ri.Get(ctx, obj.GetName(), metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s %s is still being deleted", obj.GetKind(), obj.GetName())
}

// Cluster is what the installer needs of a cluster: it reads, applies and, for the port-forward,
// has its connection settings. Live is the real one; tests use a fake.
type Cluster interface {
	Applier
	Clientset() kubernetes.Interface
	Name() string
	Address() string
	RESTConfig() *rest.Config
}

func (l *Live) Clientset() kubernetes.Interface { return l.CS }
func (l *Live) Name() string                    { return l.Context }
func (l *Live) Address() string                 { return l.Server }
func (l *Live) RESTConfig() *rest.Config        { return l.Config }
