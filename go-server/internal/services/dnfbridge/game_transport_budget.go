package dnfbridge

import (
	"context"
	"errors"
	"io"
	"time"
)

// Transport cancellation is separate from the event/transaction lifetime.
// Retiring an account must still wait for its owner to finish committing.
func (p *gameSession) gameTransportContext() context.Context {
	p.transportOnce.Do(func() {
		parent := p.ctx
		if parent == nil {
			parent = context.Background()
		}
		p.transportCtx, p.transportCancel = context.WithCancel(parent)
	})
	return p.transportCtx
}

func (p *gameSession) stopGameTransport() {
	if p == nil {
		return
	}
	p.gameTransportContext()
	p.transportCancel()
}

func (p *gameSession) closeGameTransport() {
	if p == nil {
		return
	}
	p.stopGameTransport()
	p.transportCloseOnce.Do(func() {
		if p.conn != nil {
			_ = p.conn.Close()
		}
	})
}

// Combine explicit caller cancellation, the connection lifetime and the
// default upper bound. No context here is used to cancel an asset transaction.
func gameTransportBudget(p *gameSession, parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, createWriteTimeout)
	lifetime := p.gameTransportContext()
	stop := context.AfterFunc(lifetime, cancel)
	if lifetime.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (s *Service) lockGameWire(p *gameSession, parent context.Context) error {
	ctx, cancel := gameTransportBudget(p, parent)
	defer cancel()
	started := s.performanceStart()
	err := lockCurrentCrossSceneProfile(ctx, &p.wireMu)
	s.performanceEnd(perfWireLock, started, err)
	return err
}

// Caller holds wireMu. The scope is logical: buffering is not a socket write.
// Nested scopes inherit the earlier deadline and cancellation; on exit the
// parent is restored. Only the actual write owns a socket deadline/callback.
func beginGameWriteBudgetLocked(p *gameSession, parent context.Context) (func(), error) {
	ctx, cancel := gameTransportBudget(p, parent)
	previous := p.wireWriteContext
	var stop func() bool
	if previous != nil {
		if deadline, ok := previous.Deadline(); ok {
			var childCancel context.CancelFunc
			ctx, childCancel = context.WithDeadline(ctx, deadline)
			oldCancel := cancel
			cancel = func() { childCancel(); oldCancel() }
		}
		stop = context.AfterFunc(previous, cancel)
		if previous.Err() != nil {
			cancel()
		}
	}
	finish := func() {
		if stop != nil {
			stop()
		}
		cancel()
		p.wireWriteContext = previous
	}
	if err := ctx.Err(); err != nil {
		finish()
		return nil, err
	}
	p.wireWriteContext = ctx
	return finish, nil
}

// All game TCP writes, including provider controls and batch flushes, pass
// here under wireMu. A short write cannot reuse an advanced codec sequence.
func (s *Service) writeGameSessionObserved(p *gameSession, data []byte, stage int) (int, error) {
	if p.wireWriteContext == nil {
		finish, err := beginGameWriteBudgetLocked(p, nil)
		if err != nil {
			p.closeGameTransport()
			return 0, err
		}
		defer finish()
	}
	ctx := p.wireWriteContext
	if err := ctx.Err(); err != nil {
		p.closeGameTransport() // The caller may already have encoded this frame.
		return 0, err
	}
	deadline, _ := ctx.Deadline()
	if err := p.conn.SetWriteDeadline(deadline); err != nil {
		p.closeGameTransport()
		return 0, err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = p.conn.SetWriteDeadline(time.Now())
		close(finished)
	})
	defer func() {
		if !stop() {
			<-finished
		}
		if err := p.conn.SetWriteDeadline(time.Time{}); err != nil {
			p.closeGameTransport()
		}
	}()
	n, err := s.writeGameConnectionObserved(p.conn, data, stage)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		// Keep the established zero-byte, non-timeout retry contract. A partial
		// frame or expired write can no longer safely reuse this stream.
		// Do not call arbitrary error Unwrap/Timeout methods here: preserve
		// even typed-nil provider errors for the caller's diagnostic owner.
		if p.dproto != nil || n > 0 || err == io.ErrShortWrite || ctx.Err() != nil || !time.Now().Before(deadline) {
			p.closeGameTransport()
		}
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
	}
	return n, err
}
