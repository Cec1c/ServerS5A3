package dnfbridge

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Cosmetic profile traffic must not hold an actor's appearance lock forever
// when another member stops reading. Keep both lock waits and the socket write
// inside the caller's budget. Ordinary game writes retain their existing path.
func lockCurrentCrossSceneProfile(ctx context.Context, mutex *sync.Mutex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if mutex.TryLock() {
		return nil
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if mutex.TryLock() {
				return nil
			}
		}
	}
}

func (s *Service) sendCurrentCrossSceneProfileFrame(ctx context.Context, viewer, actor *gameSession, body []byte, current func() bool) (bool, error) {
	return s.sendCurrentCrossSceneProfileFrames(ctx, viewer, actor, [][]byte{body}, current)
}

// Return with appearance and wire held, and with HP sampled only after that
// wire was acquired. A completed return may have written new HP while this
// profile was being built or waiting. Try-only dungeon reads avoid the inverse
// order of gameplay handlers that send while holding dungeon.mu.
func (s *Service) lockCurrentCrossSceneProfileForDelivery(ctx context.Context, viewer, actor *gameSession, needsHP bool) ([]byte, error) {
	for {
		if err := lockCurrentCrossSceneProfile(ctx, &actor.crossSceneAppearanceMu); err != nil {
			return nil, err
		}
		if err := lockCurrentCrossSceneProfile(ctx, &viewer.wireMu); err != nil {
			actor.crossSceneAppearanceMu.Unlock()
			return nil, err
		}
		var realtime []byte
		ready := true
		if needsHP {
			realtime, ready = s.tryCurrentTownPartyRealtimeBodyOnWire(viewer, actor)
		}
		if ready {
			return realtime, nil
		}
		viewer.wireMu.Unlock()
		actor.crossSceneAppearanceMu.Unlock()
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// sendCurrentCrossSceneProfileFrames keeps a compound profile projection on
// one connection with one appearance snapshot. The caller may send a silent
// mode8 priming frame followed by the ordinary mode3 inspection frame without
// an appearance invalidation or unrelated upper frame interleaving between
// them. TCP delivery is still not transactional: an error after one complete
// frame can leave that frame delivered, so the caller must treat a partial
// pair as a failed attempt that can be retried by the next request.
func (s *Service) sendCurrentCrossSceneProfileFrames(ctx context.Context, viewer, actor *gameSession, bodies [][]byte, current func() bool) (bool, error) {
	if len(bodies) == 0 {
		return false, errors.New("cross-scene profile frame list is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, createWriteTimeout)
	defer cancel()
	if viewer == nil || viewer.conn == nil || actor == nil {
		return false, errors.New("cross-scene profile connection unavailable")
	}
	needsHP := false
	for _, body := range bodies {
		needsHP = needsHP || (len(body) > 0 && body[0] == currentCrossSceneMode8Subtype)
	}
	realtime, err := s.lockCurrentCrossSceneProfileForDelivery(ctx, viewer, actor, needsHP)
	if err != nil {
		return false, err
	}
	defer actor.crossSceneAppearanceMu.Unlock()
	defer viewer.wireMu.Unlock()
	deadline, _ := ctx.Deadline()
	if err := viewer.conn.SetWriteDeadline(deadline); err != nil {
		return false, err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = viewer.conn.SetWriteDeadline(time.Now())
		close(finished)
	})
	defer func() {
		if !stop() {
			<-finished
		}
		// Restore before releasing wireMu so later ordinary frames do not
		// inherit this profile's deadline or a racing cancellation callback.
		_ = viewer.conn.SetWriteDeadline(time.Time{})
	}()
	for _, body := range bodies {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// Waiting for the wire can yield to a leave, disconnect, generation
		// change or channel change. Recheck immediately before every frame;
		// the callback is memory-only and does not retain aggregate locks.
		if !current() {
			return false, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if len(body) > 0 && body[0] == currentCrossSceneMode8Subtype {
			body, err = projectCurrentTownPartyHPIntoProfile(body, realtime, selectedCharacterID(actor))
			if err != nil {
				return false, err
			}
		}
		err := s.sendGameUpperRawClassCodecLocked(viewer, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0, true)
		if err != nil {
			// A timeout may have written a prefix and advanced DPROTO. Reusing
			// that stream would corrupt subsequent frames; close only this peer.
			_ = viewer.conn.Close()
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return false, context.DeadlineExceeded
			}
			return false, err
		}
	}
	return true, nil
}
