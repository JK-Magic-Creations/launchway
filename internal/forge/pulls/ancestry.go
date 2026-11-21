package pulls

import "context"

// Contains reports whether the commit desc has the commit anc in its history
// (or is it), for the delivery status of work: a deployment of desc holds a
// change that was merged as anc.
func (s *Service) Contains(ctx context.Context, repoID, anc, desc string) (bool, error) {
	var ok bool
	err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		ok, err = g.isAncestor(ctx, anc, desc)
		return err
	})
	return ok, err
}

// Refuse is an error that stops a merge with a reason a person can act on.
func Refuse(format string, a ...any) error { return conflict(format, a...) }
