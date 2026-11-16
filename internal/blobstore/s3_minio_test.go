package blobstore

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests in this file run the S3 store against a real server, such as a
// throwaway MinIO. See the test-s3 target in the Makefile. Each test works
// under a random prefix of its own and deletes what it made.

// minioSetup returns the configuration and credentials of the test server,
// or skips the test when none is configured.
func minioSetup(t *testing.T) (S3Config, S3Credentials) {
	t.Helper()
	endpoint := os.Getenv("LAUNCHWAY_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set LAUNCHWAY_TEST_S3_ENDPOINT, LAUNCHWAY_TEST_S3_BUCKET, LAUNCHWAY_TEST_S3_ACCESS_KEY and LAUNCHWAY_TEST_S3_SECRET_KEY to run against MinIO or another S3 store")
	}
	cfg := S3Config{
		Endpoint: endpoint,
		Region:   os.Getenv("LAUNCHWAY_TEST_S3_REGION"),
		Bucket:   os.Getenv("LAUNCHWAY_TEST_S3_BUCKET"),
		Prefix:   "launchway-test-" + randomHex(6) + "/",
	}
	creds := S3Credentials{AccessKeyID: os.Getenv("LAUNCHWAY_TEST_S3_ACCESS_KEY"), SecretAccessKey: os.Getenv("LAUNCHWAY_TEST_S3_SECRET_KEY")}
	if cfg.Bucket == "" || creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		t.Fatal("LAUNCHWAY_TEST_S3_ENDPOINT is set: LAUNCHWAY_TEST_S3_BUCKET, LAUNCHWAY_TEST_S3_ACCESS_KEY and LAUNCHWAY_TEST_S3_SECRET_KEY are needed too")
	}
	if os.Getenv("LAUNCHWAY_TEST_S3_CREATE_BUCKET") == "1" {
		createBucketOnce(t, cfg, creds)
	}
	return cfg, creds
}

var (
	bucketOnce sync.Once
	bucketErr  error
)

// createBucketOnce creates the test bucket with a signed PUT, so no other
// client (such as mc) is needed. A bucket that already exists is fine.
func createBucketOnce(t *testing.T, cfg S3Config, creds S3Credentials) {
	t.Helper()
	bucketOnce.Do(func() {
		s, err := NewS3(cfg, creds)
		if err != nil {
			bucketErr = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _, err = s.do(ctx, s3Request{op: "create bucket", method: http.MethodPut})
		var se *S3Error
		if errors.As(err, &se) && (se.Code == "BucketAlreadyOwnedByYou" || se.Code == "BucketAlreadyExists") {
			err = nil
		}
		bucketErr = err
	})
	if bucketErr != nil {
		t.Fatalf("creating the test bucket %s: %v", cfg.Bucket, bucketErr)
	}
}

func TestS3MinIO(t *testing.T) {
	cfg, creds := minioSetup(t)
	s, err := NewS3(cfg, creds)
	must(t, err)
	t.Logf("using %s, bucket %s, prefix %s", cfg.Endpoint, cfg.Bucket, cfg.Prefix)
	testStore(t, s)

	// An upload in 8 MiB chunks, as runners send them, with the last one
	// smaller, each part sent through a store of its own (as if to another
	// server of the group).
	key := "minio/chunked.bin"
	data := randomBytes(2*(8<<20) + 12345)
	id, err := s.CreateUpload(ctx, key)
	must(t, err)
	var parts []Part
	for n, off := 1, 0; off < len(data); n++ {
		end := min(off+8<<20, len(data))
		other, err := NewS3(cfg, creds)
		must(t, err)
		etag, err := other.UploadPart(ctx, key, id, n, data[off:end])
		must(t, err)
		parts = append(parts, Part{Number: n, ETag: etag})
		off = end
	}
	must(t, s.CompleteUpload(ctx, key, id, parts))
	defer s.Delete(ctx, key)
	if got := readAll(t, s, key, 8<<20-10, 20); string(got) != string(data[8<<20-10:8<<20+10]) {
		t.Fatal("a read across the chunk boundary differs")
	}
	if size, err := s.Stat(ctx, key); err != nil || size != int64(len(data)) {
		t.Fatalf("Stat: %d, %v", size, err)
	}
}

func TestS3MinIOWrongSecret(t *testing.T) {
	cfg, creds := minioSetup(t)
	creds.SecretAccessKey += "-wrong"
	s, err := NewS3(cfg, creds)
	must(t, err)
	err = s.Check(ctx)
	var se *S3Error
	if !errors.As(err, &se) || se.Code != "SignatureDoesNotMatch" {
		t.Fatalf("got %v, want SignatureDoesNotMatch", err)
	}
	t.Logf("the error reads: %v", err)
	if strings.Contains(err.Error(), creds.SecretAccessKey) || strings.Contains(err.Error(), creds.AccessKeyID) {
		t.Fatalf("the error %q contains a credential", err)
	}
}

func TestS3MinIOMissingBucket(t *testing.T) {
	cfg, creds := minioSetup(t)
	cfg.Bucket = "launchway-missing-" + randomHex(6)
	s, err := NewS3(cfg, creds)
	must(t, err)
	err = s.Check(ctx)
	var se *S3Error
	if !errors.As(err, &se) || se.Code != "NoSuchBucket" {
		t.Fatalf("got %v, want NoSuchBucket", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a missing bucket matches ErrNotFound, as if only an object were missing")
	}
	t.Logf("the error reads: %v", err)
}
