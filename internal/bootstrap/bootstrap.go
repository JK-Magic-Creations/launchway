// Package bootstrap makes the Secrets a Launchway installation needs before its first server can
// start, once, and keeps them: the root key, the shared runner token, the bundled database's
// password and the one-time setup code.
//
// It runs as a Helm pre-install/pre-upgrade hook Job (`launchway bootstrap-secrets`), not in the
// chart's templates: a template cannot tell "the Secret is not there" from "this render cannot see
// the cluster" (helm template, a GitOps controller), so a key generated in a template changes at
// every render and replaces the real one. A Job runs against the real cluster, so it can look first.
//
// The rules are few and absolute. A Secret is created only if it is absent. An existing Secret is
// never modified, whatever it holds. No value is ever logged or returned.
package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Annotations put on every Secret made here. keep is the equivalent of Helm's resource policy:
// these Secrets are not Helm's (the Job made them), so `helm uninstall` and a GitOps prune never
// delete them; the annotation says that is on purpose.
const (
	AnnotationKeep      = "helm.sh/resource-policy"
	AnnotationCreatedBy = "launchway.dev/created-by"
	createdBy           = "bootstrap-secrets"
)

// Generator makes the value of a Secret's key, reading randomness from r.
type Generator func(r io.Reader) (string, error)

// Spec is one Secret to make if it is absent.
type Spec struct {
	Name     string
	Key      string
	Generate Generator
}

// Options say what to make and where.
type Options struct {
	Namespace string
	Specs     []Spec
	// Labels are put on every Secret made.
	Labels map[string]string
	Log    *slog.Logger
	// Rand is the source of randomness; default crypto/rand.
	Rand io.Reader
	// Attempts and Backoff bound the retries of a call that failed for a reason that may pass
	// (an API server restarting). Defaults 5 and 2s.
	Attempts int
	Backoff  time.Duration
}

// Outcome of one Secret.
type Outcome string

const (
	Created Outcome = "created"
	Present Outcome = "already present"
)

// Result names a Secret and what happened to it. It never holds a value.
type Result struct {
	Name    string
	Outcome Outcome
}

// Ensure makes every Secret in o.Specs that does not exist. It is safe to run any number of times,
// concurrently or not: when another writer wins the race to create a Secret, that Secret is kept and
// Ensure reports it present. An existing Secret that lacks the named key is an error (it is not
// changed): a server would otherwise fail to start with a less clear message.
func Ensure(ctx context.Context, cs kubernetes.Interface, o Options) ([]Result, error) {
	if o.Namespace == "" {
		return nil, errors.New("bootstrap: a namespace is required")
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Attempts < 1 {
		o.Attempts = 5
	}
	if o.Backoff == 0 {
		o.Backoff = 2 * time.Second
	}
	seen := map[string]bool{}
	for _, s := range o.Specs {
		if s.Name == "" || s.Key == "" || s.Generate == nil {
			return nil, fmt.Errorf("bootstrap: a Secret needs a name, a key and a generator (got name %q, key %q)", s.Name, s.Key)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("bootstrap: Secret %s is named twice", s.Name)
		}
		seen[s.Name] = true
	}
	var out []Result
	for _, s := range o.Specs {
		r, err := ensureOne(ctx, cs, o, s)
		if err != nil {
			return out, err
		}
		o.Log.Info("secret", "name", s.Name, "outcome", string(r.Outcome))
		out = append(out, r)
	}
	return out, nil
}

func ensureOne(ctx context.Context, cs kubernetes.Interface, o Options, s Spec) (Result, error) {
	api := cs.CoreV1().Secrets(o.Namespace)
	existing, err := retry(ctx, o, func() (*corev1.Secret, error) {
		sec, err := api.Get(ctx, s.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return sec, err
	})
	if err != nil {
		return Result{}, fmt.Errorf("reading Secret %s: %w", s.Name, err)
	}
	if existing != nil {
		return checkKey(existing, s)
	}
	val, err := s.Generate(o.Rand)
	if err != nil {
		return Result{}, fmt.Errorf("making the value for Secret %s: %w", s.Name, err)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        s.Name,
			Namespace:   o.Namespace,
			Labels:      o.Labels,
			Annotations: map[string]string{AnnotationKeep: "keep", AnnotationCreatedBy: createdBy},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{s.Key: []byte(val)},
	}
	raced := false
	_, err = retry(ctx, o, func() (*corev1.Secret, error) {
		_, err := api.Create(ctx, sec, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			raced = true
			return nil, nil
		}
		return nil, err
	})
	if err != nil {
		return Result{}, fmt.Errorf("creating Secret %s: %w", s.Name, err)
	}
	if !raced {
		return Result{Name: s.Name, Outcome: Created}, nil
	}
	// Another writer (a second Job, a person) made it first: keep theirs, never overwrite.
	theirs, err := retry(ctx, o, func() (*corev1.Secret, error) { return api.Get(ctx, s.Name, metav1.GetOptions{}) })
	if err != nil {
		return Result{}, fmt.Errorf("reading Secret %s: %w", s.Name, err)
	}
	return checkKey(theirs, s)
}

func checkKey(sec *corev1.Secret, s Spec) (Result, error) {
	if len(sec.Data[s.Key]) == 0 {
		return Result{}, fmt.Errorf("the Secret %s exists but has no %q key (or it is empty); it is not changed. Add the key, or delete the Secret so a new one is made", s.Name, s.Key)
	}
	return Result{Name: s.Name, Outcome: Present}, nil
}

// retry runs f, repeating it when the error may pass. NotFound, AlreadyExists (handled by the
// caller), Forbidden and Invalid are final.
func retry(ctx context.Context, o Options, f func() (*corev1.Secret, error)) (*corev1.Secret, error) {
	var err error
	for i := 0; i < o.Attempts; i++ {
		var sec *corev1.Secret
		sec, err = f()
		if err == nil {
			return sec, nil
		}
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
			return nil, err
		}
		if i == o.Attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(o.Backoff):
		}
	}
	return nil, err
}

// RootKey is 32 random bytes in standard base64 with a trailing newline: the text of a root key file,
// exactly as `launchway encryption init` writes it and the keyring reads it (keyring.ParseKey).
func RootKey(r io.Reader) (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b) + "\n", nil
}

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// Token returns a Generator of n random letters and digits (about 5.95 bits each). Letters and
// digits only, so the value is safe in a URL, a header and a shell.
func Token(n int) Generator {
	return func(r io.Reader) (string, error) { return pick(r, alnum, n) }
}

// SetupCodeAlphabet leaves out the characters people confuse (0 O 1 I L).
const SetupCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// SetupCode is 25 characters from a 31-symbol alphabet (about 124 bits), written in five groups of
// five: ABCDE-FGHJK-... A person types it once, into the browser, to become the first administrator.
func SetupCode(r io.Reader) (string, error) {
	s, err := pick(r, SetupCodeAlphabet, 25)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, 5)
	for i := 0; i < 25; i += 5 {
		parts = append(parts, s[i:i+5])
	}
	return strings.Join(parts, "-"), nil
}

// pick draws n characters uniformly (no modulo bias) from alphabet.
func pick(r io.Reader, alphabet string, n int) (string, error) {
	limit := big.NewInt(int64(len(alphabet)))
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(r, limit)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out), nil
}
