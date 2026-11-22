package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/installer/kube"
)

// doctorNetwork says whether the cluster already has a system deciding network policy in the
// installation's namespace (Kubernetes NetworkPolicies that are not Launchway's own, or a network
// plugin's own policy objects), and what that means for the chart's policies. It reads the cluster
// with the kubeconfig, so it runs where `launchway install` runs, not inside the server pod.
func doctorNetwork(args []string) error {
	fs := flag.NewFlagSet("doctor network", flag.ExitOnError)
	ns := fs.String("namespace", "launchway", "the installation's namespace")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig file (default: KUBECONFIG, then ~/.kube/config)")
	kctx := fs.String("context", "", "kubeconfig context (default: the current one)")
	_ = fs.Parse(args)
	live, err := kube.Connect(*kubeconfig, *kctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	found := kube.DetectPolicyAuthority(ctx, live.CS, *ns)
	if len(found) == 0 {
		fmt.Printf("Network policy: nothing else decides it in %s; the chart's NetworkPolicies are the only ones (networkPolicy.enabled defaults to true)\n", *ns)
		return nil
	}
	fmt.Fprintf(os.Stdout, "Network policy: WARNING %s\n", kube.PolicyAuthorityNote(found))
	return nil
}
