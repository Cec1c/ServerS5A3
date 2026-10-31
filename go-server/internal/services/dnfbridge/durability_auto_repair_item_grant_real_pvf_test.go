package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFAutoRepairBareItemWithoutUniqueDurationIsPreserved(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the current auto-repair item semantics")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real PVF: %v", err)
	}
	stackable, err := archive.ReadText("stackable/cash/chn_contract_devil/chn_auto_repair.stk")
	if err != nil {
		t.Fatalf("read real auto-repair stackable: %v", err)
	}
	for _, durationField := range []string{"[term]", "[expiration]", "[int data]", "[usable]"} {
		if strings.Contains(stackable, durationField) {
			t.Fatalf("real auto-repair item unexpectedly contains duration/use field %s: %s", durationField, stackable)
		}
	}
	if !strings.Contains(stackable, "[explain]") || !strings.Contains(stackable, "`[etc]`") {
		t.Fatalf("real auto-repair item is no longer an explanatory [etc] contract carrier: %s", stackable)
	}
	catalog, err := buildCurrentPremiumCatalog(archive)
	if err != nil {
		t.Fatalf("build real premium catalog: %v", err)
	}
	autoRepair, found := catalog.devilSlots[100817]
	if !found || autoRepair.Slot != premium.DevilSlotAutoRepair || autoRepair.ItemID <= 0 || autoRepair.Days <= 0 {
		t.Fatalf("real auto-repair product = %+v found=%t", autoRepair, found)
	}
	if contract, ok := currentPremiumInventoryContracts(catalog)[autoRepair.ItemID]; ok {
		t.Fatalf("shared 7/30-day auto-repair item acquired an implicit duration: %+v", contract)
	}

	service, session, repositories := newDurabilityBridgeFixture(t, 1)
	service.premiumCatalog = catalog
	service.premiumCatalogLoadErr = nil
	ctx := context.Background()
	inventory, found, err := repositories.Inventory.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	const grantSlot = int16(10)
	itemExpiry := time.Now().UTC().Add(48 * time.Hour)
	inventory.Slots["0:10"] = dnfrepo.ItemStack{
		ItemID:   autoRepair.ItemID,
		Count:    1,
		ExpireAt: itemExpiry,
		Extra:    map[string]string{"expire_time": strconv.FormatInt(itemExpiry.Unix(), 10)},
	}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}

	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, grantSlot, inventory.Slots["0:10"])
	if _, err := service.prepareCurrentPremiumContractItemUpdate(
		session,
		buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, []currentItemListEntry{entry}),
	); err != nil {
		t.Fatalf("apply real auto-repair grant projection: %v", err)
	}

	now := time.Now().UTC()
	premiumType := premium.DevilSlotType(premium.DevilSlotAutoRepair)
	account, found, err := repositories.Account.Load(ctx, "account-1")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if expire := premium.ExpireAt(account, premiumType); expire != 0 {
		t.Fatalf("ambiguous bare item activated %q with expire=%d", premium.MetadataKey(premiumType), expire)
	}
	inventory, found, err = repositories.Inventory.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("reload inventory found=%t err=%v", found, err)
	}
	if retained, exists := inventory.Slots["0:10"]; !exists || retained.ItemID != autoRepair.ItemID || retained.Count != 1 ||
		!retained.ExpireAt.Equal(itemExpiry) || retained.Extra["expire_time"] != strconv.FormatInt(itemExpiry.Unix(), 10) {
		t.Fatalf("ambiguous bare item was swallowed or changed: %+v", inventory.Slots)
	}

	owner, err := dnfequip.NewDurabilityOwner(repositories)
	if err != nil {
		t.Fatalf("new durability owner: %v", err)
	}
	resolve := func(int64) (alignedcmd.RepairCostEvidence, error) {
		return alignedcmd.RepairCostEvidence{
			EquipmentType:   "[weapon]",
			MaxDurability:   48,
			RepairPrice:     4800,
			Grade:           20,
			RepairCostRate:  0.08,
			QuickRepairRate: 1.5,
			UpgradeRates:    []float64{1, 1, 1},
		}, nil
	}
	d2SetGrantedWeaponDurability(t, ctx, repositories, 1)
	result, err := owner.Decrease(ctx, dnfequip.DecreaseDurabilityCommand{
		AccountID:   "account-1",
		CharacterID: 99,
		Slot:        12,
		Now:         now,
	}, resolve)
	if err != nil {
		t.Fatalf("decrease without a duration-bearing grant: %v", err)
	}
	if result.Previous != 1 || result.Current != 0 || result.AutoRepaired || !result.Changed {
		t.Fatalf("ambiguous item caused auto repair: %+v", result)
	}
	character, found, err := repositories.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if used := premium.DailyUsage(character, premium.DevilSlotAutoRepair, now); used != 0 {
		t.Fatalf("ambiguous item consumed daily quota: %d", used)
	}
}

func d2SetGrantedWeaponDurability(t *testing.T, ctx context.Context, repositories dnfrepo.Group, durability uint16) {
	t.Helper()
	record, found, err := repositories.Equipment.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load equipment found=%t err=%v", found, err)
	}
	entry := record.Entries["12"]
	binary.LittleEndian.PutUint16(entry.RawEntry[0x0B:0x0D], durability)
	if entry.Extra == nil {
		entry.Extra = make(map[string]string)
	}
	entry.Extra["durability"] = fmt.Sprint(durability)
	record.Entries["12"] = entry
	if err := repositories.Equipment.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
}
