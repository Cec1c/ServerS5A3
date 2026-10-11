package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func loadCurrentAdmissionArchive(t *testing.T, s *Service) {
	t.Helper()
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires actual deployed PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.initialEquipmentArchive = a
}

func TestRealPVFCurrentApprovalRefreshesActualMaterialsAfterRoster(t *testing.T) {
	s, leader, member, lc, mc, _ := prepareCurrentRaidApplicationTest(t)
	loadCurrentAdmissionArchive(t, s)
	repos, _ := s.repositoryGroup()
	for i, session := range []*gameSession{leader, member} {
		id := strconv.Itoa(int(session.selectedCharacterID))
		slots := map[string]dnfrepo.ItemStack{}
		if i == 0 {
			slots["0:121"] = dnfrepo.ItemStack{ItemID: 10096296, Count: 1}
		}
		if err := repos.Inventory.Save(nil, dnfrepo.InventoryRecord{CharacterID: id, Slots: slots}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.handleGameCommand(member, 1, 820, currentRaidApplyFixture(leader.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	lc.write.Reset()
	mc.write.Reset()
	if err := s.handleGameCommand(leader, 1, 11, currentRaidApprovalFixture(member.selectedCharacterID)); err != nil {
		t.Fatal(err)
	}
	state, _ := s.runtimeRaidForMember(member.selectedCharacterID)
	var body []byte
	for _, conn := range []*bufferConn{lc, mc} {
		ps := splitAllGameServerUpperPackets(t, conn.write.Bytes())
		if len(ps) != 4 || ps[1].Header.MsgID != 752 || ps[2].Header.MsgID != 591 || ps[3].Header.MsgID != 598 || ps[3].Header.Classification != 0 {
			t.Fatalf("approval ordering=%+v", ps)
		}
		body = ps[3].Body
		if len(body) != 14 || binary.LittleEndian.Uint32(body) != 2 || binary.LittleEndian.Uint16(body[4:]) != leader.selectedCharacterID || body[6] != 1 || binary.LittleEndian.Uint16(body[7:]) != 1 || binary.LittleEndian.Uint16(body[9:]) != member.selectedCharacterID || body[11] != 0 || binary.LittleEndian.Uint16(body[12:]) != 0 {
			t.Fatalf("materials do not match actual repository: %x", body)
		}
	}
	inv, _, _ := repos.Inventory.Load(nil, strconv.Itoa(int(leader.selectedCharacterID)))
	if inv.Slots["0:121"].Count != 1 || state.Started {
		t.Fatal("display refresh consumed materials or started raid")
	}
	if dir := os.Getenv("DNF90_CURRENT_RAID_ADMISSION_NATIVE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "raid-admission-bridge-598.bin"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	mc.write.Reset()
	query := make([]byte, 5)
	query[0] = 1
	binary.LittleEndian.PutUint32(query[1:], state.RaidKey)
	if err := s.handleOnlineRequestRaidInfo(member, query); err != nil {
		t.Fatal(err)
	}
	ps := splitAllGameServerUpperPackets(t, mc.write.Bytes())
	if len(ps) != 4 || ps[2].Header.MsgID != 591 || ps[3].Header.MsgID != 598 || !bytes.Equal(ps[3].Body, body) {
		t.Fatal("member query omitted current inventory refresh")
	}
}

func TestCurrentAdmissionRefreshRejectsForeignStaleAndViewerContexts(t *testing.T) {
	for _, kind := range []string{"channel", "owner", "retired", "removed", "reordered", "started", "viewer", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			s, _, member, _, mc, state := prepareApprovedCurrentRaid(t)
			switch kind {
			case "channel":
				member.channel.ID = 24
			case "owner":
				member.accountID = "foreign"
			case "retired":
				s.unbindGameSession(member)
			case "removed":
				s.raids[state.RaidKey].Members = s.raids[state.RaidKey].Members[:1]
			case "reordered":
				state.Members[0], state.Members[1] = state.Members[1], state.Members[0]
			case "started":
				s.raids[state.RaidKey].Started = true
			case "viewer":
				state.Members = state.Members[:1]
			case "legacy":
				s.options.currentClientVersion = "legacy"
			}
			if s.currentRaidAdmissionVisible(member, state) {
				t.Fatal("foreign or stale admission snapshot was visible")
			}
			if err := s.sendCurrentRuntimeRaidEntryCost(member, state); err != nil {
				t.Fatal(err)
			}
			if mc.write.Len() != 0 {
				t.Fatal("admission leaked to unowned context")
			}
		})
	}
}
