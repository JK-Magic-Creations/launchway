package kube

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// Forward opens a port-forward from 127.0.0.1:local to the first ready pod of the server and keeps
// it open until ctx ends. It returns once the forward is ready, with a stop function.
func Forward(ctx context.Context, cfg *rest.Config, cs kubernetes.Interface, ns, selector string, local, remote int, out io.Writer) (stop func(), err error) {
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	var pod string
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning {
			pod = p.Name
			break
		}
	}
	if pod == "" {
		return nil, fmt.Errorf("no running Launchway server pod to forward to")
	}
	rt, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", ns, pod)
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: rt}, http.MethodPost, u)
	stopCh, readyCh := make(chan struct{}), make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("%d:%d", local, remote)}, stopCh, readyCh, io.Discard, out)
	if err != nil {
		return nil, err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- pf.ForwardPorts() }()
	select {
	case <-readyCh:
	case err := <-errCh:
		return nil, fmt.Errorf("the port-forward did not start: %w", err)
	case <-time.After(20 * time.Second):
		close(stopCh)
		return nil, fmt.Errorf("the port-forward did not become ready")
	case <-ctx.Done():
		close(stopCh)
		return nil, ctx.Err()
	}
	return func() {
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
	}, nil
}
