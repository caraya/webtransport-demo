package endpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/quic-go/webtransport-go"
	"golang.org/x/sync/errgroup"
)

// Echo copies bidirectional streams in place, returns unidirectional streams on
// new outgoing streams, and echoes datagrams. It handles up to 32 streams of each
// kind concurrently, with bounded copy buffers and no whole-stream buffering.
// Any transport error other than normal session closure ends the demo session.
func Echo(ctx context.Context, session *webtransport.Session) error {
	group, workCtx := errgroup.WithContext(ctx)
	stopped := make(chan struct{})
	callerCanceled := false
	// Canceling an accept context alone doesn't unblock active stream Read/Write.
	stop := context.AfterFunc(workCtx, func() {
		defer close(stopped)
		callerCanceled = ctx.Err() != nil && session.Context().Err() == nil
		if err := session.CloseWithError(1, "echo stopped"); err != nil {
			slog.Default().Warn("echo cancellation close failed", "error", err)
		}
	})

	group.Go(func() error {
		return echoStreams(workCtx, group, session.AcceptStream, func(stream *webtransport.Stream) error {
			if _, err := io.Copy(stream, stream); err != nil {
				return fmt.Errorf("echo bidirectional stream: %w", err)
			}
			if err := stream.Close(); err != nil {
				return fmt.Errorf("finish bidirectional echo: %w", err)
			}
			return nil
		})
	})
	group.Go(func() error {
		return echoStreams(workCtx, group, session.AcceptUniStream, func(stream *webtransport.ReceiveStream) error {
			out, err := session.OpenUniStreamSync(workCtx)
			if err != nil {
				return fmt.Errorf("open unidirectional echo: %w", err)
			}
			if _, err := io.Copy(out, stream); err != nil {
				return fmt.Errorf("echo unidirectional stream: %w", err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("finish unidirectional echo: %w", err)
			}
			return nil
		})
	})
	group.Go(func() error {
		for {
			data, err := session.ReceiveDatagram(workCtx)
			if err != nil {
				return fmt.Errorf("receive datagram: %w", err)
			}
			if err := session.SendDatagram(data); err != nil {
				return fmt.Errorf("echo datagram: %w", err)
			}
		}
	})

	err := group.Wait()
	if !stop() {
		<-stopped
	}
	var sessionErr *webtransport.SessionError
	if callerCanceled || errors.Is(err, context.Canceled) ||
		(errors.As(err, &sessionErr) && sessionErr.Remote && sessionErr.ErrorCode == 0) {
		return nil
	}
	return err
}

func echoStreams[T any](ctx context.Context, group *errgroup.Group, accept func(context.Context) (T, error), echo func(T) error) error {
	slots := make(chan struct{}, 32)
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		stream, err := accept(ctx)
		if err != nil {
			<-slots
			return fmt.Errorf("accept echo stream: %w", err)
		}
		group.Go(func() error {
			defer func() { <-slots }()
			return echo(stream)
		})
	}
}
