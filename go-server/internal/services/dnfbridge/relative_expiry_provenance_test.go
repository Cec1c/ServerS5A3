package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRelativeExpiryProvenanceKeepsResolvedPeriod(t *testing.T) {
	issued := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for _, kind := range []dungeonDropItemKind{dungeonDropItemStackable, dungeonDropItemEquipment} {
		t.Run(string(kind), func(t *testing.T) {
			raw := dungeonDropItemDefinition{ItemID: 10304122, Kind: kind, UsablePeriodDays: 7}
			resolved, err := currentPVFItemDefinitionForGrantAt(raw, issued)
			if err != nil {
				t.Fatal(err)
			}
			want := issued.Add(7 * 24 * time.Hour)
			if !raw.ExpirationDate.IsZero() || !resolved.ExpirationDate.Equal(want) {
				t.Fatal("grant changed the static definition or lost its clock")
			}
			stack, _ := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{ItemID: int64(raw.ItemID), Count: 1}, resolved, want.Add(24*time.Hour))
			if !stack.ExpireAt.Equal(want) || stack.Extra["expire_time"] != strconv.FormatInt(want.Unix(), 10) || stack.Extra["expire_unix"] != strconv.FormatInt(want.Unix(), 10) {
				t.Fatalf("resolved relative deadline was renewed: got=%s want=%s", stack.ExpireAt, want)
			}
			again, _ := applyCurrentPVFItemExpirationAt(stack, resolved, want.Add(48*time.Hour))
			if !again.ExpireAt.Equal(want) {
				t.Fatal("repeat projection moved the resolved deadline")
			}
			// The operator's static PVF retirement policy still applies to the
			// identical date when it came from an absolute declaration.
			declared := dungeonDropItemDefinition{ItemID: raw.ItemID, Kind: kind, ExpirationDate: want}
			static, _ := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{ItemID: int64(raw.ItemID), Count: 1}, declared, want.Add(24*time.Hour))
			if !static.ExpireAt.Equal(currentPVFRetiredExpirationRenewalDate) {
				t.Fatal("static retired-date policy changed")
			}
		})
	}
}

func TestRelativeExpiryProvenanceHistoricalAdventurePurchase(t *testing.T) {
	service, session, repos, clock, category, product := newAdventureShopExpirationFixture(t, adventureShopExpirationSource("[usable period]\n7\n"))
	issued := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	clock.now = issued
	buyAdventureExpirationPotion(t, service, session, category.Index, product.ItemID, true)
	inv, found, err := repos.Inventory.Load(context.Background(), "31")
	if err != nil || !found || len(inv.Slots) != 1 {
		t.Fatalf("inventory found=%t err=%v slots=%d", found, err, len(inv.Slots))
	}
	want := issued.Add(7 * 24 * time.Hour)
	for _, stack := range inv.Slots {
		if !stack.ExpireAt.Equal(want) || len(stack.RawEntry) != currentItemListEntryWireSize || binary.LittleEndian.Uint32(stack.RawEntry[0x38:0x3c]) != uint32(want.Unix()) {
			t.Fatalf("formal purchase changed its issue-clock deadline: got=%s want=%s", stack.ExpireAt, want)
		}
	}
	clock.now = issued.Add(48 * time.Hour)
	if summary, err := service.reconcileCurrentPVFItemExpirations(context.Background(), repos, "31", "shop-owner"); err != nil || summary.Total() != 0 {
		t.Fatalf("relogin changed deadline: %+v err=%v", summary, err)
	}
	after, _, err := repos.Inventory.Load(context.Background(), "31")
	if err != nil {
		t.Fatal(err)
	}
	for key, before := range inv.Slots {
		if !after.Slots[key].ExpireAt.Equal(before.ExpireAt) {
			t.Fatal("relogin moved the issue-clock deadline")
		}
	}
}

func TestRelativeExpiryProvenanceRealPotion(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.ResolveItem(10304122)
	if err != nil {
		t.Fatal(err)
	}
	if definition.UsablePeriodDays != 7 || !definition.ExpirationDate.IsZero() || definition.StackLimit != 1 {
		t.Fatalf("unexpected active potion definition: %+v", definition)
	}
	issued := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	resolved, err := currentPVFItemDefinitionForGrantAt(definition, issued)
	if err != nil {
		t.Fatal(err)
	}
	want := issued.Add(7 * 24 * time.Hour)
	stack, _ := applyCurrentPVFItemExpirationAt(dnfrepo.ItemStack{ItemID: 10304122, Count: 1}, resolved, want.Add(24*time.Hour))
	if !stack.ExpireAt.Equal(want) {
		t.Fatalf("active potion relative deadline changed to %s, want %s", stack.ExpireAt, want)
	}
}
