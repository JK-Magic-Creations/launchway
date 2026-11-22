package awssig

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Sign and Signing agree: the Authorization header's signature is the last step applied to the string to sign.
func TestSignAndSigningAgree(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://codecommit.us-east-1.amazonaws.com/", strings.NewReader("{}"))
	req.Header.Set("X-Amz-Target", "CodeCommit_20150413.ListRepositories")
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	at := time.Date(2026, 11, 22, 10, 11, 12, 0, time.UTC)
	s := Signer{Creds: Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret", SessionToken: "tok"}, Region: "us-east-1", Service: "codecommit"}
	sig := s.Sign(req, HexSHA256([]byte("{}")), at)
	if want := Signing("secret", "20261122", "us-east-1", "codecommit", sig.StringToSign); sig.Signature != want {
		t.Fatalf("%s != %s", sig.Signature, want)
	}
	if sig.SignedHeaders != "content-type;host;x-amz-date;x-amz-security-token;x-amz-target" {
		t.Errorf("signed headers %s", sig.SignedHeaders)
	}
	if !strings.HasPrefix(sig.StringToSign, Algorithm+"\n20261122T101112Z\n"+Scope("20261122", "us-east-1", "codecommit")+"\n") {
		t.Errorf("string to sign %q", sig.StringToSign)
	}
	if got := req.Header.Get("Authorization"); !strings.Contains(got, "Credential=AKIAEXAMPLE/20261122/us-east-1/codecommit/aws4_request") || !strings.HasSuffix(got, "Signature="+sig.Signature) {
		t.Errorf("authorization %q", got)
	}
}
