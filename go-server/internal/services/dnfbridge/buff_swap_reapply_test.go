package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestNativeBuffSwapWireAndLifecycle(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	ctx := context.Background()
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:54": {ItemID: 101040493, Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, r)
	// Literal observed Add Equipment and skill selection command bodies.
	for _, body := range [][]byte{{38, 0, 12, 0, 54, 0}, {38, 0, 33, 44, 255, 255}} {
		if handled, err := s.handleSkillSwitchInventoryCommand(session, 1428, body); err != nil || !handled {
			t.Fatalf("registration: %t %v", handled, err)
		}
	}
	changed := drainUpperPackets(t, conn)
	if len(changed) != 2 || changed[0].msgID != 1369 || changed[1].msgID != 1369 {
		t.Fatal("registration did not reset stale native bonus state")
	}
	conn.write.Reset()
	if _, found, err := r.Settings.Load(ctx, "character:1:skill_switch_inventory"); err != nil || found {
		t.Fatal("native mode touched legacy state")
	}
	session.postFinishLoadingPlayerStateSent = true
	if err := s.sendCurrentFinishLoadingCharacterState(session, "dungeon_test"); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), conn.write.Bytes()...)
	var ids []uint16
	var snapshot []byte
	for len(raw) > 0 {
		p, rest := splitGameServerUpperPacket(t, raw)
		ids = append(ids, p.Header.MsgID)
		if p.Header.MsgID == 1369 {
			if p.Header.Classification != 0 {
				t.Fatal("snapshot was not native notification class0")
			}
			snapshot = p.Body
		}
		raw = rest
	}
	if len(ids) < 3 || ids[0] != 37 || ids[1] != 30 || ids[len(ids)-1] != 1369 {
		t.Fatalf("restore order: %v", ids)
	}
	if len(snapshot) != 168 || binary.LittleEndian.Uint16(snapshot) != 38 || snapshot[2] != 33 {
		t.Fatalf("native snapshot header %x", snapshot)
	}
	for slot := 0; slot < 33; slot++ {
		row := snapshot[3+slot*5 : 8+slot*5]
		want := []byte{0, byte(slot), 44, 255, 255}
		if slot == 12 {
			want = []byte{1, 12, 0, 54, 0}
		}
		if !bytes.Equal(row, want) {
			t.Fatalf("slot%d: %x want %x", slot, row, want)
		}
	}
	conn.write.Reset()
	if err := s.sendCurrentFinishLoadingCharacterState(session, "repeat"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("duplicate finish-loading repeated native reset")
	}
	// A later actor/equipment refresh restores the same exact state again.
	if err := s.sendBuffSwapReapply(session, "equipment_test"); err != nil {
		t.Fatal(err)
	}
	p := drainUpperPackets(t, conn)
	if len(p) != 1 || p[0].msgID != 1369 || !bytes.Equal(p[0].body, snapshot) {
		t.Fatal("reentry changed registered state")
	}
}

func TestNativeBuffSwapDefaultOffAndRollback(t *testing.T) {
	r := newSkillSwitchInventoryRepos(t)
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryReferenceDrop, r)
	for _, value := range []string{"", "off", "unknown", "0"} {
		t.Setenv(buffSwapReapplyEnv, value)
		if buffSwapReapplyEnabled() {
			t.Fatalf("unexpected enable %q", value)
		}
		if _, err := s.handleSkillSwitchInventoryCommand(session, 1428, []byte{38, 0, 33, 44, 255, 255}); err != nil {
			t.Fatal(err)
		}
		if err := s.sendBuffSwapReapply(session, "off_test"); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatal("off wrote bytes")
		}
		if _, found, _ := r.Settings.Load(context.Background(), buffswap.Scope("1")); found {
			t.Fatal("off wrote native state")
		}
	}
	t.Setenv(buffSwapReapplyEnv, "on")
	if _, err := s.handleSkillSwitchInventoryCommand(session, 1428, []byte{38, 0, 33, 44, 255, 255}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(buffSwapReapplyEnv, "off")
	conn.write.Reset()
	if err := s.sendBuffSwapReapply(session, "rollback_test"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("rollback wrote snapshot")
	}
	if _, found, _ := r.Settings.Load(context.Background(), buffswap.Scope("1")); !found {
		t.Fatal("rollback erased native state")
	}
}

func TestNativeBuffSwapRebuildRestoresAfterFinalActorBinding(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	s, session, conn, _ := newSceneReadyReplayFixture(t)
	s.options.gameUpperHeader = gameUpperHeaderChannel13
	r, _ := s.repositoryGroup()
	c, found, err := r.Character.Load(context.Background(), "6")
	if err != nil || !found {
		t.Fatal("fixture character missing", err)
	}
	session.accountID = c.AccountID
	if err := buffswap.Apply(context.Background(), r, "6", c.AccountID, buffswap.Command{Skill: 38, Slot: 33, List: 44, Index: 0xffff}); err != nil {
		t.Fatal(err)
	}
	// This shared equipment fixture has a slot26 pet with serial0. Keep the
	// invalid instance and require its real rejection to remain visible while
	// proving that the independent persisted buff restoration still succeeds.
	err = s.rebuildLoginActorGenerationAfterSceneCommit(session, "relogin_test", 6)
	if err == nil || !strings.Contains(err.Error(), "equipped creature item refresh rejected invalid slot 26: item=400990170 serial=0") {
		t.Fatalf("invalid fixture pet rejection = %v", err)
	}
	p := drainUpperPackets(t, conn)
	if len(p) != 3 || p[0].msgID != 2 || p[1].msgID != 2 || p[2].msgID != 1369 {
		t.Fatalf("final actor restore order: %v", p)
	}
	if binary.LittleEndian.Uint16(p[2].body) != 38 {
		t.Fatal("relogin lost selected buff")
	}
	for _, refresh := range []struct {
		name   string
		call   func(*gameSession, string, string) error
		frames int
	}{
		{"mode0", s.sendSelectedActorMode0AppearanceRefresh, 2},
		{"mode1", s.sendSelectedActorMode1AppearanceRefresh, 2},
		{"mode0_mode1", s.sendSelectedActorAppearanceRefresh, 3},
	} {
		if err := refresh.call(session, "wear_test", refresh.name); err != nil {
			t.Fatal(err)
		}
		packets := drainUpperPackets(t, conn)
		if len(packets) != refresh.frames || packets[len(packets)-1].msgID != 1369 || binary.LittleEndian.Uint16(packets[len(packets)-1].body) != 38 {
			t.Fatalf("equipment %s restore order/state: %v", refresh.name, packets)
		}
	}
}
