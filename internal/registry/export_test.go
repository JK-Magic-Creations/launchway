package registry

import "context"

// PutStrayForTest writes an object under a key the registry owns and records it nowhere.
func (s *Service) PutStrayForTest(ctx context.Context, key string, data []byte) error {
	return s.putBytes(ctx, key, data)
}
