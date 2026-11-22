package codecommitfake

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type approval struct{ arn, state string }

type pull struct {
	id        int
	title     string
	desc      string
	status    string
	author    string
	src, dst  string // full reference names
	merged    bool
	revision  int
	approvals []approval
	token     string
}

type template struct{ name, content string }

type comment struct {
	id, content, author string
	pr                  int
}

func (h *Host) addPull(src, dst, title string) int {
	p := &pull{id: h.nextPR, title: title, status: "OPEN", author: "arn:aws:iam::" + Account + ":user/Jane_Doe", src: "refs/heads/" + src, dst: "refs/heads/" + dst, revision: 1}
	h.nextPR++
	h.prs = append(h.prs, p)
	return p.id
}

// Merge marks a pull request merged and closed (a test does what a person does on the host).
func (h *Host) Merge(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.pull(strconv.Itoa(id)); p != nil {
		p.status, p.merged = "CLOSED", true
	}
}

// Push moves a pull request's source branch (a revision of the pull request).
func (h *Host) Push(id int, file string) {
	h.mu.Lock()
	p := h.pull(strconv.Itoa(id))
	h.mu.Unlock()
	if _, err := h.commitFile(strings.TrimPrefix(p.src, "refs/heads/"), file, []byte(file+"\n"), "push "+file); err != nil {
		panic(err)
	}
	h.mu.Lock()
	p.revision++
	h.mu.Unlock()
}

func (h *Host) pull(id string) *pull {
	for _, p := range h.prs {
		if strconv.Itoa(p.id) == id {
			return p
		}
	}
	return nil
}

func (h *Host) pullJSON(p *pull) map[string]any {
	sc, _ := h.git(nil, "", "rev-parse", p.src)
	dc, _ := h.git(nil, "", "rev-parse", p.dst)
	mb, _ := h.git(nil, "", "merge-base", p.src, p.dst)
	return map[string]any{"pullRequestId": strconv.Itoa(p.id), "title": p.title, "description": p.desc, "pullRequestStatus": p.status, "authorArn": p.author,
		"clientRequestToken": p.token, "creationDate": 1508962823.285, "lastActivityDate": 1508962823.285, "revisionId": fmt.Sprintf("rev-%d-%d", p.id, p.revision),
		"approvalRules": []any{},
		"pullRequestTargets": []any{map[string]any{"repositoryName": Repo, "sourceReference": p.src, "destinationReference": p.dst, "sourceCommit": sc,
			"destinationCommit": dc, "mergeBase": mb, "mergeMetadata": map[string]any{"isMerged": p.merged}}}}
}

func (h *Host) pullAction(action string, in wire, who caller) (any, int, string, string) {
	const missing = "PullRequestDoesNotExistException"
	switch action {
	case "ListPullRequests":
		ids := []string{}
		for _, p := range h.prs {
			if in["pullRequestStatus"] == nil || p.status == in.str("pullRequestStatus") {
				ids = append(ids, strconv.Itoa(p.id))
			}
		}
		return map[string]any{"pullRequestIds": ids}, 0, "", ""
	case "GetPullRequest", "UpdatePullRequestStatus", "UpdatePullRequestTitle", "UpdatePullRequestDescription", "GetPullRequestApprovalStates":
		p := h.pull(in.str("pullRequestId"))
		if p == nil {
			return nil, 400, missing, "no such pull request"
		}
		return h.onPull(action, in, p)
	case "CreatePullRequest":
		return h.createPull(in, who)
	case "PostCommentForPullRequest":
		p := h.pull(in.str("pullRequestId"))
		if p == nil {
			return nil, 400, missing, "no such pull request"
		}
		sc, _ := h.git(nil, "", "rev-parse", p.src)
		dc, _ := h.git(nil, "", "rev-parse", p.dst)
		if in.str("afterCommitId") != sc || in.str("beforeCommitId") != dc {
			return nil, 400, "CommitDoesNotExistException", "the commits are not the tips of the pull request's branches"
		}
		if sc == dc {
			return nil, 400, "BeforeCommitIdAndAfterCommitIdAreSameException", "same"
		}
		c := &comment{id: fmt.Sprintf("cmt%08d", h.nextCmt), content: in.str("content"), author: who.arn, pr: p.id}
		h.nextCmt++
		h.cmts[c.id] = c
		return map[string]any{"pullRequestId": in.str("pullRequestId"), "repositoryName": Repo, "afterCommitId": sc, "beforeCommitId": dc, "comment": c.json()}, 0, "", ""
	case "UpdateComment":
		c := h.cmts[in.str("commentId")]
		switch {
		case c == nil:
			return nil, 400, "CommentDoesNotExistException", "no such comment"
		case c.author != who.arn:
			return nil, 400, "CommentNotCreatedByCallerException", "only the author may edit a comment"
		}
		c.content = in.str("content")
		return map[string]any{"comment": c.json()}, 0, "", ""
	}
	return h.templateAction(action, in)
}

func (c *comment) json() map[string]any {
	return map[string]any{"authorArn": c.author, "commentId": c.id, "content": c.content, "creationDate": 1508369622.123, "lastModifiedDate": 1508369622.123, "deleted": false,
		"callerReactions": []string{}, "reactionCounts": map[string]int{}}
}

func (h *Host) onPull(action string, in wire, p *pull) (any, int, string, string) {
	switch action {
	case "GetPullRequest":
		return map[string]any{"pullRequest": h.pullJSON(p)}, 0, "", ""
	case "UpdatePullRequestStatus":
		st := in.str("pullRequestStatus")
		if st == "OPEN" && p.status == "CLOSED" {
			return nil, 400, "InvalidPullRequestStatusUpdateException", "the only valid update is from OPEN to CLOSED"
		}
		p.status = st
	case "UpdatePullRequestTitle", "UpdatePullRequestDescription":
		if p.status == "CLOSED" {
			return nil, 400, "PullRequestAlreadyClosedException", "closed"
		}
		if action == "UpdatePullRequestTitle" {
			p.title = in.str("title")
		} else {
			p.desc = in.str("description")
		}
	case "GetPullRequestApprovalStates":
		if in.str("revisionId") != fmt.Sprintf("rev-%d-%d", p.id, p.revision) {
			return nil, 400, "InvalidRevisionIdException", "not the pull request's revision"
		}
		as := []map[string]string{}
		for _, a := range p.approvals {
			as = append(as, map[string]string{"userArn": a.arn, "approvalState": a.state})
		}
		return map[string]any{"approvals": as}, 0, "", ""
	}
	return map[string]any{"pullRequest": h.pullJSON(p)}, 0, "", ""
}

func (h *Host) createPull(in wire, who caller) (any, int, string, string) {
	var ts []map[string]string
	if json.Unmarshal(in["targets"], &ts) != nil || len(ts) != 1 {
		return nil, 400, "InvalidTargetsException", "exactly one target"
	}
	t := ts[0]
	for k := range t {
		if k != "repositoryName" && k != "sourceReference" && k != "destinationReference" {
			return nil, 400, "InvalidTargetException", "unknown field " + k
		}
	}
	if t["repositoryName"] != Repo || !strings.HasPrefix(t["sourceReference"], "refs/heads/") || !strings.HasPrefix(t["destinationReference"], "refs/heads/") {
		return nil, 400, "InvalidTargetException", "a target has the repository name and full reference names"
	}
	if t["sourceReference"] == t["destinationReference"] {
		return nil, 400, "SourceAndDestinationAreSameException", "same"
	}
	for _, r := range []string{t["sourceReference"], t["destinationReference"]} {
		if h.Branch(strings.TrimPrefix(r, "refs/heads/")) == "" {
			return nil, 400, "ReferenceDoesNotExistException", "no such reference"
		}
	}
	if in.str("clientRequestToken") == "" {
		return nil, 400, "ClientRequestTokenRequiredException", "a token is required"
	}
	if in.str("title") == "" {
		return nil, 400, "TitleRequiredException", "a title is required"
	}
	for _, p := range h.prs {
		if p.token == in.str("clientRequestToken") {
			return map[string]any{"pullRequest": h.pullJSON(p)}, 0, "", ""
		}
	}
	id := h.addPull(strings.TrimPrefix(t["sourceReference"], "refs/heads/"), strings.TrimPrefix(t["destinationReference"], "refs/heads/"), in.str("title"))
	p := h.prs[len(h.prs)-1]
	p.desc, p.author, p.token = in.str("description"), who.arn, in.str("clientRequestToken")
	_ = id
	return map[string]any{"pullRequest": h.pullJSON(p)}, 0, "", ""
}

func (h *Host) templateAction(action string, in wire) (any, int, string, string) {
	switch action {
	case "ListAssociatedApprovalRuleTemplatesForRepository":
		names := []string{}
		for _, t := range h.tmpl {
			names = append(names, t.name)
		}
		return map[string]any{"approvalRuleTemplateNames": names}, 0, "", ""
	case "GetApprovalRuleTemplate":
		for _, t := range h.tmpl {
			if t.name == in.str("approvalRuleTemplateName") {
				return map[string]any{"approvalRuleTemplate": map[string]any{"approvalRuleTemplateName": t.name, "approvalRuleTemplateContent": t.content,
					"approvalRuleTemplateId": "a29abb15-EXAMPLE", "creationDate": 1571356106.936, "lastModifiedDate": 1571356106.936}}, 0, "", ""
			}
		}
		return nil, 400, "ApprovalRuleTemplateDoesNotExistException", "no such template"
	}
	return nil, 404, "UnknownOperationException", "unreachable"
}
