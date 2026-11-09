package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func pairedBuffCommand(group byte, skill uint16, slot, list byte, index uint16) []byte {
	return append([]byte{0xb7, group}, reviewCurrentBuffCommand(skill, slot, list, index)...)
}

func pairedBuffExpect(t *testing.T, conn *bufferConn, group byte, skill uint16, bound bool, index uint16) {
	t.Helper()
	packets := drainUpperPackets(t, conn)
	if len(packets) != 1 || packets[0].msgID != 1367 {
		t.Fatalf("interactive response reset native selections: %+v", packets)
	}
	body := packets[0].body
	if len(body) != 170 || body[0] != 0xfe || body[1] != group || binary.LittleEndian.Uint16(body[2:]) != skill || body[4] != 33 {
		t.Fatalf("paired header=%x", body)
	}
	for slot := 0; slot < 33; slot++ {
		row := body[5+slot*5 : 10+slot*5]
		if bound && slot == 12 {
			if row[0] != 1 || row[1] != 12 || row[2] != 0 || binary.LittleEndian.Uint16(row[3:]) != index {
				t.Fatalf("bound row=%x", row)
			}
		} else if row[0] != 0 || row[1] != byte(slot) || row[2] != 45 || binary.LittleEndian.Uint16(row[3:]) != 0xffff {
			t.Fatalf("empty row=%x", row)
		}
	}
}

func TestBuffPairedRegistrationPreservesGroupsAndSwitchesSkillRows(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, session, conn, repos := r191CacheFixture(t)
	ctx := context.Background()
	inv, _, err := repos.Inventory.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	inv.Slots["0:55"] = repo.ItemStack{ItemID: 101040493, Count: 1}
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	step := func(group byte, skill uint16, slot, list byte, index uint16, bound bool, wantIndex uint16) {
		t.Helper()
		reviewCurrentBuffDispatch(t, s, session, pairedBuffCommand(group, skill, slot, list, index))
		pairedBuffExpect(t, conn, group, skill, bound, wantIndex)
	}
	step(0, 38, 33, 45, 0xffff, false, 0)
	step(0, 38, 12, 0, 54, true, 54)
	step(1, 39, 33, 45, 0xffff, false, 0)
	step(1, 39, 12, 0, 55, true, 55)
	step(0, 40, 33, 45, 0xffff, false, 0)
	step(0, 38, 33, 45, 0xffff, true, 54)
	step(1, 0, 33, 45, 0xffff, false, 0)
	step(1, 39, 33, 45, 0xffff, true, 55)
	state, _, err := buffswap.Load(ctx, repos, "1", "dnf:10001")
	if err != nil || state.SelectedSkills[0] != 38 || state.SelectedSkills[1] != 39 || state.SkillSlots[38][12].Instance == state.SkillSlots[39][12].Instance {
		t.Fatalf("grouped state=%+v err=%v", state, err)
	}
	fresh, cold, out, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, repos)
	fresh.options.currentClientVersion = "official-24.9"
	if err := fresh.sendBuffSwapReapply(cold, "cold_two_group_restore"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, out)
	if len(packets) != 2 {
		t.Fatalf("cold groups=%+v", packets)
	}
	for i, p := range packets {
		if p.msgID != 1367 || len(p.body) != 170 || p.body[1] != byte(i) || binary.LittleEndian.Uint16(p.body[2:]) != uint16(38+i) || binary.LittleEndian.Uint16(p.body[68:]) != uint16(54+i) {
			t.Fatalf("cold group%d=%x", i, p.body)
		}
	}
	inv, _, err = repos.Inventory.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	inv.Slots["0:56"] = inv.Slots["0:55"]
	delete(inv.Slots, "0:55")
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "group1_item_moved"); err != nil {
		t.Fatal(err)
	}
	packets = drainUpperPackets(t, conn)
	if len(packets) != 2 || binary.LittleEndian.Uint16(packets[0].body[68:]) != 54 || binary.LittleEndian.Uint16(packets[1].body[68:]) != 56 {
		t.Fatalf("moved groups=%+v", packets)
	}
}

func TestBuffPairedFailedRowWriteRemainsRetryable(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, session, conn, _ := r191CacheFixture(t)
	session.conn = r191FailingConn{conn}
	if err := s.handleGameCommand(session, 1, 1426, pairedBuffCommand(1, 39, 12, 0, 54)); err == nil {
		t.Fatal("failed write ignored")
	}
	session.conn = conn
	reviewCurrentBuffDispatch(t, s, session, pairedBuffCommand(1, 39, 12, 0, 54))
	pairedBuffExpect(t, conn, 1, 39, true, 54)
}

func TestBuffPairedInvalidEnvelopeCannotWriteOtherGroup(t *testing.T) {
	for _, enabled := range []string{"on", "off"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", enabled)
			s, session, conn, repos := r191CacheFixture(t)
			for _, body := range [][]byte{pairedBuffCommand(2, 38, 12, 0, 54), {0xb7, 1}, append([]byte{0xb8, 1}, reviewCurrentBuffCommand(38, 12, 0, 54)...)} {
				reviewCurrentBuffDispatch(t, s, session, body)
				if conn.write.Len() != 0 {
					t.Fatal("invalid envelope produced a projection")
				}
				if _, found, err := repos.Settings.Load(context.Background(), buffswap.Scope("1")); err != nil || found {
					t.Fatalf("invalid envelope saved=%t err=%v", found, err)
				}
			}
		})
	}
}
