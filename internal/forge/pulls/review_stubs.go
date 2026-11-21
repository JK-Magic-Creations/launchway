package pulls

import "context"

// Filled in by review.go and merge.go in later commits of this branch.
func (s *Service) reanchorComments(context.Context, string, PullRequest, string) {}
func (s *Service) dismissStale(context.Context, string, PullRequest, string)     {}
func (s *Service) autoMerge(context.Context, string, string)                     {}
