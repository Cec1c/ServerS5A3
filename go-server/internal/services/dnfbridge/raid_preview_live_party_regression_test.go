package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The owner is formed through the real application/approval handlers. Keep
// its automatic seats intact: an unedited nonzero seat is not a real party.
func raidPreviewLivePartyFixture(t *testing.T, explicit bool) (*r195SoloPrefixFixture, *gameSession, *bufferConn, map[uint16][2]byte) {
	t.Helper()
	f := newR195SoloPrefixFixture(t)
	want := map[uint16][2]byte{f.leaderID: {0, 0}, f.memberID: {0, 0}}
	if explicit {
		body := make([]byte, 12)
		binary.LittleEndian.PutUint32(body[4:8], uint32(f.leaderID))
		binary.LittleEndian.PutUint32(body[8:12], 2)
		if err := f.service.handleGameCommand(f.leader, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketRaidManagerWork), body); err != nil {
			t.Fatal(err)
		}
		member := mustR195SoloPrefixMember(t, f.state(t), f.leaderID)
		if !member.RecruitingPlacementExplicit || member.GroupIndex != 2 {
			t.Fatal("fixture did not record explicit manager placement")
		}
		want[f.leaderID] = [2]byte{2, member.SlotOrder}
	}
	repos, ok := f.service.repositoryGroup()
	if !ok || repos.Character == nil {
		t.Fatal("missing fixture character repository")
	}
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "raid-preview-viewer", Name: "viewer", Level: 90, Job: "0",
	}); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	viewer := &gameSession{conn: conn, connID: "raid-preview-viewer", channel: f.leader.channel,
		accountID: "raid-preview-viewer", loginPacketSeen: true, accountFromToken: true}
	f.service.bindGameSessionCharacter(viewer, 77)
	if !f.service.currentRaidAuthenticated(viewer) {
		t.Fatal("preview viewer is not an authenticated selected owner")
	}
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	conn.write.Reset()
	return f, viewer, conn, want
}

func raidPreviewMemberEndForTest(t *testing.T, body []byte, offset int) int {
	t.Helper()
	if offset < 0 || len(body)-offset < 7 {
		t.Fatal("preview member name header truncated")
	}
	nameLen := uint64(binary.LittleEndian.Uint32(body[offset+3:]))
	offset += 7
	if nameLen > uint64(len(body)-offset) {
		t.Fatal("preview member name truncated")
	}
	offset += int(nameLen)
	if len(body)-offset < 26 { // 21-byte core tail, extra byte, extra DSTR length.
		t.Fatal("preview member tail truncated")
	}
	offset += 21
	extraLen := uint64(binary.LittleEndian.Uint32(body[offset+1:]))
	offset += 5
	if extraLen > uint64(len(body)-offset) {
		t.Fatal("preview member extra name truncated")
	}
	return offset + int(extraLen)
}

func raidPreviewMetadataEndForTest(t *testing.T, body []byte, offset int, key uint32) int {
	t.Helper()
	if offset < 0 || len(body)-offset < 8 || binary.LittleEndian.Uint32(body[offset:]) != key {
		t.Fatal("preview metadata key/header mismatch")
	}
	nameLen := uint64(binary.LittleEndian.Uint32(body[offset+4:]))
	offset += 8
	if nameLen > uint64(len(body)-offset) || uint64(len(body)-offset)-nameLen < 11 {
		t.Fatal("preview metadata name/flags truncated")
	}
	return raidPreviewMemberEndForTest(t, body, offset+int(nameLen)+11)
}

func raidPreviewMetadataLeaderGroupForTest(t *testing.T, object []byte, key uint32) byte {
	t.Helper()
	// Adapt only the envelope to the existing byte reader, never the expected
	// group value or a production metadata/projection builder.
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body, key)
	binary.LittleEndian.PutUint32(body[4:], 2)
	body = append(body, object...)
	return r195SoloPrefixMetadataLeaderGroup(t, body, key)
}

func raidPreviewACKRosterForTest(t *testing.T, body []byte, key uint32, selector byte) (byte, []raidRefreshMemberForTest) {
	t.Helper()
	if len(body) < 7 || body[0] != 1 || body[1] != selector || binary.LittleEndian.Uint32(body[2:]) != key {
		t.Fatal("preview must be successful ACK1359 for the requested owner/selector")
	}
	offset := 6
	leaderGroup := byte(0)
	if selector == 2 {
		end := raidPreviewMetadataEndForTest(t, body, offset, key)
		leaderGroup = raidPreviewMetadataLeaderGroupForTest(t, body[offset:end], key)
		offset = end
	}
	roster := make([]byte, 8)
	binary.LittleEndian.PutUint32(roster, key)
	binary.LittleEndian.PutUint32(roster[4:], 3)
	roster = append(roster, body[offset:]...)
	return leaderGroup, parseR195CurrentRaidRoster(t, roster, key)
}

func assertRaidPreviewGroupsForTest(t *testing.T, rows []raidRefreshMemberForTest, want map[uint16][2]byte) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("preview roster count=%d want=%d", len(rows), len(want))
	}
	seen := make(map[uint16]bool, len(rows))
	for _, row := range rows {
		placement, ok := want[row.CharID]
		if !ok || seen[row.CharID] || [2]byte{row.GroupIndex, row.SlotOrder} != placement {
			t.Fatalf("preview member=%d placement=%v want=%v known=%t duplicate=%t", row.CharID,
				[2]byte{row.GroupIndex, row.SlotOrder}, placement, ok, seen[row.CharID])
		}
		seen[row.CharID] = true
	}
}

func assertRaidPreviewViewerUnjoinedForTest(t *testing.T, f *r195SoloPrefixFixture, viewer *gameSession, before runtimeRaidState) {
	t.Helper()
	if _, joined := f.service.runtimeRaidForMember(viewer.selectedCharacterID); joined || len(f.service.raids) != 1 {
		t.Fatal("read-only preview assigned viewer raid membership")
	}
	if after := f.state(t); !reflect.DeepEqual(after, before) {
		t.Fatal("read-only preview changed stored raid seats or markers")
	}
	if f.leaderConn.write.Len() != 0 || f.memberConn.write.Len() != 0 {
		t.Fatal("read-only preview broadcast to existing members")
	}
	if party := runtimePartyStateSnapshot(viewer); len(runtimePartyMembers(party)) > 1 {
		t.Fatal("read-only preview created an ordinary party for viewer")
	}
}

func TestRaidPreviewLivePartyDirectoryAndSearchProjectSoloAndExplicitPlacement(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := map[bool]string{false: "untouched", true: "explicit_leader"}[explicit]
		t.Run(name, func(t *testing.T) {
			f, viewer, conn, want := raidPreviewLivePartyFixture(t, explicit)
			before := f.state(t)
			entries := f.service.currentRaidSearchEntries(viewer, raid.CurrentSearchRequest{Mode: 1}, "")
			if len(entries) != 1 || entries[0].RaidKey != f.raidKey || len(entries[0].Members) != len(want) {
				t.Fatal("directory lost actual raid/member owners")
			}
			rows := make([]raidRefreshMemberForTest, 0, len(entries[0].Members))
			for _, member := range entries[0].Members {
				rows = append(rows, raidRefreshMemberForTest{CharID: member.Core.CharKey,
					GroupIndex: member.Core.GroupIndex, SlotOrder: member.Core.SlotOrder})
			}
			assertRaidPreviewGroupsForTest(t, rows, want)
			for _, catalog := range []bool{true, false} {
				conn.write.Reset()
				request, opcode, offset := []byte{1}, uint16(590), 4
				if !catalog {
					request, opcode, offset = currentSearchBody(0, 5, false, true, true, ""), 753, 12
				}
				if err := f.service.handleGameCommand(viewer, 1, 831, request); err != nil {
					t.Fatal(err)
				}
				packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if packet.Header.MsgID != opcode || packet.Header.Classification != 0 || len(rest) != 0 ||
					len(packet.Body) < offset || binary.LittleEndian.Uint32(packet.Body[offset-4:]) != 1 {
					t.Fatal("directory/search reply has wrong opcode, count or extra membership frames")
				}
				end := raidPreviewMetadataEndForTest(t, packet.Body, offset, f.raidKey)
				if len(packet.Body) != end+1 || packet.Body[end] != byte(len(want)) {
					t.Fatal("directory must contain metadata and actual member count, without a roster")
				}
				if got := raidPreviewMetadataLeaderGroupForTest(t, packet.Body[offset:end], f.raidKey); got != want[f.leaderID][0] {
					t.Fatalf("directory/search leader group=%d want=%d", got, want[f.leaderID][0])
				}
				assertRaidPreviewViewerUnjoinedForTest(t, f, viewer, before)
			}
		})
	}
}

func TestRaidPreviewLivePartyInfoACKProjectsSoloAndExplicitWithoutJoiningViewer(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, selector := range []byte{1, 2} {
			name := map[bool]string{false: "untouched", true: "explicit_leader"}[explicit] + map[byte]string{1: "/selector1", 2: "/selector2"}[selector]
			t.Run(name, func(t *testing.T) {
				f, viewer, conn, want := raidPreviewLivePartyFixture(t, explicit)
				before := f.state(t)
				request := []byte{selector, 0, 0, 0, 0}
				binary.LittleEndian.PutUint32(request[1:], f.raidKey)
				if err := f.service.handleOnlineRequestRaidInfo(viewer, request); err != nil {
					t.Fatal(err)
				}
				raw := consumeCurrentRaidProfiles(t, conn.write.Bytes(), before)
				packet, rest := splitGameServerUpperPacket(t, raw)
				if packet.Header.Classification != 1 || packet.Header.MsgID != 1359 || len(rest) != 0 {
					t.Fatal("viewer must receive ACK1359 without own-membership NOTI591")
				}
				leaderGroup, rows := raidPreviewACKRosterForTest(t, packet.Body, f.raidKey, selector)
				assertRaidPreviewGroupsForTest(t, rows, want)
				if selector == 2 && leaderGroup != want[f.leaderID][0] {
					t.Fatalf("ACK metadata leader group=%d want=%d", leaderGroup, want[f.leaderID][0])
				}
				assertRaidPreviewViewerUnjoinedForTest(t, f, viewer, before)
			})
		}
	}
}

func TestRaidPreviewLivePartyRestoreACKUsesLiveSoloProjection(t *testing.T) {
	f, _, _, want := raidPreviewLivePartyFixture(t, true)
	f.installBattle(t)
	f.service.raidMu.Lock()
	live := f.service.raids[f.raidKey]
	live.Battle.Configuration.PhaseLimitSeconds = []uint32{120, 120}
	live.Battle.Deadline = time.Now().Add(time.Minute)
	f.service.raidMu.Unlock()
	before := f.state(t)
	// Deliberately retain the old explicit placement and false solo marker.
	// Once started, the absent live ordinary party must win for every member.
	want[f.leaderID] = [2]byte{0, 0}
	f.service.markCurrentRaidSessionRestore(f.leader, f.leaderID)
	if pending, _, _ := r195RaidSessionRestoreFlags(f.leader); !pending {
		t.Fatal("fixture did not enter actual session restore gate")
	}
	if err := f.service.restoreCurrentRaidSessionProjection(f.leader, "preview_live_party_regression"); err != nil {
		t.Fatal(err)
	}
	packets := r195ParseUpperFrames(t, f.leaderConn)
	ackIndex, rosterIndex := -1, -1
	for i, packet := range packets {
		if packet.Header.Classification == 1 && packet.Header.MsgID == 1359 {
			ackIndex = i
			leaderGroup, rows := raidPreviewACKRosterForTest(t, packet.Body, f.raidKey, 2)
			assertRaidPreviewGroupsForTest(t, rows, want)
			if leaderGroup != 0 {
				t.Fatalf("restore ACK retained recruiting group=%d after start", leaderGroup)
			}
		}
		if mode, ok := r195RaidRefreshMode(packet); ok && mode == 3 {
			rosterIndex = i
			assertRaidPreviewGroupsForTest(t, parseR195CurrentRaidRoster(t, packet.Body, f.raidKey), want)
		}
	}
	if ackIndex < 0 || rosterIndex <= ackIndex {
		t.Fatal("restore must send object ACK before matching own-membership roster")
	}
	if pending, _, _ := r195RaidSessionRestoreFlags(f.leader); pending {
		t.Fatal("successful restore retained pending gate")
	}
	if after := f.state(t); !reflect.DeepEqual(after, before) {
		t.Fatal("restore projection changed stored placement or frozen battle authority")
	}
}
