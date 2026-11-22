package dnfbridge

import (
	"encoding/binary"
	"testing"
)

// Decode kind2 independently from the writer, including both variable-length
// official identity strings. This table starts immediately after the header.
func retainedDepartureRows(t *testing.T, body []byte, channel int) []op9PartyRow {
	t.Helper()
	if len(body) < 15 || body[9] != 2 {
		t.Fatalf("invalid kind2 record: %x", body)
	}
	offset := 11
	rows := make([]op9PartyRow, 0, int(body[10]))
	for i := 0; i < int(body[10]); i++ {
		if offset+3 > len(body) {
			t.Fatal("truncated kind2 row")
		}
		row := op9PartyRow{slot: body[offset], uid: binary.LittleEndian.Uint16(body[offset+1:])}
		offset += 3
		if int(body[7]) != channel {
			offset += 5
			for j := 0; j < 2; j++ {
				if offset+4 > len(body) {
					t.Fatal("truncated kind2 identity length")
				}
				n := int(binary.LittleEndian.Uint32(body[offset:]))
				offset += 4
				if n > len(body)-offset {
					t.Fatal("truncated kind2 identity")
				}
				offset += n
			}
			offset++
		}
		offset += 3
		rows = append(rows, row)
	}
	if offset+4 != len(body) {
		t.Fatalf("kind2 end=%d len=%d", offset+4, len(body))
	}
	return rows
}

func TestDungeonRetainedRecordKeepsEverySurvivorAcrossConsecutiveDepartures(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	p101 := addRetirementPeer(t, f, 101)
	p102 := addRetirementPeer(t, f, 102)
	peers := []*gameSession{f.leader, f.member, p101, p102}
	for _, p := range peers {
		p.channel.ID = 11
	}
	before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
	round69AlignInstancePartyID(t, f, before.ID)
	for _, leaver := range []*gameSession{f.member, f.leader} {
		for _, p := range peers {
			p.conn.(*bufferConn).write.Reset()
		}
		leaver.dungeon.runtime = nil
		if err := f.service.leaveRuntimePartyOnMemberGiveup(leaver, f.instance); err != nil {
			t.Fatal(err)
		}
		state, ok := f.service.runtimePartyManagerForService().SnapshotByUser(101, 0)
		if !ok {
			t.Fatal("survivor party disappeared")
		}
		for _, receiver := range []*gameSession{p101, p102} {
			updates := 0
			for _, frame := range splitUpperFrames(t, receiver.conn.(*bufferConn).write.Bytes()) {
				if frame.msgID != 9 || len(frame.body) < 10 || frame.body[9] != 2 {
					continue
				}
				rows := retainedDepartureRows(t, frame.body, 11)
				if len(rows) != len(state.Members) {
					t.Fatal("empty/stale roster would destroy surviving models")
				}
				for i, member := range state.Members {
					if rows[i].uid != member.UserID || rows[i].slot != member.Slot {
						t.Fatalf("changed survivor identity/slot: %+v", rows)
					}
					if member.UserID == state.Leader && frame.body[len(frame.body)-3] != member.Slot {
						t.Fatal("leader slot was compacted")
					}
				}
				updates++
			}
			if updates != 2 {
				t.Fatalf("expected channel and dungeon record updates; got %d", updates)
			}
		}
	}
}

func TestDungeonRetainedRecordRefusesPresentTarget(t *testing.T) {
	f := newLeaderExitFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	state := runtimePartyStateSnapshot(f.leader)
	f.leaderConn.write.Reset()
	if sent, err := f.service.sendCurrentPartyRecordProjectionInContext(f.leader, f.leader, state, "test", nil, currentPartyFrameOwnerSelection{}, 100); sent || err == nil {
		t.Fatal("accepted an active member as a departed record")
	}
	if f.leaderConn.write.Len() != 0 {
		t.Fatal("invalid target wrote a frame")
	}
}
