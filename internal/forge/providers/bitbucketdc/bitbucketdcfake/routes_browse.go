package bitbucketdcfake

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

func parseMultipart(r *http.Request, raw []byte) (map[string]string, map[string][]byte, string) {
	mt, p, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		return nil, nil, "a file edit is sent as multipart/form-data"
	}
	mr := multipart.NewReader(strings.NewReader(string(raw)), p["boundary"])
	fields, files := map[string]string{}, map[string][]byte{}
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(part)
		if part.FileName() != "" {
			files[part.FormName()] = b
		} else {
			fields[part.FormName()] = string(b)
		}
	}
	return fields, files, ""
}

// routeBrowse: PUT .../browse/{path}, "Edit file": multipart with the form
// fields branch, message, sourceBranch and sourceCommitId and the file in the part named content.
func (h *Host) routeBrowse(r *http.Request, sub string, in map[string]any, raw []byte) (int, any, string) {
	if r.Method != "PUT" || !strings.HasPrefix(sub, "/browse/") {
		return 0, nil, ""
	}
	if r.Header.Get("X-Atlassian-Token") != "no-check" {
		return 0, nil, "a multipart request passes the XSRF check with X-Atlassian-Token: no-check"
	}
	path := unesc(strings.TrimPrefix(sub, "/browse/"))
	fields, files, bad := parseMultipart(r, raw)
	if bad != "" {
		return 0, nil, bad
	}
	for k := range fields {
		if k != "message" && k != "branch" && k != "sourceBranch" && k != "sourceCommitId" {
			return 0, nil, "form field " + k + " is not documented for this call"
		}
	}
	content, ok := files["content"]
	if len(files) != 1 || !ok || fields["message"] == "" || fields["branch"] == "" {
		return 0, nil, "an edit has a message, a branch and one file in the part named content"
	}
	br, src := fields["branch"], fields["sourceBranch"]
	if src != "" {
		if _, exists := h.branches[br]; exists {
			return 409, errBody("the branch to create exists"), ""
		}
		if _, ok := h.branches[src]; !ok {
			return 404, errBody("the source branch does not exist"), ""
		}
		h.branches[br] = h.branches[src]
		h.files[br], h.fileCommit[br] = map[string][]byte{}, map[string]string{}
		for k, v := range h.files[src] {
			h.files[br][k], h.fileCommit[br][k] = v, h.fileCommit[src][k]
		}
	} else if _, ok := h.branches[br]; !ok {
		return 404, errBody("no such branch"), ""
	}
	last, have := h.fileCommit[br][path]
	if have && fields["sourceCommitId"] != last || !have && fields["sourceCommitId"] != "" {
		return 409, errBody("the file has changed since sourceCommitId"), ""
	}
	h.files[br][path] = content
	c := h.sha()
	h.branches[br], h.fileCommit[br][path] = c, c
	return 200, map[string]any{"id": c, "displayId": c[:11], "message": fields["message"], "parents": []any{}}, ""
}
