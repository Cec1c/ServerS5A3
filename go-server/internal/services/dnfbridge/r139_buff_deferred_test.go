package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
)

type r139CountTransactions struct {
	repo.CharacterItemUnitOfWork
	attempts, commits *int
}

func (u r139CountTransactions) WithinCharacterItems(ctx context.Context, id string, fn func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	*u.attempts++
	err := u.CharacterItemUnitOfWork.WithinCharacterItems(ctx, id, fn)
	if err == nil {
		*u.commits++
	}
	return err
}

type r139ContainerFailureConn struct {
	bufferConn
	failed, sticky bool
	buffAttempts   int
}

func (c *r139ContainerFailureConn) Write(p []byte) (int, error) {
	if len(p) >= 3 && p[0] == 0 {
		msg := binary.LittleEndian.Uint16(p[1:3])
		if msg == 1368 || msg == 1367 {
			c.buffAttempts++
		}
		if msg == 13 {
			c.failed = true
			return 0, errors.New("r139 container write failure")
		}
	}
	if c.failed && c.sticky {
		return 0, errors.New("r139 connection still failed")
	}
	return c.bufferConn.Write(p)
}

// This uses an existing supported full-container rollback option, a real
// nonappearance wear move, and an actual socket-write error. No fabricated
// Result or direct sentinel injection is used to reach the deferred branch.
func TestR139BuffContainerDeferredMustNotEscapeBoundary(t *testing.T) {
	for _, sticky := range []bool{false, true} {
		name := "transient_writer"
		if sticky {
			name = "persistent_writer"
		}
		t.Run(name, func(t *testing.T) {
			f := p94Fixture(t)
			instance := p94Register(t, f)
			attempts, commits := 0, 0
			g := f.group
			g.CharacterItems = r139CountTransactions{g.CharacterItems, &attempts, &commits}
			f.service.repositoryProvider = func() (repo.Group, bool) { return g, true }
			var logs bytes.Buffer
			f.service.logger = slog.New(slog.NewTextHandler(&logs, nil))
			f.service.options.equipMoveContainerRefreshFull = true
			conn := &r139ContainerFailureConn{sticky: sticky}
			f.session.conn = conn
			err := f.service.handleGameCommand(f.session, 1, 19, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag))
			r139AssertOwner(t, f, 16, instance, 0, 15)
			if !conn.failed || attempts != 1 || commits != 1 || !strings.Contains(logs.String(), "deferred remaining aligned post-commit projections") {
				t.Fatalf("not the real deferred branch: failed=%v attempts=%d commits=%d logs=%s", conn.failed, attempts, commits, logs.String())
			}
			t.Logf("real op19 committed once; failed op13; deferred warning=%t; subsequent1368Attempts=%d; returnedError=%v", strings.Contains(logs.String(), "deferred remaining aligned post-commit projections"), conn.buffAttempts, err)
			if err != nil || conn.buffAttempts != 0 {
				t.Fatalf("deferred boundary escaped: 1368 attempts=%d returned error=%v; want no attempt and existing nil result", conn.buffAttempts, err)
			}
		})
	}
}

func TestR139BuffSnapshotWriteFailureCommitsOnlyOnce(t *testing.T) {
	f := p94Fixture(t)
	instance := p94Register(t, f)
	attempts, commits := 0, 0
	g := f.group
	g.CharacterItems = r139CountTransactions{g.CharacterItems, &attempts, &commits}
	f.service.repositoryProvider = func() (repo.Group, bool) { return g, true }
	conn := &r139FailMessageConn{kind: 0, msg: 1367}
	f.session.conn = conn
	err := f.service.handleGameCommand(f.session, 1, 19, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag))
	if err == nil || !strings.Contains(err.Error(), "r139 injected message write failure") || !conn.hit || attempts != 1 || commits != 1 {
		t.Fatalf("1368 failure semantics: hit=%v attempts=%d commits=%d err=%v", conn.hit, attempts, commits, err)
	}
	r139AssertOwner(t, f, 16, instance, 0, 15)
}
