package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func TestCLIVariables(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := source.NewManager(eng, filepath.Join(t.TempDir(), "data"), []source.Project{{Name: "shop", URL: t.TempDir(), DefaultBranch: "main"}}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	pol, _ := auth.NewPolicy([]auth.Binding{{Group: "ops", Role: auth.Operator}, {Group: "root", Role: auth.Admin}})
	lay := &settings.Layout{Levels: settings.DefaultLevels, ProjectUnit: map[string]string{"shop": ""}}
	s := &api.Server{Engine: eng, Projects: mgr, Log: quiet, RunnerToken: "r", Auth: &api.Auth{Policy: pol, IdleTimeout: time.Hour, Lifetime: time.Hour},
		Variables: settings.NewVariableService(settings.NewVariableStore(eng), func() (*settings.Layout, error) { return lay, nil })}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	now := time.Now().UnixMilli()
	clientFor := func(user, group string, out *bytes.Buffer) *client {
		if err := eng.SignIn(user, []string{group}, engine.Profile{}, "session-"+user, now, now+3600_000, 0); err != nil {
			t.Fatal(err)
		}
		tok := "lw_" + "clivar" + user + strings.Repeat("0", 20)
		if err := eng.CreateToken(engine.APIToken{ID: "tok_" + user, Name: user, User: user, CreatedAt: now, ExpiresAt: now + 86400_000}, auth.Hash(tok)); err != nil {
			t.Fatal(err)
		}
		return newClient(credentials{Server: srv.URL, Token: tok}, out)
	}
	var oo, ao bytes.Buffer
	otto, ada := clientFor("otto", "ops", &oo), clientFor("ada", "root", &ao)

	if err := ada.variablesSet([]string{"replicas", "--installation", "--type", "number", "--value", "1"}); err != nil {
		t.Fatal(err)
	}
	if err := otto.variablesSet([]string{"replicas", "--project", "shop", "--type", "number", "--value", "2"}); err != nil {
		t.Fatal(err)
	}
	if err := otto.variablesSet([]string{"replicas", "--installation", "--type", "number", "--value", "9"}); err == nil {
		t.Fatal("an operator set at the installation")
	}
	if err := ada.variablesSet([]string{"tok", "--installation", "--type", "secret", "--value", "vault:apps/x#token"}); err != nil {
		t.Fatal(err)
	}
	oo.Reset()
	if err := otto.variablesEffective([]string{"shop"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"replicas = 2  (number, from project shop)", "~~1~~  installation  (overridden)", "tok = vault:apps/x#token", "The nearest setting wins"} {
		if !strings.Contains(oo.String(), want) {
			t.Errorf("effective lacks %q:\n%s", want, oo.String())
		}
	}
	if err := otto.variablesLock([]string{"replicas", "--project", "shop", "--reason", "mine"}); err == nil {
		t.Fatal("an operator locked")
	}
	if err := ada.variablesLock([]string{"replicas", "--installation"}); err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("%v", err)
	}
	if err := ada.variablesLock([]string{"replicas", "--installation", "--reason", "fleet rule"}); err != nil {
		t.Fatal(err)
	}
	err = otto.variablesSet([]string{"replicas", "--project", "shop", "--environment", "prod", "--type", "number", "--value", "5"})
	if err == nil || !strings.Contains(err.Error(), "locked by the base (fleet rule)") {
		t.Fatalf("%v", err)
	}
	if err := ada.variablesUnlock([]string{"replicas", "--installation"}); err != nil {
		t.Fatal(err)
	}
	if err := ada.variablesDelete([]string{"replicas", "--installation"}); err != nil {
		t.Fatal(err)
	}
	if err := ada.variablesSet([]string{"x", "--type", "string", "--value", "a"}); err == nil {
		t.Fatal("no level was accepted")
	}
}
