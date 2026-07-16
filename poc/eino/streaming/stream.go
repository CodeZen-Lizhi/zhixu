// Package streaming defines framework-neutral streaming helpers for the Eino PoC.
package streaming

import (
	"context"
	"errors"
	"io"
)

var ErrCanceled = context.Canceled

// Reader is the minimal model stream contract. Close must release provider resources.
type Reader interface {
	Recv() (string, error)
	Close() error
}

// Consume reads chunks until EOF or cancellation, and always closes the reader.
// A cancellation stops consumption promptly and returns context.Canceled.
func Consume(ctx context.Context, r Reader, emit func(string) error) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return errors.New("stream reader is nil")
	}
	defer func() {
		err = errors.Join(err, r.Close())
	}()
	for {
		result := make(chan struct {
			chunk string
			err   error
		}, 1)
		go func() {
			chunk, err := r.Recv()
			result <- struct {
				chunk string
				err   error
			}{chunk, err}
		}()
		select {
		case <-ctx.Done():
			// Closing the provider stream is the cancellation boundary. A compliant
			// reader unblocks Recv after Close, preventing a goroutine leak.
			return ctx.Err()
		case res := <-result:
			chunk, err := res.chunk, res.err
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				if errors.Is(err, context.Canceled) && ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			if chunk == "" {
				continue
			}
			if emit != nil {
				if err := emit(chunk); err != nil {
					return err
				}
			}
		}
	}
}
