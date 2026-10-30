package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Pause the existing compound publication callback after its actor roster was
// built. A real op13 leave completes before the delayed op9 can reach the wire.
func TestPartyProjectionProbeRejectsRosterAfterCompletedLeave(t *testing.T) {
	h := newPartyLeaveHarness(t)
	h.service.options.currentClientVersion = "official-24.9"
	state := runtimePartyStateSnapshot(h.member)
	called := false
	sent, err := h.service.sendCurrentPartyActorFrameProjectionBefore(h.leader, h.member, state, "stale_leave_probe", func() error {
		called = true
		h.dispatchMemberLeave(t)
		if _, found := h.service.runtimePartyManagerForService().SnapshotByUser(1002, 0); found {
			t.Fatal("leave did not remove member")
		}
		h.leaderConn.write.Reset()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("publication callback not reached")
	}
	if sent || h.leaderConn.write.Len() != 0 {
		t.Fatalf("retired roster sent after completed op13 leave: sent=%t bytes=%d", sent, h.leaderConn.write.Len())
	}
}

func TestPartyProjectionProbeRejectsAlreadyRetiredRoster(t *testing.T) {
	h := newPartyLeaveHarness(t)
	state := runtimePartyStateSnapshot(h.member)
	h.dispatchMemberLeave(t)
	h.leaderConn.write.Reset()
	sent, err := h.service.sendCurrentPartyActorFrameProjection(h.leader, h.member, state, "already_retired")
	if err != nil || sent || h.leaderConn.write.Len() != 0 {
		t.Fatalf("old roster restored: sent=%t bytes=%d err=%v", sent, h.leaderConn.write.Len(), err)
	}
}

func TestPartyProjectionProbeRejectsHostAndSlotChanges(t *testing.T) {
	for _, change := range []string{"host", "slot"} {
		t.Run(change, func(t *testing.T) {
			h := newPartyLeaveHarness(t)
			state := runtimePartyStateSnapshot(h.member)
			sent, err := h.service.sendCurrentPartyActorFrameProjectionBefore(h.leader, h.member, state, change, func() error {
				manager := h.service.runtimePartyManagerForService()
				if change == "host" {
					result := manager.TransferLeader(1001, h.leader.characterGeneration, 1002, h.member.characterGeneration)
					if !result.OK {
						t.Fatal(result.Reason)
					}
				} else {
					result := manager.Reposition(1001, h.leader.characterGeneration, 1, 2)
					if !result.OK {
						t.Fatal(result.Reason)
					}
				}
				return nil
			})
			if err != nil || sent || h.leaderConn.write.Len() != 0 {
				t.Fatalf("obsolete %s published: sent=%t bytes=%d err=%v", change, sent, h.leaderConn.write.Len(), err)
			}
		})
	}
}

func TestPartyProjectionProbeDungeonEquipmentInitialFailurePublishesNothing(t *testing.T) {
	f := newTownResendFixture(t)
	f.service.bindGameSessionCharacter(f.local, f.local.selectedCharacterID)
	f.service.options.currentClientVersion = "official-24.9"
	repos, _ := f.service.repositoryGroup()
	fault := &remoteActorSnapshotFaultEquipment{EquipmentRepository: repos.Equipment, failFirst: true, err: errors.New("snapshot unavailable")}
	repos.Equipment = fault
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	err := f.service.sendDungeonPartyPeerActorState(f.local, f.peer, "initial_failure")
	if !errors.Is(err, fault.err) || f.localBuf.write.Len() != 0 {
		t.Fatalf("partial actor on storage failure: err=%v bytes=%d", err, f.localBuf.write.Len())
	}
}

func TestPartyProjectionProbeDungeonSelectionChangePublishesNothing(t *testing.T) {
	f := newTownResendFixture(t)
	f.service.bindGameSessionCharacter(f.local, f.local.selectedCharacterID)
	f.service.options.currentClientVersion = "official-24.9"
	repos, _ := f.service.repositoryGroup()
	fault := &remoteActorSnapshotFaultEquipment{EquipmentRepository: repos.Equipment, onLoad: func() { f.peer.selectedCharacterID = 29 }}
	repos.Equipment = fault
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	_ = f.service.sendDungeonPartyPeerActorState(f.local, f.peer, "selection_change")
	if f.localBuf.write.Len() != 0 {
		t.Fatal("obsolete dungeon actor sent after selection changed")
	}
}

func TestPartyProjectionProbeCurrentRosterStillDelivered(t *testing.T) {
	h := newPartyLeaveHarness(t)
	h.service.options.currentClientVersion = "official-24.9"
	state := runtimePartyStateSnapshot(h.member)
	sent, err := h.service.sendCurrentPartyActorFrameProjectionBefore(h.leader, h.member, state, "current_roster_probe", func() error { return nil })
	if err != nil || !sent || h.leaderConn.write.Len() == 0 {
		t.Fatalf("current roster sent=%t bytes=%d err=%v", sent, h.leaderConn.write.Len(), err)
	}
}

func TestPartyProjectionProbeDungeonEquipmentSnapshotSurvivesLaterReadFailure(t *testing.T) {
	f := newTownResendFixture(t)
	f.service.bindGameSessionCharacter(f.local, f.local.selectedCharacterID)
	f.service.options.currentClientVersion = "official-24.9"
	repos, _ := f.service.repositoryGroup()
	seedLiveCharacter631WornSlots(t, repos)
	record, _, err := repos.Equipment.Load(context.Background(), "6")
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{CharacterID: "17", Entries: map[string]dnfrepo.EquipmentEntry{"12": record.Entries["12"]}}); err != nil {
		t.Fatal(err)
	}
	fault := &remoteActorSnapshotFaultEquipment{EquipmentRepository: repos.Equipment, failOnMode1: true, err: errors.New("later mode1 read unavailable")}
	repos.Equipment = fault
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	// The subsequent op9 builds a separate identity roster. Count storage
	// reads when the completed mode0/mode1 pair first reaches the wire.
	appearanceReads := -1
	f.local.conn = &partyProjectionWriteProbe{bufferConn: f.localBuf, beforeWrite: func() {
		if appearanceReads < 0 {
			appearanceReads = len(fault.loads)
		}
	}}
	if err := f.service.sendDungeonPartyPeerActorState(f.local, f.peer, "dungeon_snapshot_probe"); err != nil {
		t.Fatal(err)
	}
	f.local.conn = f.localBuf
	frames := f.frames()
	if len(frames) < 2 || frames[0].msgID != 2 || frames[1].msgID != 2 || frames[1].body[0] != 1 {
		t.Fatalf("bad frames=%v", frameIDs(frames))
	}
	body := frames[1].body
	if binary.LittleEndian.Uint16(body[21:23]) != 17 || body[currentMode1CreateCountOffset] != 1 {
		t.Fatalf("dungeon peer equipment cleared after healthy appearance: key=%d rows=%d loads=%v", binary.LittleEndian.Uint16(body[21:23]), body[currentMode1CreateCountOffset], fault.loads)
	}
	row := body[currentMode1CreateRowsOffset:]
	if row[0] != 12 || binary.LittleEndian.Uint32(row[1:5]) != 101040493 || binary.LittleEndian.Uint16(row[10:12]) != 48 {
		t.Fatalf("peer weapon differs: %x", row[:12])
	}
	if appearanceReads != 1 {
		t.Fatalf("appearance equipment reads=%d, want one pinned snapshot; total reads=%v", appearanceReads, fault.loads)
	}
}

type partyProjectionWriteProbe struct {
	*bufferConn
	beforeWrite func()
}

func (c *partyProjectionWriteProbe) Write(body []byte) (int, error) {
	c.beforeWrite()
	return c.bufferConn.Write(body)
}
