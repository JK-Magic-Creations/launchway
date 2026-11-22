package api

// What turns events of the product into notices for people: pull request
// events, run, approval and deployment events from the notification service,
// the work inbox and published releases.

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/notices"
	"github.com/jk-magic-creations/launchway/internal/notify"
	"github.com/jk-magic-creations/launchway/internal/work"
)

func splitNames(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// noticePull tells people of a pull request event.
func (s *Server) noticePull(ctx context.Context, repo forge.Repository, e pulls.Event) {
	if s.Notices == nil {
		return
	}
	p := e.Pull
	base := notices.Event{Actor: e.Actor, Name: repo.Path, Public: repo.Visibility == forge.Public, Scopes: []string{"repo:" + repo.Path},
		Title: fmt.Sprintf("#%d %s", p.Number, p.Title), Link: fmt.Sprintf("/repositories/%s/pulls/%d", repo.Path, p.Number)}
	participants := append(append([]string{p.Author}, p.Assignees...), p.Reviewers...)
	send := func(kind string, people []string, dedupe string) {
		ev := base
		ev.Kind, ev.People, ev.Dedupe = kind, people, dedupe
		if _, err := s.Notices.Notify(ctx, ev); err != nil {
			s.Log.Warn("a notice could not be made", "kind", kind, "err", err)
		}
	}
	key := fmt.Sprintf("pull:%s:%d:%s", repo.ID, p.Number, e.Action)
	switch e.Action {
	case "review_requested":
		send(notices.ReviewRequested, splitNames(e.Detail["reviewers"]), key+":"+e.Detail["reviewers"]+":"+p.HeadSHA)
	case "review_submitted":
		send(notices.ReviewSubmitted, append([]string{p.Author}, p.Assignees...), key+":"+e.Detail["review"])
		if m := splitNames(e.Detail["mentions"]); len(m) > 0 {
			send(notices.Mention, m, key+":mention:"+e.Detail["review"])
		}
	case "commented":
		send(notices.Comment, participants, key+":"+e.Detail["comment"])
		if m := splitNames(e.Detail["mentions"]); len(m) > 0 {
			send(notices.Mention, m, key+":mention:"+e.Detail["comment"])
		}
	case "merged":
		send(notices.PullMerged, participants, key)
	case "closed":
		send(notices.PullClosed, participants, key+":"+p.UpdatedAt.Format("20060102150405"))
	}
}

// personEvent tells people of what the notification service noticed.
func (s *Server) personEvent(ctx context.Context, ev notify.Event) {
	if s.Notices == nil {
		return
	}
	out := notices.Event{Name: ev.Project, Scopes: []string{"flight:" + ev.Project}, Title: ev.Title, Dedupe: ev.Key, Link: "/runs/" + ev.RunID}
	actor, approvers := "", []string(nil)
	if ev.RunID != "" {
		actor, approvers = s.Engine.RunPeople(ctx, ev.RunID)
	}
	switch ev.Kind {
	case notify.RunFailed:
		out.Kind, out.People = notices.RunFailed, []string{actor}
	case "run.fixed":
		out.Kind, out.People = notices.RunFixed, []string{actor}
	case notify.ApprovalRequested:
		out.Kind, out.People = notices.ApprovalWaiting, s.Engine.StepApprovers(ev.RunID, ev.Step)
		out.Link = "/runs/" + ev.RunID + "?step=" + url.QueryEscape(ev.Step)
	case notify.DeploymentSucceed, notify.DeploymentFailed:
		out.Kind, out.People = notices.DeployFinished, approvers
	case notify.DeploymentRolledBk:
		out.Kind, out.People = notices.DeployRolledBack, approvers
	default:
		return
	}
	out.People = slices.DeleteFunc(slices.Clone(out.People), func(p string) bool { return p == "" })
	if len(out.People) == 0 {
		return
	}
	if _, err := s.Notices.Notify(ctx, out); err != nil {
		s.Log.Warn("a notice could not be made", "kind", out.Kind, "err", err)
	}
}

// workInboxed tells people of a mention or a comment on a work item, as the
// work store files it. It runs while the work store holds its transaction, so
// it works on its own.
func (s *Server) workInboxed(ctx context.Context, who, by, kind, itemID, text string) {
	var k string
	switch kind {
	case work.InboxMentioned:
		k = notices.Mention
	case work.InboxComment:
		k = notices.Comment
	default:
		return
	}
	if s.Notices == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		_, _ = s.Notices.Notify(ctx, notices.Event{Kind: k, People: []string{who}, Actor: by, Name: "work", Title: text, Link: "/work", Scopes: nil,
			Dedupe: "work:" + itemID + ":" + k + ":" + text})
	}()
}

// noticeRelease tells the people who watch a Flight that a release was published.
func (s *Server) noticeRelease(ctx context.Context, project, version, by string) {
	if s.Notices == nil {
		return
	}
	_, _ = s.Notices.Notify(ctx, notices.Event{Kind: notices.ReleasePublished, Actor: by, Name: project, Scopes: []string{"flight:" + project},
		Title: version, Link: "/releases", Dedupe: "release:" + project + ":" + version})
}
