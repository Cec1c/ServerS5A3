package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDimensionExtraPolicyDoesNotTurnMaximaIntoFreeEntries(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		base, want int64
		valid      bool
	}{
		{"actual", "[max daily enter count]\n10\n[max extra enter count]\n5", 5, 5, true},
		{"no_bonus", "[max daily enter count]\n5\n[max extra enter count]\n0", 5, 0, true},
		{"missing", "[max daily enter count]\n10", 5, 0, false},
		{"negative", "[max daily enter count]\n10\n[max extra enter count]\n-1", 5, 0, false},
		{"inconsistent", "[max daily enter count]\n9\n[max extra enter count]\n5", 5, 0, false},
		{"duplicate", "[max daily enter count]\n10\n[max extra enter count]\n5\n[max extra enter count]\n4", 5, 0, false},
		{"multiple", "[max daily enter count]\n10\n[max extra enter count]\n5 4", 5, 0, false},
		{"overflow", "[max daily enter count]\n9223372036854775807\n[max extra enter count]\n9223372036854775807", 5, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCurrentDimensionGateExtraLimit(tc.text, tc.base)
			if (err == nil) != tc.valid || got != tc.want {
				t.Fatal(got, err)
			}
		})
	}
}

// This exercises the actual prepare/commit entry chain after a domain ticket
// transaction. It is not evidence that the native ticket request is wired.
func TestRealPVFNewDimensionFamilyUsesEarnedExtrasAcrossDungeons(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	s, g, queue := newDungeonDailyCountTestService(t, path)
	base, _, err := s.currentDimensionGateDailyEntryLimit()
	if err != nil {
		t.Fatal(err)
	}
	extra, err := s.currentDimensionGateDailyExtraLimit(base)
	if err != nil || base != 5 || extra != 5 {
		t.Fatal(base, extra, err)
	}
	session := seedDungeonDailyCountCharacter(t, g, "19", 19, "dimension-account")
	bindDungeonSelectorOriginForTestAt(t, s, session, 107, 0, 250, 224)
	ctx := context.Background()
	if err := g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 10158692, Count: 1}, "0:21": {ItemID: 3332, Count: 100}}}); err != nil {
		t.Fatal(err)
	}
	ids := []uint32{7165, 7168, 7171, 7174, 7177}
	enter := func(id uint32) {
		t.Helper()
		r, err := enterDungeonOnce(t, s, session, id)
		if err != nil {
			t.Fatal(id, err)
		}
		if err := r.Session.Abandon(); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		enter(id)
	}
	if _, err := enterDungeonOnce(t, s, session, 7166); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatal("sixth free entry", err)
	}
	owner, err := dnfdungeon.NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	r, err := owner.GrantDimensionGateExtraEntry(ctx, dnfdungeon.DimensionGateExtraEntryCommand{CharacterID: "19", AccountID: "dimension-account", Slot: 20, ItemID: 10158692, Count: 5, BaseLimit: base, ExtraLimit: extra, MinimumLevel: 70, UpdatedAt: queue.now})
	if err != nil || r.Total != 10 || !r.Slot.Removed {
		t.Fatal(r, err)
	}
	// A new session still receives the persisted grant, including other difficulty variants.
	session = &gameSession{selectedCharacterID: 19, accountID: "dimension-account", conn: &bufferConn{}, connID: "dimension-reconnect"}
	bindDungeonSelectorOriginForTestAt(t, s, session, 107, 0, 250, 224)
	for _, id := range []uint32{7166, 7169, 7172, 7175, 7178} {
		enter(id)
	}
	if _, err := enterDungeonOnce(t, s, session, 7179); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatal("eleventh entry", err)
	}
	stored, _, err := g.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	used, granted, total, err := dnfdungeon.DimensionGateDailyAllowance(stored, queue.now, base, extra)
	if err != nil || used != 10 || granted != 5 || total != 10 {
		t.Fatal(used, granted, total, err)
	}
	seedDungeonDailyCountCharacter(t, g, "20", 20, "dimension-account")
	other, _, err := g.Character.Load(ctx, "20")
	if err != nil {
		t.Fatal(err)
	}
	used, granted, total, err = dnfdungeon.DimensionGateDailyAllowance(other, queue.now, base, extra)
	if err != nil || used != 0 || granted != 0 || total != 5 {
		t.Fatal("grant leaked across characters", used, granted, total, err)
	}
}
