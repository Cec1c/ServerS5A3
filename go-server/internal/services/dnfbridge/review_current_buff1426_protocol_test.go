package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func reviewCurrentBuffCommand(skill uint16, slot, list byte, index uint16) []byte {
	body := []byte{0, 0, slot, list, 0, 0}
	binary.LittleEndian.PutUint16(body, skill)
	binary.LittleEndian.PutUint16(body[4:], index)
	return body
}

func reviewCurrentBuffDispatch(t *testing.T, s *Service, session *gameSession, body []byte) {
	t.Helper()
	if err := s.handleGameCommand(session, 1, 1426, body); err != nil {
		t.Fatal(err)
	}
}

func reviewCurrentBuffSnapshot(t *testing.T, conn *bufferConn, skill uint16, bound bool) []byte {
	t.Helper()
	rawPacket, _ := splitGameServerUpperPacket(t, append([]byte(nil), conn.write.Bytes()...))
	if rawPacket.Header.Classification != 0 {
		t.Fatal("current snapshot must be class0 notification")
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 1 || packets[0].msgID != 1368 {
		t.Fatalf("current snapshot must be exactly1368: %v", packets)
	}
	got := packets[0].body
	want := make([]byte, 168)
	binary.LittleEndian.PutUint16(want, skill)
	want[2] = 33
	for slot := byte(0); slot < 33; slot++ {
		row := want[3+int(slot)*5 : 8+int(slot)*5]
		copy(row, []byte{0, slot, 45, 255, 255})
		if bound && slot == 12 {
			copy(row, []byte{1, 12, 0, 54, 0})
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("current consumer168B snapshot mismatch: got%x want%x", got, want)
	}
	conn.write.Reset()
	return got
}

func reviewCurrentBuffProvenance(t *testing.T, r repo.Group, skill uint16, slots int) {
	t.Helper()
	state, found, err := buffswap.Load(context.Background(), r, "1", "dnf:10001")
	if err != nil || !found || state.Skill != skill || len(state.Slots) != slots {
		t.Fatalf("persisted state=%+v found=%t err=%v", state, found, err)
	}
	rec, found, err := r.Settings.Load(context.Background(), buffswap.Scope("1"))
	if err != nil || !found {
		t.Fatalf("settings found=%t err=%v", found, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rec.Values["state"]), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["wire_profile"]) != `"current1426-1368"` || rec.Values["source"] != "current_exe_994D20" {
		t.Fatalf("current provenance missing: %v", rec.Values)
	}
}

func TestReviewCurrentBuff1426OwnedRegistrationAndColdRestore(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	if err := r.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	s, session, conn, logger, logPath := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	session.sceneBootstrapTailSent = true
	// Current skill selection immediately emits994D20; final-close is no commit.
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 33, 45, 0xffff))
	reviewCurrentBuffProvenance(t, r, 38, 0)
	reviewCurrentBuffSnapshot(t, conn, 38, false)
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 12, 0, 54))
	reviewCurrentBuffProvenance(t, r, 38, 1)
	registered, _, err := buffswap.Load(context.Background(), r, "1", "dnf:10001")
	if err != nil || registered.Slots[12].ItemID != 101040493 || registered.Slots[12].Instance == "" {
		t.Fatalf("owned item identity not saved: %+v %v", registered, err)
	}
	snapshot := reviewCurrentBuffSnapshot(t, conn, 38, true)
	for cold := 0; cold < 2; cold++ {
		fresh, coldSession, coldConn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
		// Actual finish-loading boundary, new Service/session; no registration call,
		// no transient-session profile flag and no direct Apply/forced snapshot helper.
		coldSession.postFinishLoadingPlayerStateSent = true
		if err := fresh.sendCurrentFinishLoadingCharacterState(coldSession, "current_cold_login"); err != nil {
			t.Fatal(err)
		}
		packets := drainUpperPackets(t, coldConn)
		if len(packets) < 3 || packets[0].msgID != 37 || packets[1].msgID != 30 || packets[len(packets)-1].msgID != 1368 || !bytes.Equal(packets[len(packets)-1].body, snapshot) {
			t.Fatalf("cold registration restore/order: %v", packets)
		}
	}
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(38, 12, 45, 0xffff))
	reviewCurrentBuffProvenance(t, r, 38, 0)
	reviewCurrentBuffSnapshot(t, conn, 38, false)
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(0, 33, 45, 0xffff))
	reviewCurrentBuffProvenance(t, r, 0, 0)
	reviewCurrentBuffSnapshot(t, conn, 0, false)
	fresh, coldSession, coldConn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	if err := fresh.sendBuffSwapReapply(coldSession, "cold_empty_after_clear"); err != nil {
		t.Fatal(err)
	}
	reviewCurrentBuffSnapshot(t, coldConn, 0, false)
	log := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(log, "game-legacy-fps-devide-collect") || !strings.Contains(log, "game-buff-swap-native-recorded") {
		t.Fatalf("current commands took FPS route or were not recorded: %s", log)
	}
}

func TestReviewCurrentBuff1426RejectionsAndFPSCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       []byte
		owner      string
		unselected bool
	}{
		{name: "length0", body: nil},
		{name: "length5", body: make([]byte, 5)},
		{name: "length7", body: make([]byte, 7)},
		{name: "length976_unproven_current_shape", body: make([]byte, 976)},
		{name: "invalid_list44", body: reviewCurrentBuffCommand(38, 33, 44, 0xffff)},
		{name: "invalid_slot", body: reviewCurrentBuffCommand(38, 34, 45, 0xffff)},
		{name: "invalid_null_index", body: reviewCurrentBuffCommand(38, 12, 45, 54)},
		{name: "missing_owned_item", body: reviewCurrentBuffCommand(38, 12, 0, 54)},
		{name: "wrong_owner", body: reviewCurrentBuffCommand(38, 33, 45, 0xffff), owner: "dnf:other"},
		{name: "missing_authenticated_account", body: reviewCurrentBuffCommand(38, 33, 45, 0xffff), owner: " "},
		{name: "unselected", body: reviewCurrentBuffCommand(38, 33, 45, 0xffff), unselected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			r := newSkillSwitchInventoryRepos(t)
			s, session, conn, logger, path := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
			session.sceneBootstrapTailSent = false
			if tc.owner != "" {
				session.accountID = tc.owner
			}
			if tc.unselected {
				session.selectedCharacterID = 0
			}
			reviewCurrentBuffDispatch(t, s, session, tc.body)
			if _, found, err := r.Settings.Load(context.Background(), buffswap.Scope("1")); err != nil || found {
				t.Fatalf("rejected command saved state: %t %v", found, err)
			}
			if conn.write.Len() != 0 || session.fpsFinishLoadingGateSent {
				t.Fatal("rejected BUFF changed FPS gate or wrote packets")
			}
			log := readPeerEndpointTestLog(t, logger, path)
			if !strings.Contains(log, "game-buff-swap-native-rejected") || strings.Contains(log, "game-legacy-fps-devide-collect") {
				t.Fatalf("wrong rejection route: %s", log)
			}
		})
	}
	for _, tc := range []struct {
		name, env string
		body      []byte
	}{
		{"historical_fps976_default_off", "off", make([]byte, 976)},
		{"default_off_six", "off", reviewCurrentBuffCommand(38, 33, 45, 0xffff)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, tc.env)
			r := newSkillSwitchInventoryRepos(t)
			s, session, conn, logger, path := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
			session.sceneBootstrapTailSent = true
			reviewCurrentBuffDispatch(t, s, session, tc.body)
			if _, found, err := r.Settings.Load(context.Background(), buffswap.Scope("1")); err != nil || found {
				t.Fatalf("FPS/off wrote state: %t %v", found, err)
			}
			if conn.write.Len() != 0 {
				t.Fatal("FPS after bootstrap wrote packets")
			}
			log := readPeerEndpointTestLog(t, logger, path)
			if !strings.Contains(log, "game-legacy-fps-devide-collect-gate-skipped") || strings.Contains(log, "game-buff-swap-native") {
				t.Fatalf("old FPS/off route changed: %s", log)
			}
		})
	}
}

func TestReviewCurrentBuff1426MigratesHistoricalStateWithoutLosingBinding(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	if err := r.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	session.sceneBootstrapTailSent = true
	// Historical1428/44 route and1369 bytes must continue unchanged.
	if err := s.handleGameCommand(session, 1, 1428, reviewCurrentBuffCommand(38, 12, 0, 54)); err != nil {
		t.Fatal(err)
	}
	old := drainUpperPackets(t, conn)
	if len(old) != 1 || old[0].msgID != 1369 || !bytes.Equal(old[0].body[3:8], []byte{0, 0, 44, 255, 255}) {
		t.Fatalf("historical compatibility changed: %v", old)
	}
	conn.write.Reset()
	reviewCurrentBuffDispatch(t, s, session, reviewCurrentBuffCommand(39, 33, 45, 0xffff))
	reviewCurrentBuffProvenance(t, r, 39, 1)
	reviewCurrentBuffSnapshot(t, conn, 39, true)
	// Another historical command deliberately selects the historical profile.
	if err := s.handleGameCommand(session, 1, 1428, reviewCurrentBuffCommand(38, 33, 44, 0xffff)); err != nil {
		t.Fatal(err)
	}
	old = drainUpperPackets(t, conn)
	if len(old) != 1 || old[0].msgID != 1369 {
		t.Fatalf("historical switch did not retain1369: %v", old)
	}
	state, found, err := buffswap.Load(context.Background(), r, "1", "dnf:10001")
	if err != nil || !found || len(state.Slots) != 1 || state.Slots[12].ItemID != 101040493 {
		t.Fatalf("profile transition lost item: %+v %v", state, err)
	}
}
