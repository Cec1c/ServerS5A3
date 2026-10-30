package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func currentMainListBodySlots(t *testing.T, body []byte) map[int16]int64 {
	t.Helper()
	if len(body) < 5 || body[0] != dnfrepo.MainInventoryListType {
		t.Fatalf("main-list body header=%x", body)
	}
	count := int(binary.LittleEndian.Uint16(body[3:5]))
	if len(body) != 5+count*currentItemListEntryWireSize {
		t.Fatalf("main-list body length=%d count=%d", len(body), count)
	}
	rows := make(map[int16]int64, count)
	for offset := 5; offset < len(body); offset += currentItemListEntryWireSize {
		row := body[offset : offset+currentItemListEntryWireSize]
		rows[int16(binary.LittleEndian.Uint16(row[0:2]))] = int64(binary.LittleEndian.Uint32(row[2:6]))
	}
	return rows
}

func TestMainInventoryPreflightReusesOneSnapshotPerRequest(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)

	// Reusing the preflight removes one steady-state non-transactional load:
	// this path now uses one preflight load and the committed expiration snapshot.
	for build := 1; build <= 2; build++ {
		fixture.counter.reset()
		if _, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(
			context.Background(), fixture.session, dnfrepo.MainInventoryListType,
		); !ok {
			t.Fatalf("build %d failed", build)
		}
		if got := fixture.counter.total("Inventory.Load"); got != 1 {
			t.Fatalf("build %d Inventory.Load=%d, want 1; calls=%s", build, got, fixture.counter.summary())
		}
		if got := fixture.counter.total("AccountAssets.inventory.Load"); got != 1 {
			t.Fatalf("build %d AccountAssets.inventory.Load=%d, want 1", build, got)
		}
		if got := fixture.counter.total("AccountAssets.Begin"); got != 1 {
			t.Fatalf("build %d AccountAssets.Begin=%d, want 1", build, got)
		}
		if got := fixture.counter.total("RentalAssets.Begin"); got != 0 {
			t.Fatalf("build %d opened RentalAssets without a contract: %d", build, got)
		}
	}
}

func TestMainInventoryPreflightDoesNotHideContractAddedBeforeNextBuild(t *testing.T) {
	const contractItemID int64 = 490701999
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	ctx := context.Background()

	firstBody, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(ctx, fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("first build failed")
	}
	if _, found := currentMainListBodySlots(t, firstBody)[45]; found {
		t.Fatal("first body unexpectedly contains the contract slot")
	}
	inventory, found, err := fixture.group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	inventory.Slots["0:45"] = dnfrepo.ItemStack{ItemID: contractItemID, Count: 1}
	if err := fixture.group.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}

	fixture.counter.reset()
	secondBody, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(ctx, fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("second build failed")
	}
	if got := fixture.counter.total("Inventory.Load"); got != 1 {
		t.Fatalf("second build Inventory.Load=%d, want 1 (no cross-request snapshot); calls=%s", got, fixture.counter.summary())
	}
	if got := fixture.counter.total("RentalAssets.Begin"); got != 1 {
		t.Fatalf("RentalAssets.Begin=%d, want 1 for the newly added contract", got)
	}
	for slot, itemID := range currentMainListBodySlots(t, secondBody) {
		if itemID == contractItemID {
			t.Fatalf("contract item %d remained in final body at slot %d", itemID, slot)
		}
	}
	account, found, err := fixture.group.Account.Load(ctx, "dnf:1")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if expire := premium.ExpireAt(account, 22); expire <= time.Now().Add(24*time.Hour).Unix() {
		t.Fatalf("contract entitlement was not extended: expire=%d", expire)
	}
}

func TestMainInventoryPreflightDropsSnapshotAfterGoldSlotRepair(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	ctx := context.Background()
	inventory, found, err := fixture.group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	inventory.Slots["0:0"] = dnfrepo.ItemStack{ItemID: 3227, Count: 1, Extra: map[string]string{"marker": "retain"}}
	if err := fixture.group.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}

	fixture.counter.reset()
	body, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(ctx, fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("main-inventory build failed")
	}
	if got := fixture.counter.total("Inventory.Load"); got != 2 {
		t.Fatalf("Inventory.Load=%d, want 2: repair invalidates preflight; final projection uses the committed expiry snapshot; calls=%s", got, fixture.counter.summary())
	}
	if got := fixture.counter.total("CharacterItems.Begin"); got != 1 {
		t.Fatalf("CharacterItems.Begin=%d, want 1 durable repair", got)
	}
	rows := currentMainListBodySlots(t, body)
	if itemID := rows[0]; itemID != 0 {
		t.Fatalf("final slot 0 item=%d, want the projected gold wallet row", itemID)
	}
	if itemID := rows[121]; itemID != 3227 {
		t.Fatalf("repaired material slot 121 item=%d, want 3227", itemID)
	}
	stored, found, err := fixture.group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload inventory found=%t err=%v", found, err)
	}
	if _, exists := stored.Slots["0:0"]; exists {
		t.Fatal("durable inventory still contains the non-wallet item in slot 0")
	}
	if got := stored.Slots["0:121"]; got.ItemID != 3227 || got.Extra["marker"] != "retain" {
		t.Fatalf("repaired stack=%+v", got)
	}
}
