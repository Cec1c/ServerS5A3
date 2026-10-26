package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func addRetirementPeer(t *testing.T, f leaderExitFixtureState, id uint16) *gameSession {
	t.Helper()
	repositories, _ := f.service.repositoryProvider()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: fmt.Sprint(id), AccountID: "account-1", Level: 20}); err != nil {
		t.Fatal(err)
	}
	p := &gameSession{conn: &bufferConn{}, channel: f.leader.channel, residentChannel: f.leader.residentChannel, selectedCharacterID: id}
	f.service.bindGameSessionCharacter(p, id)
	identity, _ := f.service.boundGameSessionCharacterSnapshot(p)
	member, ok := f.service.runtimePartyMemberForIdentity(identity)
	if !ok {
		t.Fatal("missing member")
	}
	manager := f.service.runtimePartyManagerForService()
	before, _ := manager.SnapshotByUser(99, 0)
	joined := manager.Join(before.ID, member)
	if !joined.OK {
		t.Fatal(joined.Reason)
	}
	f.service.publishRuntimePartySnapshot(joined.Party)
	f.instance.mu.Lock()
	f.instance.members[id] = time.Now()
	f.instance.sessions[id] = p
	f.instance.mu.Unlock()
	return p
}

func TestDungeonPeerRetirementOrderKeepsSurvivorSlots(t *testing.T) {
	for _, count := range []int{3, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			peers := []*gameSession{f.leader}
			for id := uint16(101); int(id) < 99+count; id++ {
				peers = append(peers, addRetirementPeer(t, f, id))
			}
			before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
			round69AlignInstancePartyID(t, f, before.ID)
			f.member.dungeon.runtime = nil
			if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); err != nil {
				t.Fatal(err)
			}
			for _, peer := range peers {
				frames := splitUpperFrames(t, peer.conn.(*bufferConn).write.Bytes())
				first, retired, removed, final := -1, -1, -1, -1
				for i, frame := range frames {
					if frame.msgID == 2 || frame.msgID == 11 {
						t.Fatal("retirement rebuilt an actor or restarted its connection")
					}
					if frame.msgID == 9 && len(frame.body) > 10 {
						rows := parseRetirementCurrentRows(t, frame.body)
						if len(rows) != count-1 {
							t.Fatalf("remaining rows=%+v", rows)
						}
						for _, row := range rows {
							if row.uid == 100 {
								t.Fatal("departed member in new roster")
							}
							for _, old := range before.Members {
								if old.UserID == row.uid && old.Slot != row.slot {
									t.Fatal("survivor moved slots")
								}
							}
						}
						if binary.LittleEndian.Uint16(frame.body[4:6]) == peer.selectedCharacterID {
							if first < 0 {
								first = i
							}
							final = i
						}
					}
					if frame.msgID == 9 && len(frame.body) == 10 && frame.body[9] == 3 {
						if binary.LittleEndian.Uint16(frame.body[4:6]) != 100 || retired >= 0 {
							t.Fatal("unexpected retired record")
						}
						retired = i
					}
					if frame.msgID == currentTownUserLeaveMsgID {
						if removed >= 0 || binary.LittleEndian.Uint16(frame.body[len(frame.body)-2:]) != 100 {
							t.Fatal("unexpected active removal")
						}
						removed = i
					}
				}
				if first < 0 || retired <= first || final <= retired || removed <= final {
					t.Fatalf("receiver %d: local=%d retire=%d restore=%d active=%d", peer.selectedCharacterID, first, retired, final, removed)
				}
			}
			if f.leader.dungeon.runtime != f.leaderRuntime {
				t.Fatal("survivor runtime changed")
			}
			for _, peer := range peers {
				peer.conn.(*bufferConn).write.Reset()
			}
			if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); err != nil {
				t.Fatal(err)
			}
			for _, peer := range peers {
				if peer.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("duplicate leave published again")
				}
			}
		})
	}
}

// Current owner0 records carry the actor +55 byte and extended inline member
// identity. The legacy parser deliberately does not consume these fields.
func parseRetirementCurrentRows(t *testing.T, body []byte) []op9PartyRow {
	t.Helper()
	if len(body) < 31 || body[9] != 0 {
		t.Fatalf("invalid current roster: %x", body)
	}
	offset := 16 + int(binary.LittleEndian.Uint32(body[12:16])) + 14
	if offset >= len(body) {
		t.Fatal("truncated current actor")
	}
	count := int(body[offset])
	offset++
	rows := make([]op9PartyRow, 0, count)
	for i := 0; i < count; i++ {
		if offset+8 > len(body) {
			t.Fatal("truncated current row")
		}
		row := op9PartyRow{slot: body[offset], uid: binary.LittleEndian.Uint16(body[offset+1:])}
		offset += 8
		for j := 0; j < 2; j++ {
			if offset+4 > len(body) {
				t.Fatal("truncated current identity length")
			}
			n := int(binary.LittleEndian.Uint32(body[offset:]))
			offset += 4
			if n > len(body)-offset {
				t.Fatal("truncated current identity")
			}
			offset += n
		}
		if offset+4 > len(body) {
			t.Fatal("truncated current identity tail")
		}
		offset += 4
		rows = append(rows, row)
	}
	if offset+4 != len(body) {
		t.Fatalf("current roster tail offset=%d len=%d", offset, len(body))
	}
	return rows
}

func TestDungeonPeerRetirementWriteFailurePreservesRemovalIdentity(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			addRetirementPeer(t, f, 101)
			before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
			round69AlignInstancePartyID(t, f, before.ID)
			failure := errors.New("retirement projection failed")
			conn := &failNthDungeonWriteConn{failAt: failAt, err: failure}
			f.leader.conn = conn
			if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); !errors.Is(err, failure) {
				t.Fatalf("err=%v", err)
			}
			for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
				if frame.msgID == currentTownUserLeaveMsgID {
					t.Fatal("actor identity erased after incomplete roster")
				}
			}
			if _, found := f.service.runtimePartyManagerForService().SnapshotByUser(100, 0); found {
				t.Fatal("failed write rolled back departure")
			}
			if f.leader.dungeon.runtime != f.leaderRuntime {
				t.Fatal("write failure destroyed survivor instance")
			}
		})
	}
}
