package config

import "testing"

func TestDefaultAutomationAccountsNameTheDependencyUpdater(t *testing.T) {
	found := false
	for _, a := range DefaultAutomationAccounts {
		found = found || a == "launchway-updater*"
	}
	if !found {
		t.Fatal("the dependency updater's account is not on the default automation list, so its pull requests would run as a person's")
	}
}
