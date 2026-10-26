package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func r195ExpectBuffRows(t *testing.T, conn *bufferConn, present bool, index uint16) {
	t.Helper()
	packets := drainUpperPackets(t, conn)
	if len(packets) != 1 || packets[0].msgID != 1367 {
		t.Fatalf("wear must send one row-only update, got %+v", packets)
	}
	want := make([]byte, 166)
	want[0] = 33
	for slot := 0; slot < 33; slot++ {
		i := 1 + slot*5
		copy(want[i:i+5], []byte{0, byte(slot), 45, 255, 255})
	}
	if present {
		copy(want[61:66], []byte{1, 12, 0, byte(index), byte(index >> 8)})
	}
	if !bytes.Equal(packets[0].body, want) {
		t.Fatalf("row-only body=%x want=%x", packets[0].body, want)
	}
}

func TestR195BuffWearRefreshesRowsWithoutResettingSelectedSkills(t *testing.T) {
	s, session, conn, repos := r191CacheFixture(t)
	if err := s.handleGameCommand(session, 1, 1426, reviewCurrentBuffCommand(38, 12, 0, 54)); err != nil {
		t.Fatal(err)
	}
	if packets := drainUpperPackets(t, conn); len(packets) != 1 || packets[0].msgID != 1368 {
		t.Fatal("explicit registration must initialize full skill state")
	}
	// The actor may hold new objects even when all coordinates are unchanged.
	for i := 0; i < 2; i++ {
		if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "actor_rebuilt"); err != nil {
			t.Fatal(err)
		}
		r195ExpectBuffRows(t, conn, true, 54)
	}
	ctx := context.Background()
	inv, _, err := repos.Inventory.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	inv.Slots["0:55"] = inv.Slots["0:54"]
	delete(inv.Slots, "0:54")
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	session.conn = r191FailingConn{conn}
	if err := s.sendBuffSwapReapplyAfterNonAppearanceWear(session, "moved_registered_instance"); err == nil {
		t.Fatal("row write failure not propagated")
	}
	session.conn = conn
	if err := s.sendBuffSwapReapplyAfterNonAppearanceWear(session, "retry_projection"); err != nil {
		t.Fatal(err)
	}
	r195ExpectBuffRows(t, conn, true, 55)
	if err := s.sendBuffSwapReapplyAfterNonAppearanceWear(session, "unchanged_nonappearance"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatal("unchanged nonappearance update was not deduplicated")
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "deleted_instance"); err != nil {
		t.Fatal(err)
	}
	r195ExpectBuffRows(t, conn, false, 0)
	if err := buffswap.ApplyCurrent(ctx, repos, "1", "dnf:10001", buffswap.Command{Skill: 39, Slot: 33, List: 44, Index: 0xffff}); err != nil {
		t.Fatal(err)
	}
	if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "skill_changed_outside_projection"); err != nil {
		t.Fatal(err)
	}
	if packets := drainUpperPackets(t, conn); len(packets) != 1 || packets[0].msgID != 1368 || packets[0].body[0] != 39 {
		t.Fatal("changed skill requires full state")
	}
}
