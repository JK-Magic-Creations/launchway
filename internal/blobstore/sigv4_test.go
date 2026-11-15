package blobstore

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The expected values below are copied from AWS's published examples, so
// the signer is checked against AWS, not against itself.

// iamCreds and s3Creds are the example credentials of the IAM and S3 docs.
var (
	iamCreds = S3Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	s3Creds  = S3Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
)

func TestSigV4AWSExamples(t *testing.T) {
	tests := []struct {
		name      string
		creds     S3Credentials
		service   string
		method    string
		url       string
		rawPath   string
		headers   map[string]string
		payload   string // hex SHA-256 of the body
		when      time.Time
		canonical string
		toSign    string
		signature string
	}{{
		// IAM user guide, "Signature Version 4 signing process" example:
		// GET https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08
		name:    "iam ListUsers",
		creds:   iamCreds,
		service: "iam",
		method:  "GET",
		url:     "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08",
		headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"},
		payload: emptySHA256,
		when:    time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC),
		canonical: "GET\n/\nAction=ListUsers&Version=2010-05-08\n" +
			"content-type:application/x-www-form-urlencoded; charset=utf-8\n" +
			"host:iam.amazonaws.com\n" +
			"x-amz-date:20150830T123600Z\n\n" +
			"content-type;host;x-amz-date\n" +
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		toSign: "AWS4-HMAC-SHA256\n20150830T123600Z\n20150830/us-east-1/iam/aws4_request\n" +
			"f536975d06c0309214f805bb90ccff089219ecd68b2577efef23edd43b7e1a59",
		signature: "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7",
	}, {
		// The SigV4 test suite's get-vanilla case.
		name:    "get-vanilla",
		creds:   iamCreds,
		service: "service",
		method:  "GET",
		url:     "https://example.amazonaws.com/",
		payload: emptySHA256,
		when:    time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC),
		canonical: "GET\n/\n\n" +
			"host:example.amazonaws.com\n" +
			"x-amz-date:20150830T123600Z\n\n" +
			"host;x-amz-date\n" +
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		toSign: "AWS4-HMAC-SHA256\n20150830T123600Z\n20150830/us-east-1/service/aws4_request\n" +
			"bb579772317eb040ac9ed261061d46c1f17a8133879d6129b6e1c25292927e63",
		signature: "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31",
	}, {
		// S3 API reference, "Examples: Signature Calculations in AWS
		// Signature Version 4", GET Object with a Range header.
		name:    "s3 GetObject range",
		creds:   s3Creds,
		service: "s3",
		method:  "GET",
		url:     "https://examplebucket.s3.amazonaws.com/test.txt",
		headers: map[string]string{"Range": "bytes=0-9", "X-Amz-Content-Sha256": emptySHA256},
		payload: emptySHA256,
		when:    time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC),
		canonical: "GET\n/test.txt\n\n" +
			"host:examplebucket.s3.amazonaws.com\n" +
			"range:bytes=0-9\n" +
			"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
			"x-amz-date:20130524T000000Z\n\n" +
			"host;range;x-amz-content-sha256;x-amz-date\n" +
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		toSign: "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\n" +
			"7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972",
		signature: "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
	}, {
		// The same page, PUT Object, with a '$' in the key that the
		// canonical URI encodes.
		name:    "s3 PutObject",
		creds:   s3Creds,
		service: "s3",
		method:  "PUT",
		url:     "https://examplebucket.s3.amazonaws.com/test$file.text",
		rawPath: "/test%24file.text",
		headers: map[string]string{
			"Date":                 "Fri, 24 May 2013 00:00:00 GMT",
			"X-Amz-Storage-Class":  "REDUCED_REDUNDANCY",
			"X-Amz-Content-Sha256": "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
		},
		payload: "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
		when:    time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC),
		canonical: "PUT\n/test%24file.text\n\n" +
			"date:Fri, 24 May 2013 00:00:00 GMT\n" +
			"host:examplebucket.s3.amazonaws.com\n" +
			"x-amz-content-sha256:44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072\n" +
			"x-amz-date:20130524T000000Z\n" +
			"x-amz-storage-class:REDUCED_REDUNDANCY\n\n" +
			"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class\n" +
			"44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
		toSign: "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\n" +
			"9e0e90d9c76de8fa5b200d8c849cd5b8dc7a3be3951ddb7f6a76b4158342019d",
		signature: "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
	}, {
		// The same page, GET Bucket (List Objects) with query parameters.
		name:    "s3 ListObjects",
		creds:   s3Creds,
		service: "s3",
		method:  "GET",
		url:     "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
		headers: map[string]string{"X-Amz-Content-Sha256": emptySHA256},
		payload: emptySHA256,
		when:    time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC),
		canonical: "GET\n/\nmax-keys=2&prefix=J\n" +
			"host:examplebucket.s3.amazonaws.com\n" +
			"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
			"x-amz-date:20130524T000000Z\n\n" +
			"host;x-amz-content-sha256;x-amz-date\n" +
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		toSign: "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\n" +
			"df57d21db20da04d7fa30298dd4488ba3a2b47ca3a489c74750e0f1e7df1b9b7",
		signature: "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, tt.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tt.rawPath != "" {
				req.URL.RawPath = tt.rawPath
			}
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			sg := signer{creds: tt.creds, region: "us-east-1", service: tt.service}
			got := sg.sign(req, tt.payload, tt.when)
			if got.canonicalRequest != tt.canonical {
				t.Errorf("canonical request:\n%s\nwant:\n%s", got.canonicalRequest, tt.canonical)
			}
			if got.stringToSign != tt.toSign {
				t.Errorf("string to sign:\n%s\nwant:\n%s", got.stringToSign, tt.toSign)
			}
			if got.signature != tt.signature {
				t.Errorf("signature %s, want %s", got.signature, tt.signature)
			}
			auth := req.Header.Get("Authorization")
			wantPrefix := "AWS4-HMAC-SHA256 Credential=" + tt.creds.AccessKeyID + "/" + tt.when.Format("20060102") + "/us-east-1/" + tt.service + "/aws4_request, SignedHeaders="
			if !strings.HasPrefix(auth, wantPrefix) || !strings.HasSuffix(auth, ", Signature="+tt.signature) {
				t.Errorf("Authorization header %q", auth)
			}
		})
	}
}

func TestSigV4SessionTokenIsSigned(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/x", nil)
	sg := signer{creds: S3Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TOKEN"}, region: "us-east-1", service: "s3"}
	got := sg.sign(req, emptySHA256, time.Now())
	if req.Header.Get("X-Amz-Security-Token") != "TOKEN" || !strings.Contains(got.signedHeaders, "x-amz-security-token") {
		t.Fatalf("session token not sent or not signed: %q", got.signedHeaders)
	}
}

func TestCanonicalQuery(t *testing.T) {
	tests := map[string]string{
		"":                                 "",
		"uploads":                          "uploads=",
		"uploads=":                         "uploads=",
		"uploadId=a%2Fb%2Bc&partNumber=12": "partNumber=12&uploadId=a%2Fb%2Bc",
		"b=2&a=2&a=1":                      "a=1&a=2&b=2",
		"x=a+b":                            "x=a%20b", // '+' in a query is a space
		"k=%7e~":                           "k=~~",
	}
	for in, want := range tests {
		if got := canonicalQuery(in); got != want {
			t.Errorf("canonicalQuery(%q) = %q, want %q", in, got, want)
		}
	}
	if got := encodeQuery("uploadId", "a/b+c=d", "partNumber", "3"); got != "uploadId=a%2Fb%2Bc%3Dd&partNumber=3" {
		t.Errorf("encodeQuery = %q", got)
	}
	if got := uriEncode("a b/ü~", false); got != "a%20b/%C3%BC~" {
		t.Errorf("uriEncode = %q", got)
	}
}
