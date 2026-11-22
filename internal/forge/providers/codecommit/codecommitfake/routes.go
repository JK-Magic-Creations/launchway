package codecommitfake

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/repos/"):
		h.serveGit(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/" && r.URL.Query().Get("Action") != "":
		h.serveSTS(w, r)
	default:
		h.serveAPI(w, r)
	}
}

// spec is what the reference lists for an action's request: required and optional fields.
type spec struct{ req, opt []string }

var specs = map[string]spec{
	"ListRepositories":             {nil, []string{"nextToken", "order", "sortBy"}},
	"BatchGetRepositories":         {[]string{"repositoryNames"}, nil},
	"ListBranches":                 {[]string{"repositoryName"}, []string{"nextToken"}},
	"GetBranch":                    {[]string{"repositoryName", "branchName"}, nil},
	"CreateBranch":                 {[]string{"branchName", "commitId", "repositoryName"}, nil},
	"GetCommit":                    {[]string{"commitId", "repositoryName"}, nil},
	"ListPullRequests":             {[]string{"repositoryName"}, []string{"authorArn", "maxResults", "nextToken", "pullRequestStatus"}},
	"GetPullRequest":               {[]string{"pullRequestId"}, nil},
	"CreatePullRequest":            {[]string{"targets", "title"}, []string{"clientRequestToken", "description"}},
	"UpdatePullRequestStatus":      {[]string{"pullRequestId", "pullRequestStatus"}, nil},
	"UpdatePullRequestTitle":       {[]string{"pullRequestId", "title"}, nil},
	"UpdatePullRequestDescription": {[]string{"pullRequestId", "description"}, nil},
	"PostCommentForPullRequest": {[]string{"afterCommitId", "beforeCommitId", "content", "pullRequestId", "repositoryName"},
		[]string{"clientRequestToken", "location"}},
	"UpdateComment":                                    {[]string{"commentId", "content"}, nil},
	"GetPullRequestApprovalStates":                     {[]string{"pullRequestId", "revisionId"}, nil},
	"ListAssociatedApprovalRuleTemplatesForRepository": {[]string{"repositoryName"}, []string{"maxResults", "nextToken"}},
	"GetApprovalRuleTemplate":                          {[]string{"approvalRuleTemplateName"}, nil},
	"PutFile": {[]string{"branchName", "fileContent", "filePath", "repositoryName"},
		[]string{"commitMessage", "email", "fileMode", "name", "parentCommitId"}},
}

// wire is a decoded request: its fields by name.
type wire map[string]json.RawMessage

func (m wire) str(k string) string {
	var s string
	_ = json.Unmarshal(m[k], &s)
	return s
}

var repoNameRE = regexp.MustCompile(`^[\w.-]{1,100}$`)

func (h *Host) serveAPI(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	tgt := r.Header.Get("X-Amz-Target")
	action := strings.TrimPrefix(tgt, "CodeCommit_20150413.")
	if r.Method != http.MethodPost || r.URL.Path != "/" || r.URL.RawQuery != "" || action == tgt {
		h.violate(w, http.StatusNotFound, "UnknownOperationException", "%s %s with target %q is not a call of the API", r.Method, r.URL.Path, tgt)
		return
	}
	if r.Header.Get("Content-Type") != "application/x-amz-json-1.1" {
		h.violate(w, http.StatusBadRequest, "SerializationException", "the content type is not application/x-amz-json-1.1")
		return
	}
	who, ok := h.verify(w, r, body, "codecommit")
	if !ok {
		return
	}
	h.record("POST " + tgt)
	sp, known := specs[action]
	if !known {
		h.violate(w, http.StatusNotFound, "UnknownOperationException", "the action %q is not one the fake implements", action)
		return
	}
	var in wire
	if err := json.Unmarshal(body, &in); err != nil {
		h.violate(w, http.StatusBadRequest, "SerializationException", "the body is not a JSON object")
		return
	}
	allowed := map[string]bool{}
	for _, k := range append(append([]string{}, sp.req...), sp.opt...) {
		allowed[k] = true
	}
	for k := range in {
		if !allowed[k] {
			h.violate(w, http.StatusBadRequest, "SerializationException", "%s has no field %q", action, k)
			return
		}
	}
	for _, k := range sp.req {
		if _, ok := in[k]; !ok {
			h.violate(w, http.StatusBadRequest, "ValidationException", "%s requires %s", action, k)
			return
		}
	}
	if !h.typed(w, action, in) {
		return
	}
	if h.Denied[action] {
		h.fail(w, http.StatusForbidden, "AccessDeniedException", "User: %s is not authorized to perform: codecommit:%s", who.arn, action)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out, status, typ, msg := h.dispatch(action, in, who) //nolint:contextcheck // the fake runs git on its own repository and has no request context to give it
	if typ != "" {
		h.fail(w, status, typ, "%s", msg)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("x-amzn-RequestId", "0728aaa8-EXAMPLE")
	_ = json.NewEncoder(w).Encode(out)
}

// typed checks the types and limits of the fields the reference states.
func (h *Host) typed(w http.ResponseWriter, action string, in wire) bool {
	strs := []string{"nextToken", "order", "sortBy", "repositoryName", "branchName", "commitId", "authorArn", "pullRequestStatus", "pullRequestId", "title", "description",
		"clientRequestToken", "afterCommitId", "beforeCommitId", "content", "commentId", "revisionId", "approvalRuleTemplateName", "filePath", "commitMessage",
		"email", "fileMode", "name", "parentCommitId", "fileContent"}
	for _, k := range strs {
		if raw, ok := in[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				h.violate(w, http.StatusBadRequest, "SerializationException", "%s.%s must be a string", action, k)
				return false
			}
		}
	}
	bad := func(f string, a ...any) bool {
		h.violate(w, http.StatusBadRequest, "ValidationException", f, a...)
		return false
	}
	if raw, ok := in["maxResults"]; ok {
		var n int
		if json.Unmarshal(raw, &n) != nil || n < 1 {
			return bad("maxResults must be a positive integer")
		}
	}
	if rn := in.str("repositoryName"); in["repositoryName"] != nil && !repoNameRE.MatchString(rn) {
		return bad("repositoryName must match [\\w\\.-]+ and be at most 100 characters")
	}
	if len([]rune(in.str("title"))) > 150 {
		return bad("the title is at most 150 characters")
	}
	if len([]rune(in.str("description"))) > 10240 || len([]rune(in.str("content"))) > 10240 {
		return bad("a description or a comment is at most 10,240 characters")
	}
	if in["content"] != nil && in.str("content") == "" {
		return bad("CommentContentRequiredException: the comment is empty")
	}
	if st := in.str("pullRequestStatus"); in["pullRequestStatus"] != nil && st != "OPEN" && st != "CLOSED" {
		return bad("pullRequestStatus is OPEN or CLOSED")
	}
	if len(in.str("branchName")) > 256 {
		return bad("branchName is at most 256 characters")
	}
	if raw, ok := in["repositoryNames"]; ok {
		var names []string
		if json.Unmarshal(raw, &names) != nil || len(names) == 0 || len(names) > 100 {
			return bad("repositoryNames is 1 to 100 strings")
		}
	}
	if in["fileContent"] != nil {
		b, err := base64.StdEncoding.DecodeString(in.str("fileContent"))
		if err != nil {
			return bad("fileContent is base64")
		}
		if len(b) > 6291456 {
			return bad("FileContentSizeLimitExceededException: files are at most 6 MB")
		}
	}
	return true
}

// dispatch runs one action under the lock; typ is the error's name when it failed.
func (h *Host) dispatch(action string, in wire, who caller) (out any, status int, typ, msg string) {
	if rn := in.str("repositoryName"); rn != "" && rn != Repo {
		return nil, 400, "RepositoryDoesNotExistException", fmt.Sprintf("%s does not exist", rn)
	}
	switch action {
	case "ListRepositories":
		return map[string]any{"repositories": []map[string]string{{"repositoryId": repoID, "repositoryName": Repo}}}, 0, "", ""
	case "BatchGetRepositories":
		var names []string
		_ = json.Unmarshal(in["repositoryNames"], &names)
		var metas []any
		missing := []string{}
		for _, n := range names {
			if n == Repo {
				metas = append(metas, h.metadata())
			} else {
				missing = append(missing, n)
			}
		}
		if metas == nil {
			metas = []any{}
		}
		return map[string]any{"repositories": metas, "repositoriesNotFound": missing}, 0, "", ""
	case "ListBranches", "GetBranch", "CreateBranch", "GetCommit", "PutFile":
		return h.codeAction(action, in)
	}
	return h.pullAction(action, in, who)
}

const repoID = "f7579e13-b83e-4027-aaef-650c0EXAMPLE"

func (h *Host) metadata() map[string]any {
	def := ""
	if h.Branch("main") != "" {
		def = "main"
	}
	return map[string]any{"accountId": Account, "Arn": "arn:aws:codecommit:" + Region + ":" + Account + ":" + Repo,
		"cloneUrlHttp": h.URL + "/v1/repos/" + Repo, "cloneUrlSsh": "ssh://git-codecommit." + Region + ".amazonaws.com/v1/repos/" + Repo,
		"creationDate": 1441725693.583, "lastModifiedDate": 1441725693.583, "defaultBranch": def, "repositoryDescription": "fake", "repositoryId": repoID, "repositoryName": Repo}
}

func (h *Host) codeAction(action string, in wire) (any, int, string, string) {
	switch action {
	case "ListBranches":
		out, _ := h.git(nil, "", "for-each-ref", "--format=%(refname:short)", "refs/heads/")
		bs := append([]string{}, strings.Fields(out)...)
		return map[string]any{"branches": bs}, 0, "", ""
	case "GetBranch":
		c := h.Branch(in.str("branchName"))
		if c == "" {
			return nil, 400, "BranchDoesNotExistException", "the branch does not exist"
		}
		return map[string]any{"branch": map[string]string{"branchName": in.str("branchName"), "commitId": c}}, 0, "", ""
	case "CreateBranch":
		if h.Branch(in.str("branchName")) != "" {
			return nil, 400, "BranchNameExistsException", "the branch exists"
		}
		if _, err := h.git(nil, "", "rev-parse", "--verify", "-q", in.str("commitId")+"^{commit}"); err != nil {
			return nil, 400, "CommitDoesNotExistException", "no such commit"
		}
		if _, err := h.git(nil, "", "update-ref", "refs/heads/"+in.str("branchName"), in.str("commitId")); err != nil {
			return nil, 400, "InvalidBranchNameException", "the reference name is not valid"
		}
		return map[string]any{}, 0, "", ""
	case "GetCommit":
		return h.getCommit(in.str("commitId"))
	}
	return h.putFile(in)
}

func (h *Host) getCommit(id string) (any, int, string, string) {
	if len(id) != 40 {
		return nil, 400, "InvalidCommitIdException", "a commit id is the full 40 characters"
	}
	out, err := h.git(nil, "", "log", "-1", "--format=%H%x00%P%x00%an%x00%ae%x00%at %ai%x00%B", id)
	if err != nil {
		return nil, 400, "CommitIdDoesNotExistException", "no such commit"
	}
	f := strings.SplitN(out, "\x00", 6)
	date := strings.Fields(f[4])
	zone := "+0000"
	if len(date) >= 3 {
		zone = date[len(date)-1]
	}
	who := map[string]string{"name": f[2], "email": f[3], "date": date[0] + " " + zone}
	return map[string]any{"commit": map[string]any{"commitId": f[0], "additionalData": "", "author": who, "committer": who, "parents": strings.Fields(f[1]),
		"message": f[5] + "\n", "treeId": "0000"}}, 0, "", ""
}

func (h *Host) putFile(in wire) (any, int, string, string) {
	branch := in.str("branchName")
	tip := h.Branch(branch)
	if tip == "" {
		return nil, 400, "BranchDoesNotExistException", "the branch does not exist"
	}
	if in.str("parentCommitId") == "" {
		return nil, 400, "ParentCommitIdRequiredException", "a parent commit id is required"
	}
	if in.str("parentCommitId") != tip {
		return nil, 400, "ParentCommitIdOutdatedException", "the parent commit is not the tip of the branch"
	}
	content, _ := base64.StdEncoding.DecodeString(in.str("fileContent"))
	if len(content) == 0 {
		return nil, 400, "FileContentRequiredException", "empty files cannot be added"
	}
	if mode := in.str("fileMode"); mode != "" && mode != "NORMAL" && mode != "EXECUTABLE" && mode != "SYMLINK" {
		return nil, 400, "InvalidFileModeException", "bad mode"
	}
	p := in.str("filePath")
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return nil, 400, "InvalidPathException", "bad path"
	}
	if old, ok := h.File(branch, p); ok && old == strings.TrimRight(string(content), "\n") {
		return nil, 400, "SameFileContentException", "the content is the same"
	}
	id, err := h.commitFile(branch, p, content, in.str("commitMessage"))
	if err != nil {
		return nil, 500, "InternalFailure", err.Error()
	}
	return map[string]any{"blobId": "b", "commitId": id, "treeId": "t"}, 0, "", ""
}
