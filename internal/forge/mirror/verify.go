package mirror

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// ---------------------------------------------------------------- verifying

// Verification states of one repository.
const (
	VerifyEqual     = "equal"
	VerifyDifferent = "different"
	VerifyMissing   = "missing"
	VerifyError     = "error"
)

// VerifyItem is one repository's verdict.
type VerifyItem struct {
	Source      string   `json:"source"`
	Repository  string   `json:"repository"`
	Status      string   `json:"status"`
	SourceRefs  int      `json:"source_refs"`
	HostedRefs  int      `json:"hosted_refs"`
	Differences []string `json:"differences,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// VerifyResult proves, or fails to prove, that every ref of every repository
// is the same here as at the source.
type VerifyResult struct {
	Equal   bool           `json:"equal"`
	Items   []VerifyItem   `json:"items"`
	Summary map[string]int `json:"summary"`
}

// Verify compares every ref of every repository of a source with the hosted
// repository of the same name: each must exist here and have exactly the
// branches, tags and notes at exactly the commits the source has. It reads
// only; it is what to run before a cut-over.
func (s *Service) Verify(ctx context.Context, req SourceRequest) (VerifyResult, error) {
	ps, err := s.plan(ctx, req)
	if err != nil {
		return VerifyResult{}, err
	}
	items := make([]VerifyItem, len(ps))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, p := range ps {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			items[i] = s.verifyOne(ctx, p)
		}()
	}
	wg.Wait()
	res := VerifyResult{Items: items, Equal: true, Summary: summarize(items, func(i VerifyItem) string { return i.Status })}
	for _, it := range items {
		res.Equal = res.Equal && it.Status == VerifyEqual
	}
	if len(items) == 0 {
		res.Equal = false
	}
	return res, nil
}

func (s *Service) verifyOne(ctx context.Context, p planned) VerifyItem {
	it := VerifyItem{Source: p.src.Path(), Repository: p.target}
	if p.err != "" {
		it.Status, it.Error = VerifyError, p.err
		return it
	}
	repo, err := s.repoByPath(ctx, p.target)
	if errors.Is(err, forge.ErrNotFound) {
		it.Status = VerifyMissing
		return it
	}
	if err != nil {
		it.Status, it.Error = VerifyError, err.Error()
		return it
	}
	c, err := s.compare(ctx, repo, p.src.URL, p.cred, p.user)
	if err != nil {
		it.Status, it.Error = VerifyError, redact.URLs(err.Error())
		return it
	}
	it.SourceRefs, it.HostedRefs, it.Differences = c.SourceRefs, c.HostedRefs, c.Differences
	if c.more > 0 {
		it.Differences = append(it.Differences, fmt.Sprintf("and %d more", c.more))
	}
	it.Status = VerifyEqual
	if !c.Equal() {
		it.Status = VerifyDifferent
	}
	return it
}
