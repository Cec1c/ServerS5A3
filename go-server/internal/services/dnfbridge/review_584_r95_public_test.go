package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Uses the real 369d program and authenticated production projection sender.
// Occupancy is a controlled authoritative ledger, not physical admission/UI.
func TestRealPVFReview584R95PublicSenderSeparatesLiveSquads(t *testing.T) {
	f, session, _ := realAntonProjectionFixture(t, 0)
	b := f.state.Battle
	f.state.Members = []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}, {CharID: 20, GroupIndex: 2}}
	b.MemberAccounts[20] = "account-20"
	b.MemberGroups = map[uint16]byte{19: 1, 20: 1}
	b.Participation = map[uint16]currentRaidParticipation{
		19: {RunID: "run-a", DungeonID: 210, Phase: b.Phase, Attempt: b.Anton.Epoch()},
		20: {RunID: "run-b", DungeonID: 210, Phase: b.Phase, Attempt: b.Anton.Epoch()},
	}
	readRows := func() [][]byte {
		t.Helper()
		var rows [][]byte
		stream := append([]byte(nil), f.conn.write.Bytes()...)
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			if packet.Header.MsgID == 584 {
				rows = append(rows, append([]byte(nil), packet.Body...))
			}
			stream = rest
		}
		f.conn.write.Reset()
		return rows
	}
	check := func(rows [][]byte, ids []uint16, operations []byte) {
		t.Helper()
		if len(rows) != len(ids)*len(operations) {
			t.Fatalf("live squad rows got=%d want=%d raw=%x", len(rows), len(ids)*len(operations), rows)
		}
		for i, operation := range operations {
			for j, id := range ids {
				row := rows[i*len(ids)+j]
				if len(row) != 9 || row[0] != 1 || binary.LittleEndian.Uint32(row[1:5]) != 210 || row[5] != operation || row[6] != 1 || binary.LittleEndian.Uint16(row[7:]) != id {
					t.Fatalf("row %d/%d=%x", i, j, row)
				}
			}
		}
	}
	if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
		t.Fatal(err)
	}
	check(readRows(), []uint16{19, 20}, []byte{0, 2})
	if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
		t.Fatal(err)
	}
	if rows := readRows(); len(rows) != 0 {
		t.Fatal("unchanged replay", rows)
	}
	if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, true); err != nil {
		t.Fatal(err)
	}
	check(readRows(), []uint16{19, 20}, []byte{0, 0, 2})
	f.state.Members = f.state.Members[:1]
	b.ProgressVersion++
	if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
		t.Fatal(err)
	}
	rows := readRows()
	want := [][]byte{{1, 210, 0, 0, 0, 0, 1, 19, 0}, {1, 210, 0, 0, 0, 0, 1, 20, 0}, {1, 210, 0, 0, 0, 0, 1, 19, 0}, {1, 210, 0, 0, 0, 2, 1, 19, 0}}
	if len(rows) != len(want) {
		t.Fatalf("departed roster rows=%x", rows)
	}
	for i := range want {
		if !bytes.Equal(rows[i], want[i]) {
			t.Fatalf("departed row%d=%x", i, rows[i])
		}
	}
	if b.MemberGroups[20] != 1 || len(b.Participation) != 2 {
		t.Fatal("projection mutated frozen rewards or runtime ledger")
	}
}
