package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jk-magic-creations/launchway/internal/bootstrap"
	"github.com/jk-magic-creations/launchway/internal/logsafe"
)

func init() { commands["bootstrap-secrets"] = cmdBootstrapSecrets }

const bootstrapSecretsUsage = `Usage: launchway bootstrap-secrets --namespace NS [--root-key NAME] [--runner-token NAME]
                                   [--db-password NAME] [--deploy-pool-token NAME] [--setup-code NAME]
                                   [--label key=value ...]

Makes the Kubernetes Secrets a new installation needs, each only if it does not exist yet. An existing
Secret is never changed, and no value is ever printed. Safe to run again, and at the same time as itself.
The Helm chart runs it as a pre-install and pre-upgrade hook Job; it needs only get and create on Secrets.

  --root-key NAME          the root key (key root.key): 32 random bytes in base64, the text of a root key file
  --runner-token NAME      the token runners present to servers (key token)
  --db-password NAME       the bundled database's password (key password)
  --deploy-pool-token NAME the Ramp's token for launches (key token)
  --setup-code NAME        the one-time code for the first administrator (key code)

Each flag also reads an environment variable: LAUNCHWAY_BOOTSTRAP_NAMESPACE, _ROOT_KEY, _RUNNER_TOKEN,
_DB_PASSWORD, _DEPLOY_POOL_TOKEN, _SETUP_CODE and _LABELS (comma-separated key=value). A flag left empty
skips that Secret. Outside a cluster it uses your kubeconfig, so name the namespace with care.
`

type labelFlag map[string]string

func (l labelFlag) String() string { return fmt.Sprint(map[string]string(l)) }
func (l labelFlag) Set(v string) error {
	for _, kv := range strings.Split(v, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		k, val, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("label %q: write it as key=value", kv)
		}
		l[k] = val
	}
	return nil
}

func cmdBootstrapSecrets(args []string) error {
	return runBootstrapSecrets(args, os.Stderr, func() (kubernetes.Interface, error) {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
		}
		if err != nil {
			return nil, fmt.Errorf("no Kubernetes API to talk to: %w", err)
		}
		return kubernetes.NewForConfig(cfg)
	})
}

// runBootstrapSecrets is cmdBootstrapSecrets with its client and log destination given, for tests.
func runBootstrapSecrets(args []string, logTo io.Writer, client func() (kubernetes.Interface, error)) error {
	fs := flag.NewFlagSet("bootstrap-secrets", flag.ContinueOnError)
	fs.SetOutput(logTo)
	fs.Usage = func() { fmt.Fprint(logTo, bootstrapSecretsUsage) }
	env := func(k string) string { return os.Getenv("LAUNCHWAY_BOOTSTRAP_" + k) }
	ns := fs.String("namespace", env("NAMESPACE"), "namespace of the Secrets")
	root := fs.String("root-key", env("ROOT_KEY"), "")
	runner := fs.String("runner-token", env("RUNNER_TOKEN"), "")
	db := fs.String("db-password", env("DB_PASSWORD"), "")
	pool := fs.String("deploy-pool-token", env("DEPLOY_POOL_TOKEN"), "")
	code := fs.String("setup-code", env("SETUP_CODE"), "")
	labels := labelFlag{}
	_ = labels.Set(env("LABELS"))
	fs.Var(labels, "label", "a label for the Secrets made, key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitErr(exitUsage)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(logTo, "bootstrap-secrets: unexpected argument %q\n", fs.Arg(0))
		return exitErr(exitUsage)
	}
	if *ns == "" {
		fmt.Fprintln(logTo, "bootstrap-secrets: --namespace is required")
		return exitErr(exitUsage)
	}
	specs := []bootstrap.Spec{}
	add := func(name, key string, g bootstrap.Generator) {
		if name != "" {
			specs = append(specs, bootstrap.Spec{Name: name, Key: key, Generate: g})
		}
	}
	add(*root, "root.key", bootstrap.RootKey)
	add(*runner, "token", bootstrap.Token(48))
	add(*db, "password", bootstrap.Token(32))
	add(*pool, "token", bootstrap.Token(48))
	add(*code, "code", bootstrap.SetupCode)
	cs, err := client()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, err = bootstrap.Ensure(ctx, cs, bootstrap.Options{
		Namespace: *ns, Specs: specs, Labels: labels,
		Log: slog.New(logsafe.Handler(slog.NewTextHandler(logTo, nil))),
	})
	return err
}
