package dnfbridge

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Export authenticated sender output for the registered class0 584 consumer.
// The live-squad ledger is controlled; PVF configuration and projection are real.
func TestRealPVFRaidParticipationNativeCorpus(t *testing.T) {
	out := os.Getenv("DNF_RAID_PARTICIPATION_CORPUS")
	if out == "" {
		t.Skip("requires native corpus output path")
	}
	f, session, _ := realAntonProjectionFixture(t, 0)
	b := f.state.Battle
	f.state.Members = []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}, {CharID: 20, GroupIndex: 2}}
	b.MemberAccounts[20] = "account-20"
	b.MemberGroups = map[uint16]byte{19: 1, 20: 1}
	b.Participation = map[uint16]currentRaidParticipation{
		19: {RunID: "run-a", DungeonID: 210, Phase: b.Phase, Attempt: b.Anton.Epoch()},
		20: {RunID: "run-b", DungeonID: 210, Phase: b.Phase, Attempt: b.Anton.Epoch()},
	}
	scenarios := []map[string]any{}
	collect := func(id string, fresh bool, want [][]uint16) {
		t.Helper()
		f.conn.write.Reset()
		if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, fresh); err != nil {
			t.Fatal(err)
		}
		rows := []map[string]any{}
		stream := append([]byte(nil), f.conn.write.Bytes()...)
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			if packet.Header.MsgID == 584 {
				if packet.Header.Classification != 0 {
					t.Fatalf("584 class=%d", packet.Header.Classification)
				}
				rows = append(rows, map[string]any{"opcode": 584, "packet_class": 0, "body_hex": hex.EncodeToString(packet.Body)})
			}
			stream = rest
		}
		scenarios = append(scenarios, map[string]any{"id": id, "packets": rows, "expected_squads": want})
	}
	collect("two_separate_squads", false, [][]uint16{{19}, {20}})
	collect("same_connection_resubscribe", true, [][]uint16{{19}, {20}})
	f.state.Members = f.state.Members[:1]
	b.ProgressVersion++
	collect("departed_member_removed", false, [][]uint16{{19}})
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(map[string]any{"scenarios": scenarios, "dungeon": 210, "source": "authenticated production projection; real PVF; controlled live ledger", "gui_acceptance": false}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
