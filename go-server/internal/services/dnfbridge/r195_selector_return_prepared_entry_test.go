package dnfbridge

import (
	"encoding/binary"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// preparedEntrySelectorReturnMarker is the same value shape produced by
// captureCurrentSelectorReturnPresence: bound identity, channel, raid scope and
// typed town transition. The full capture path is covered by
// TestRealPVFLukeSelectorReturnWithoutAreaCrossingRestoresWaitingMembers; using
// that real-PVF fixture here would add a large dependency without exercising a
// different commit/rollback boundary.
func preparedEntrySelectorReturnMarker(session *gameSession) *currentSelectorReturnPresence {
	return &currentSelectorReturnPresence{
		identity: boundGameSessionCharacter{
			session:    session,
			character:  session.selectedCharacterID,
			generation: session.characterGeneration,
		},
		channelID: session.channel.ID,
		raidKey:   0x195,
		battleID:  "prepared-entry-test-battle",
		transition: currentDungeonTownTransition{
			TownID:    32,
			AreaID:    4,
			PositionX: 580,
			PositionY: 243,
			Direction: 5,
			AreaState: 3,
		},
	}
}

func seedPreparedEntrySelectorReturn(session *gameSession) *currentSelectorReturnPresence {
	marker := preparedEntrySelectorReturnMarker(session)
	session.dungeon.mu.Lock()
	session.selectorReturnPresence = marker
	session.confirmedDungeonReturnStatePending = true
	session.returnTownFinishLoadingAckOnly = true
	session.dungeon.mu.Unlock()
	return marker
}

func assertPreparedEntrySelectorReturnRetained(t *testing.T, session *gameSession, marker *currentSelectorReturnPresence) {
	t.Helper()
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.selectorReturnPresence != marker {
		t.Fatalf("selector return owner changed: got=%p want=%p", session.selectorReturnPresence, marker)
	}
	if !session.confirmedDungeonReturnStatePending || !session.returnTownFinishLoadingAckOnly {
		t.Fatalf("pending selector return flags were dropped: pending=%t ack_only=%t",
			session.confirmedDungeonReturnStatePending, session.returnTownFinishLoadingAckOnly)
	}
}

// preparedEntryBlockingConn blocks the leader's post-commit op16 ACK. The new
// clear loop runs before that ACK; the follower's own
// handleDungeonSelectUpperPlanned runs later at the op28 barrier. Blocking here
// keeps the two effects separate.
type preparedEntryBlockingConn struct {
	*bufferConn
	firstWrite chan struct{}
	release    chan struct{}
	once       sync.Once
	releaseOne sync.Once
}

func (connection *preparedEntryBlockingConn) Write(data []byte) (int, error) {
	if len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == uint16(dnfenum.CmdPacketSelectDungeon) {
		connection.once.Do(func() {
			close(connection.firstWrite)
			<-connection.release
		})
	}
	return connection.bufferConn.Write(data)
}

func (connection *preparedEntryBlockingConn) Release() {
	connection.releaseOne.Do(func() { close(connection.release) })
}

// TestPreparedEntryWholeGroupCommitClearsFollowerSelectorReturn drives the real
// two-member op16 path used by partyDungeonSyncFixture. At the leader's
// post-commit op16 ACK, the follower runtime is already installed and the
// production post-commit loop has already run, while the follower's own entry
// handler has not. The old selector marker must already be gone at that point.
func TestPreparedEntryWholeGroupCommitClearsFollowerSelectorReturn(t *testing.T) {
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	t.Cleanup(func() { partyDungeonLoadingBarrierDispatch = restoreDispatch })

	service, leader, follower, _, _, _ := partyDungeonSyncFixture(t)
	seedPreparedEntrySelectorReturn(follower)

	blocked := &preparedEntryBlockingConn{
		bufferConn: &bufferConn{},
		firstWrite: make(chan struct{}),
		release:    make(chan struct{}),
	}
	leader.conn = blocked
	defer blocked.Release()

	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[:4], 700)
	done := make(chan error, 1)
	go func() {
		done <- service.handleDungeonSelectUpper(leader, body)
	}()

	select {
	case <-blocked.firstWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("leader never reached the post-commit op16 ACK")
	}

	if !sessionHasActiveDungeonRuntime(follower) {
		t.Fatal("leader ACKed before the follower runtime was committed")
	}
	follower.dungeon.mu.Lock()
	presence := follower.selectorReturnPresence
	pending := follower.confirmedDungeonReturnStatePending
	ackOnly := follower.returnTownFinishLoadingAckOnly
	follower.dungeon.mu.Unlock()
	if presence != nil || pending || ackOnly {
		t.Fatalf("post-commit loop did not clear the follower marker before its own entry handler: "+
			"presence=%p pending=%t ack_only=%t", presence, pending, ackOnly)
	}

	blocked.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("whole-group party entry: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("party entry did not finish after the leader op16 ACK was released")
	}
	if !sessionHasActiveDungeonRuntime(follower) {
		t.Fatal("follower runtime disappeared after the full entry completed")
	}
}

// TestPreparedEntryProvisionalFollowerCommitKeepsSelectorReturn pins the other
// half of the ordering rule: committing a follower runtime must not clear the
// marker. Only the later whole-group success loop owns that cleanup.
func TestPreparedEntryProvisionalFollowerCommitKeepsSelectorReturn(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	session.dungeon.runtime = nil
	session.petGrowth = petGrowthClockState{}
	marker := seedPreparedEntrySelectorReturn(session)

	member := &runtimePartyPreparedDungeonEntry{session: session, runtime: runtime}
	if err := service.commitRuntimePartyPreparedFollowers([]*runtimePartyPreparedDungeonEntry{member}); err != nil {
		t.Fatalf("provisional follower commit: %v", err)
	}
	if !member.committed || session.dungeon.runtime != runtime {
		t.Fatalf("provisional follower commit did not install the runtime: committed=%t runtime=%p want=%p",
			member.committed, session.dungeon.runtime, runtime)
	}
	assertPreparedEntrySelectorReturnRetained(t, session, marker)
}

// TestPreparedEntryRollbackKeepsSelectorReturnPending forces the production
// commit loop to take its rollback branch after the first follower's runtime
// was really installed. The incomplete second record is the deterministic
// stand-in for a later leader-side rejection.
func TestPreparedEntryRollbackKeepsSelectorReturnPending(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	session.dungeon.runtime = nil
	session.petGrowth = petGrowthClockState{}
	marker := seedPreparedEntrySelectorReturn(session)

	member := &runtimePartyPreparedDungeonEntry{session: session, runtime: runtime}
	err := service.commitRuntimePartyPreparedFollowers([]*runtimePartyPreparedDungeonEntry{member, nil})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete prepared follower did not take the rollback branch: %v", err)
	}
	if session.dungeon.runtime != nil || member.committed {
		t.Fatalf("rollback did not retire the provisionally committed follower: runtime=%p committed=%t",
			session.dungeon.runtime, member.committed)
	}
	assertPreparedEntrySelectorReturnRetained(t, session, marker)
}
