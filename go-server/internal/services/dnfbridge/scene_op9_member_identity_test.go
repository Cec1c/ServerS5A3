package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Extract the native cross-context identity after the slot and character key.
// This fixture uses one member; actor metadata has a different layout and is
// deliberately skipped rather than being interpreted as another member row.
func officialPartyMemberIdentityFixture(t *testing.T, body []byte) []byte {
	t.Helper()
	position := 10
	if body[9] == 0 {
		position = 12
		nameBytes := int(binary.LittleEndian.Uint32(body[position:]))
		position += 4 + nameBytes + 14
	}
	if body[position] != 1 {
		t.Fatalf("fixture member count=%d", body[position])
	}
	position += 4 // count, slot, u16 character key
	start := position
	position += 5
	for i := 0; i < 2; i++ {
		n := int(binary.LittleEndian.Uint32(body[position:]))
		position += 4 + n
	}
	position++ // secondary identity flag
	if position+3+4 != len(body) {
		t.Fatalf("identity boundary=%d packet size=%d", position, len(body))
	}
	return body[start:position]
}

func TestOfficialPartyMemberIdentityLevelBeforeJob(t *testing.T) {
	var nativeCases []map[string]any
	for _, job := range []byte{0, 3, 4, 9, 11, 15} {
		const level, grow = byte(90), byte(0x23)
		member := currentSceneOp9PartyMemberProjection{
			State: alignedcmd.PartyMemberState{UserID: 101, Slot: 2, UserState: 1},
			Name:  "native_identity", Job: job, Level: level, Grow: grow,
		}
		actor := dnfrepo.CharacterRecord{Name: member.Name, Job: fmt.Sprint(job), Level: int(level)}
		bodies := map[string][]byte{
			"ordinary": buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(101, actor, true, member.Name, 0, 23, []currentSceneOp9PartyMemberProjection{member}, 2, true),
			"retained": buildCurrentSceneOp9RetainedPartyBody(102, 0, 23, []currentSceneOp9PartyMemberProjection{member}, 2, 1),
			"room": buildDungeonPartyRoomSlotTableBodyForProfile(
				dungeonPartyRoomSlotTableActor{ObjectKey: 101, Name: member.Name, Job: job, Level: level, Grow: grow},
				[]dungeonPartyRoomSlotTableRow{{Slot: 2, UserID: 101, Name: member.Name, Job: job, Level: level, Grow: grow}},
				2, vendorDungeonPartyRoomSlotTableShape, true),
		}
		for kind, body := range bodies {
			identity := officialPartyMemberIdentityFixture(t, body)
			// 2016FC3 initializes profile+14 from byte 0; 2017037 initializes
			// profile+4 from byte 1. Distinct job/level values catch inversion.
			if identity[0] != level || identity[1] != job || identity[2] != grow {
				t.Fatalf("%s job=%d native level/job/grow=%x", kind, job, identity[:5])
			}
			nativeCases = append(nativeCases, map[string]any{"kind": kind, "job": job, "level": level, "grow": grow, "identityHex": hex.EncodeToString(identity)})
		}
	}
	// Optional fixtures for the separate machine-code reader. No native test
	// is launched here. Export only synthetic identities, never player rows.
	if path := os.Getenv("DNF_OP9_MEMBER_IDENTITY_FIXTURES"); path != "" {
		data, err := json.MarshalIndent(nativeCases, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("fixture export: write=%v close=%v", writeErr, closeErr)
		}
	}
}

func TestPartyMemberLevelJobKeepsLegacyAndSameChannelLayouts(t *testing.T) {
	member := currentSceneOp9PartyMemberProjection{State: alignedcmd.PartyMemberState{UserID: 101}, Name: "peer", Job: 9, Level: 90, Grow: 0x23}
	for _, official := range []bool{false, true} {
		body := buildCurrentSceneOp9ActorPartyDisplayBodyForProfile(101, dnfrepo.CharacterRecord{}, false, "", 23, 23, []currentSceneOp9PartyMemberProjection{member}, 0, official)
		position := 12 + 4 + 13
		if official {
			position++
		}
		if len(body) != position+1+6+4 || body[position] != 1 {
			t.Fatalf("same-channel identity unexpectedly added: official=%v size=%d", official, len(body))
		}
	}
	var writer packetWriter
	writeCurrentSceneOp9MemberLevelJob(&writer, 90, 9, false)
	if got := writer.bytes(); len(got) != 2 || got[0] != 9 || got[1] != 90 {
		t.Fatalf("legacy identity order changed: %x", got)
	}
}
