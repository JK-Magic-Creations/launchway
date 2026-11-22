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

// The published examples of AWS Signature Version 4, so the signer is checked against the vendor, not against
// itself. Sources: the IAM user guide "Create a signed AWS API request" (the ListUsers example), the SigV4 test
// suite's vanilla cases (https://docs.aws.amazon.com/general/latest/gr/signature-v4-test-suite.html) and the S3 API
// reference "Examples: Signature Calculations in AWS Signature Version 4". All keys are the vendor's example keys.
func TestPublishedSigV4Examples(t *testing.T) {
	iam := Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	s3 := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	suite := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	s3day := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, service, method, url string
		creds                      Credentials
		headers                    map[string]string
		payload                    string
		at                         time.Time
		signedHeaders, signature   string
	}{
		{"iam ListUsers", "iam", "GET", "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", iam,
			map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"}, EmptySHA256, suite,
			"content-type;host;x-amz-date", "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"},
		{"get-vanilla", "service", "GET", "https://example.amazonaws.com/", iam, nil, EmptySHA256, suite,
			"host;x-amz-date", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"post-vanilla", "service", "POST", "https://example.amazonaws.com/", iam, nil, EmptySHA256, suite,
			"host;x-amz-date", "5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		{"s3 GetObject with a Range", "s3", "GET", "https://examplebucket.s3.amazonaws.com/test.txt", s3,
			map[string]string{"Range": "bytes=0-9", "X-Amz-Content-Sha256": EmptySHA256}, EmptySHA256, s3day,
			"host;range;x-amz-content-sha256;x-amz-date", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"s3 ListObjects with a query", "s3", "GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", s3,
			map[string]string{"X-Amz-Content-Sha256": EmptySHA256}, EmptySHA256, s3day,
			"host;x-amz-content-sha256;x-amz-date", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
		{"s3 GetBucketLifecycle", "s3", "GET", "https://examplebucket.s3.amazonaws.com/?lifecycle", s3,
			map[string]string{"X-Amz-Content-Sha256": EmptySHA256}, EmptySHA256, s3day,
			"host;x-amz-content-sha256;x-amz-date", "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"s3 PutObject with a $ in the key", "s3", "PUT", "https://examplebucket.s3.amazonaws.com/test%24file.text", s3,
			map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY",
				"X-Amz-Content-Sha256": "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072"},
			"44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072", s3day,
			"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class", "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, tt.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			got := Signer{Creds: tt.creds, Region: "us-east-1", Service: tt.service}.Sign(req, tt.payload, tt.at)
			if got.Signature != tt.signature || got.SignedHeaders != tt.signedHeaders {
				t.Errorf("signature %s (want %s), signed headers %s (want %s)\ncanonical request:\n%s", got.Signature, tt.signature, got.SignedHeaders, tt.signedHeaders, got.CanonicalRequest)
			}
			want := "AWS4-HMAC-SHA256 Credential=" + tt.creds.AccessKeyID + "/" + tt.at.Format("20060102") + "/us-east-1/" + tt.service + "/aws4_request, SignedHeaders=" + tt.signedHeaders + ", Signature=" + tt.signature
			if a := req.Header.Get("Authorization"); a != want {
				t.Errorf("authorization %q, want %q", a, want)
			}
		})
	}
}

// The key derivation step of the IAM user guide's example, on its own.
func TestSigningKeyDerivationExample(t *testing.T) {
	const secret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	toSign := "AWS4-HMAC-SHA256\n20150830T123600Z\n20150830/us-east-1/iam/aws4_request\nf536975d06c0309214f805bb90ccff089219ecd68b2577efef23edd43b7e1a59"
	if got := Signing(secret, "20150830", "us-east-1", "iam", toSign); got != "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7" {
		t.Errorf("%s", got)
	}
}

// Query parameters are sorted by name and then value and encoded as SigV4 asks; a valueless one gets "=".
func TestCanonicalQueryOrderAndEncoding(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"b=2&a=1":           "a=1&b=2",
		"a=2&a=1":           "a=1&a=2",
		"lifecycle":         "lifecycle=",
		"k=a b&k2=%7E":      "k=a%20b&k2=~",
		"Param2=v&Param1=v": "Param1=v&Param2=v",
		"a=%2F&b=/":         "a=%2F&b=%2F",
	} {
		if got := CanonicalQuery(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
