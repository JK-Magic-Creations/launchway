package githttp

import (
	"context"
	"errors"
	"io"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// RefUpdate is one ref a push moves; Policy functions are given them.
type RefUpdate = storage.RefUpdate

// UploadArchive is the third service, served over SSH only.
const UploadArchive = "git-upload-archive"

// Archiver is a backend that can also serve git-upload-archive.
type Archiver interface {
	Archive(ctx context.Context, repo string, rpc storage.RPC) error
}

// StreamRequest is one SSH command, after the caller has authorised it.
type StreamRequest struct {
	Request  // Repo, Service and User; Advertise is unused
	Protocol string
	In       io.Reader
	Out      io.Writer
	// Policy is storage.RPC.Policy for a push.
	Policy func(ctx context.Context, updates []RefUpdate) error
}

// ErrBusy is a request refused because this server (or this person) is
// serving as many as it will.
type ErrBusy struct{ Message string }

func (e *ErrBusy) Error() string { return e.Message }

// Stream serves one command of Git over SSH with the same caps, the same
// storage calls and the same hooks as the HTTP transport: a push is received
// by the same code, so the same rules and the same commit point apply. The
// transfer ends when the client's input ends, the context ends, or git makes
// no progress for the idle timeout. The caps are shared with Serve.
func (h *Handler) Stream(ctx context.Context, q StreamRequest) Result {
	release, status, msg := h.acquire(ctx, q.User)
	if release == nil {
		return Result{Status: status, Err: &ErrBusy{msg}}
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, h.o.MaxDuration)
	defer cancel()
	limit := h.o.MaxFetchBytes
	if q.Service == ReceivePack {
		limit = h.o.MaxPushBytes
	}
	in := &capReader{r: q.In, left: limit} // the same ceiling as an HTTP push; a pack is streamed, never held in memory
	track := &trackReader{r: in}
	rpc := storage.RPC{Stream: true, In: track, Out: q.Out, GitProtocol: protocolHeader(q.Protocol), Policy: q.Policy, Notice: q.Notice}
	var res Result
	var err error
	switch q.Service {
	case ReceivePack:
		var rr storage.ReceiveResult
		rr, err = h.o.Backend.Receive(ctx, q.Repo.ID, rpc)
		res.Receive = &rr
		var stale *storage.ErrStale
		if errors.As(err, &stale) {
			err = nil
		}
	case UploadPack:
		err = h.o.Backend.Upload(ctx, q.Repo.ID, rpc)
	case UploadArchive:
		a, ok := h.o.Backend.(Archiver)
		if !ok {
			err = errors.New("archives are not available from this storage")
			break
		}
		rpc.In = &trackReader{r: q.In}
		err = a.Archive(ctx, q.Repo.ID, rpc)
	default:
		err = errors.New("no such Git service")
	}
	res.Status = 200
	if err != nil {
		if errors.Is(track.err, ErrTooLarge) {
			err = ErrTooLarge
		}
		res.Err = err
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrTooLarge) {
			h.o.Log.Error("serving git over ssh", "err", err)
		}
	}
	return res
}

func protocolHeader(p string) string { return p }
