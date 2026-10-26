package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestCrossSceneProfileCancellationDuringPrepareDoesNotWrite(t *testing.T) {
	for _, kind := range []string{"batch", "inspect"} {
		t.Run(kind, func(t *testing.T) {
			f := newCurrentCrossSceneProfileBatchFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cancelled := false
			wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, func(id string) {
				if id == "17" {
					// The repository has returned a valid row. A cancellation
					// arriving now must be checked again before the actual write.
					cancelled = true
					cancel()
				}
			})
			f.reset()
			var err error
			if kind == "batch" {
				err = newCurrentCrossSceneProfileBatch(f.service, ctx).send(f.target, f.actor, currentConnectionTownActorOwnerContext(f.target))
			} else {
				err = f.service.sendCurrentCrossScenePeerProfile(ctx, f.target, f.actor, 29, 17)
			}
			if !cancelled || !errors.Is(err, context.Canceled) || f.targetConn.write.Len() != 0 {
				t.Fatalf("cancelled=%v error=%v viewerBytes=%d", cancelled, err, f.targetConn.write.Len())
			}
		})
	}
}

func TestCrossSceneProfileExpiredBatchDoesNotWritePreparedBody(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	batch := newCurrentCrossSceneProfileBatch(f.service, ctx)
	owner := currentConnectionTownActorOwnerContext(f.target)
	if err := batch.send(f.target, f.actor, owner); err != nil {
		t.Fatal(err)
	}
	f.reset()
	cancel()
	if err := batch.send(f.viewer2, f.actor, owner); !errors.Is(err, context.Canceled) || f.viewer2Conn.write.Len() != 0 {
		t.Fatalf("cancelled prepared body err=%v bytes=%d", err, f.viewer2Conn.write.Len())
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	if err := f.service.sendCurrentCrossScenePeerProfile(deadline, f.target, f.actor, 29, 17); !errors.Is(err, context.DeadlineExceeded) || f.targetConn.write.Len() != 0 {
		t.Fatalf("expired inspect err=%v bytes=%d", err, f.targetConn.write.Len())
	}
}

func TestCrossSceneProfileFailureKindsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("private: %w", context.Canceled), "cancelled"},
		{context.DeadlineExceeded, "deadline"},
		{io.ErrClosedPipe, "connection_closed"},
		{errors.New("private database detail"), "prepare_or_write"},
	} {
		if got := currentCrossSceneProfileErrorKind(tc.err); got != tc.want {
			t.Fatalf("error kind=%q want=%q", got, tc.want)
		}
	}
}

func TestCrossScenePeerWriteFailureDoesNotLogSuccess(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	bad := &currentCrossSceneProfileBatchFailConn{}
	f.target.conn = bad
	var log bytes.Buffer
	f.service.logger = slog.New(slog.NewJSONHandler(&log, nil))
	err := f.service.sendCurrentCrossScenePeerProfile(context.Background(), f.target, f.actor, 29, 17)
	if !errors.Is(err, io.ErrClosedPipe) || bad.writes != 1 {
		t.Fatalf("inspect write err=%v attempts=%d", err, bad.writes)
	}
	if strings.Contains(log.String(), "game-peer-userinfo-profile-refresh-sent") {
		t.Fatal("failed inspection logged a successful profile write")
	}
}
