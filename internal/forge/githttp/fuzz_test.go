package githttp

import (
	"net/http"
	"testing"
)

// FuzzService reads the Git service out of any request path and query. Only the two smart-protocol
// services are ever accepted, so no path can pick another command to run.
func FuzzService(f *testing.F) {
	for _, s := range [][3]string{
		{"GET", "/r/info/refs", "service=git-upload-pack"},
		{"POST", "/r/git-receive-pack", ""},
		{"POST", "/r/../../git-upload-pack", ""},
		{"GET", "/r/info/refs", "service=git-upload-pack;rm+-rf"},
		{"POST", "/", ""},
		{"GET", "/x", "service=%00"},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, method, path, query string) {
		if method != http.MethodGet && method != http.MethodPost {
			method = http.MethodPost
		}
		r, err := http.NewRequest(method, "http://x.invalid/", nil)
		if err != nil {
			t.Skip()
		}
		r.URL.Path, r.URL.RawQuery = path, query
		svc, _, serr := Service(r)
		if serr == nil && svc != UploadPack && svc != ReceivePack {
			t.Fatalf("Service accepted %q", svc)
		}
	})
}
