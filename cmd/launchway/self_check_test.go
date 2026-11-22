package main

import (
	"os/exec"
	"testing"
)

func TestSelfCheckHostedReadRuns(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if err := cmdSelfCheck([]string{"hosted-read"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdSelfCheck([]string{"nonsense"}); err == nil {
		t.Fatal("an unknown check was accepted")
	}
}
