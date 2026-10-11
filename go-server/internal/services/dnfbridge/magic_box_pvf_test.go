package dnfbridge

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func newMagicBoxTestCatalog(t *testing.T) (*pvfDungeonDropCatalog, dungeonDropCatalogTestSource) {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":                     "",
		"equipment/equipment.lst":                 "",
		"stackable/stackable.lst":                 "24 `cash/creature/creature_food.stk`\n10007368 `ect/chn_random/amazingbox.stk`\n10007367 `ect/chn_random/hammer.stk`\n2001 `booster/boosterbox.stk`\n2002 `other/plain.stk`\n2600014 `potion/ptn.stk`\n2003 `booster/avatarstride.stk`\n2004 `booster/decimals.stk`\n2005 `booster/narrow.stk`\n2006 `booster/gated.stk`\n",
		"stackable/ect/chn_random/amazingbox.stk": "[name] `Amazing Box`\n[stackable type] `[random upgradable legacy]`\n[RANDOMBOX]\n[int data]\n2600014 2 0 2600014 18000 2 0 2600014 5000 2 0 2600014 500 2 0 2600014 2 2 0\n[/int data]\n[int data]\n2682272 1 0 2682272 50000 1 0\n[/int data]\n[sealing removal item]\n2 10007367 1 10007368 1\n[/sealing removal item]\n[/RANDOMBOX]\n",
		"stackable/ect/chn_random/hammer.stk":     "[name] `Hammer`\n[stackable type] `[material]`\n",
		"stackable/booster/boosterbox.stk":        "[name] `Booster Box`\n[stackable type] `[booster]`\n[booster info]\n[etc]\n2 3001 100 1 3002 50 2\n[/etc]\n[/booster info]\n[need material] `10007367 1`\n",
		// Real 90 PVF shape of 阐释者礼盒 (10007317): one [avatar] row of five
		// integers after the group DrawCount.
		"stackable/booster/avatarstride.stk": "[name] `Avatar Stride Box`\n[stackable type] `[booster]`\n[booster info]\n[avatar]\n1 2600014 100000 1 3 0\n[/avatar]\n[/booster info]\n",
		// 10162015 writes one decimal weight ("8.20") per decimal row.
		"stackable/booster/decimals.stk": "[name] `Decimal Box`\n[stackable type] `[booster]`\n[booster info]\n[etc]\n1 2600014 8333 100 2600014 8.20 1\n[/etc]\n[/booster info]\n",
		// Nine integers: neither a DrawCount + triples nor a five-wide avatar row.
		"stackable/booster/narrow.stk":              "[name] `Narrow Box`\n[stackable type] `[booster]`\n[booster info]\n[etc]\n1 2600014 100 1 2600014 50 2 2600014\n[/etc]\n[/booster info]\n",
		"stackable/booster/gated.stk":               "[name] `Gated Box`\n[stackable type] `[booster]`\n[booster info]\n[charactor]\n`[swordman]` 1 85 1 2600014 300000 1\n[/charactor]\n[/booster info]\n",
		"stackable/other/plain.stk":                 "[name] `Plain`\n[stackable type] `[material]`\n",
		"stackable/potion/ptn.stk":                  "[name] `Potion`\n[stackable type] `[consumption]`\n[stack limit] 100\n[usable period] 30\n[attach type] `[sealing]`\n",
		"stackable/cash/creature/creature_food.stk": "[name] `Creature Food`\n[stackable type] `[feed]`\n[stack limit] 1000\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, source
}

func TestResolveCurrentMagicBoxPetFeedTargetsPetConsumableSegment(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	item, err := resolveCurrentMagicBoxRewardItem(catalog, source, 24)
	if err != nil {
		t.Fatal(err)
	}
	if item.ItemID != 24 || item.TargetListType != currentPetInventoryListType ||
		item.SlotStart != currentCeraShopPetConsumableSlotStart ||
		item.SlotEnd != currentCeraShopPetConsumableSlotEnd ||
		item.StackLimit != 1000 {
		t.Fatalf("pet feed reward=%+v", item)
	}
}

func TestResolveCurrentMagicBoxRandomLegacy(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	resolution, err := resolveCurrentMagicBox(catalog, source, 10007368)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if resolution.Kind != "random" {
		t.Fatalf("kind = %q, want random", resolution.Kind)
	}
	if len(resolution.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(resolution.Groups))
	}
	first := resolution.Groups[0]
	if first.DrawCount != 1 || len(first.Entries) != 4 {
		t.Fatalf("group[0] = %+v, want 4 entries DrawCount 1", first)
	}
	if first.Entries[0].ItemID != 2600014 || first.Entries[0].Weight != 18000 || first.Entries[0].Count != 2 {
		t.Fatalf("group[0] entry[0] = %+v", first.Entries[0])
	}
	if first.Entries[3].Weight != 2 {
		t.Fatalf("group[0] entry[3] = %+v, want weight 2 preserved", first.Entries[3])
	}
	second := resolution.Groups[1]
	// Seven ints: exactly one quad entry after the skipped leading triple.
	if len(second.Entries) != 1 || second.Entries[0].ItemID != 2682272 || second.Entries[0].Weight != 50000 || second.Entries[0].Count != 1 {
		t.Fatalf("group[1] = %+v", second)
	}
	if resolution.MaterialItemID != 10007367 || resolution.MaterialCountPerUse != 1 {
		t.Fatalf("material = %d x%d, want 10007367 x1", resolution.MaterialItemID, resolution.MaterialCountPerUse)
	}
	if resolution.BoxPVFPath != "stackable/ect/chn_random/amazingbox.stk" {
		t.Fatalf("path = %q", resolution.BoxPVFPath)
	}
}

func TestResolveCurrentMagicBoxBoosterFamily(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	resolution, err := resolveCurrentMagicBox(catalog, source, 2001)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if resolution.Kind != "random" || len(resolution.Groups) != 1 {
		t.Fatalf("resolution = %+v", resolution)
	}
	group := resolution.Groups[0]
	if group.DrawCount != 2 {
		t.Fatalf("draw count = %d, want leading DrawCount 2", group.DrawCount)
	}
	if len(group.Entries) != 2 || group.Entries[0].ItemID != 3001 || group.Entries[0].Count != 1 || group.Entries[1].ItemID != 3002 || group.Entries[1].Count != 2 {
		t.Fatalf("entries = %+v", group.Entries)
	}
	if resolution.MaterialItemID != 0 || resolution.MaterialCountPerUse != 0 {
		t.Fatalf("NPC exchange price charged again while opening: %d x%d", resolution.MaterialItemID, resolution.MaterialCountPerUse)
	}
}

func TestResolveCurrentMagicBoxUnsupportedAndUnknown(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	resolution, err := resolveCurrentMagicBox(catalog, source, 2002)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if resolution.Kind != "" {
		t.Fatalf("plain stackable kind = %q, want empty", resolution.Kind)
	}
	resolution, err = resolveCurrentMagicBox(catalog, source, 9999)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if resolution.Kind != "" {
		t.Fatalf("unknown item kind = %q, want empty", resolution.Kind)
	}
}

// TestResolveCurrentMagicBoxBoosterAvatarRowWidth pins the reported defect:
// 阐释者礼盒 (10007317) stores one [avatar] row of five integers after the group
// DrawCount ("1 101600024 100000 1 3 0"). Reading it as three-wide triples
// produced item 1 (复活币) x100000; the row width is part of the family
// contract in 86JP PvfLib AddWeightedRewards.
func TestResolveCurrentMagicBoxBoosterAvatarRowWidth(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	resolution, err := resolveCurrentMagicBox(catalog, source, 2003)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if resolution.Kind != "random" || len(resolution.Groups) != 1 {
		t.Fatalf("resolution = %+v", resolution)
	}
	group := resolution.Groups[0]
	want := []alignedcmd.MagicBoxRewardEntry{{ItemID: 2600014, Weight: 100000, Count: 1}}
	if group.DrawCount != 1 || !reflect.DeepEqual(group.Entries, want) {
		t.Fatalf("group = %+v, want DrawCount 1 entries %+v", group, want)
	}
	for _, entry := range group.Entries {
		if entry.ItemID == 1 {
			t.Fatalf("avatar row decoded into revive coin: %+v", group.Entries)
		}
	}
}

// TestResolveCurrentMagicBoxBoosterDecimalWeightScale covers the 90 PVF's
// decimal weights (10162015 writes "8.20"): 86JP PvfLib scales them by the
// widest fraction in the block, so 8.20 becomes 820 next to integer weights
// measured in 1/10000 units.
func TestResolveCurrentMagicBoxBoosterDecimalWeightScale(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	resolution, err := resolveCurrentMagicBox(catalog, source, 2004)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if resolution.Kind != "random" || len(resolution.Groups) != 1 {
		t.Fatalf("resolution = %+v", resolution)
	}
	want := []alignedcmd.MagicBoxRewardEntry{
		{ItemID: 2600014, Weight: 8333, Count: 100},
		{ItemID: 2600014, Weight: 820, Count: 1},
	}
	if group := resolution.Groups[0]; group.DrawCount != 1 || !reflect.DeepEqual(group.Entries, want) {
		t.Fatalf("group = %+v, want %+v", group, want)
	}
}

// TestResolveCurrentMagicBoxBoosterInfoRejectsUnprovableTables covers the
// fail-closed contract: a table whose row width this server cannot prove, and
// the job/level-gated [charactor] family, must reject the open instead of
// fabricating a reward.
func TestResolveCurrentMagicBoxBoosterInfoRejectsUnprovableTables(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	if _, err := resolveCurrentMagicBox(catalog, source, 2005); !errors.Is(err, errMagicBoxBoosterInfoShape) {
		t.Fatalf("stray-int [etc] error = %v, want %v", err, errMagicBoxBoosterInfoShape)
	}
	if _, err := resolveCurrentMagicBox(catalog, source, 2006); !errors.Is(err, errMagicBoxBoosterInfoGated) {
		t.Fatalf("[charactor] error = %v, want %v", err, errMagicBoxBoosterInfoGated)
	}
}

// TestMagicBoxBoosterInfoEmptyGroupIsNotAReward pins the "1" placeholder
// sections the 90 PVF writes for reward tables that grant nothing.
func TestMagicBoxBoosterInfoEmptyGroupIsNotAReward(t *testing.T) {
	document, err := dnfpvf.Parse("stackable/booster/empty.stk", "[booster info]\n[etc]\n1\n[/etc]\n[equipment]\n1\n[/equipment]\n[/booster info]\n")
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	groups, err := parseMagicBoxBoosterGroups(document)
	if err != nil {
		t.Fatalf("parse groups error = %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("groups = %+v, want none", groups)
	}
}

func TestResolveCurrentMagicBoxRewardItemMetadata(t *testing.T) {
	catalog, source := newMagicBoxTestCatalog(t)
	before := time.Now().UTC()
	item, err := resolveCurrentMagicBoxRewardItem(catalog, source, 2600014)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if item.ItemID != 2600014 || item.Kind != "stackable" || item.StackLimit != 100 || !item.Seal || item.UsablePeriodDays != 30 {
		t.Fatalf("reward item = %+v", item)
	}
	minExpire := time.Unix(before.Unix()+30*86400, 0).UTC()
	maxExpire := time.Unix(time.Now().UTC().Unix()+30*86400, 0).UTC()
	if item.ExpireAt.Before(minExpire) || item.ExpireAt.After(maxExpire) {
		t.Fatalf("reward expiration=%s outside %s..%s", item.ExpireAt, minExpire, maxExpire)
	}
	if item.SlotStart != 65 || item.SlotEnd != 120 {
		t.Fatalf("slot range = %d-%d, want 65-120", item.SlotStart, item.SlotEnd)
	}
	if item.PVFPath != "stackable/potion/ptn.stk" {
		t.Fatalf("path = %q", item.PVFPath)
	}
}

// 魔盒奖励装备的耐久与修理定价 / dungeon_drop_catalog 同口径（三键回退）：
// 只写 [maximum durability] / [max durability] 的奖励装备过去在这里读到 0，
// 发出去的实例既没有耐久也不可修（packageitem/owner.go:784、:865 把同一个值
// 同时写进 Extra 的 durability 与 max_durability）。
func TestResolveCurrentMagicBoxRewardItemReadsAlternateDurabilityKeys(t *testing.T) {
	cases := []struct {
		name     string
		sections string
		want     uint16
	}{
		{name: "durability only", sections: "[durability] 57\n", want: 57},
		{name: "maximum durability only", sections: "[maximum durability] 57\n", want: 57},
		{name: "max durability only", sections: "[max durability] 58\n", want: 58},
		{name: "durability wins over maximum durability", sections: "[durability] 57\n[maximum durability] 99\n", want: 57},
		{name: "zero durability falls back to maximum durability", sections: "[durability] 0\n[maximum durability] 57\n", want: 57},
		{name: "no durability key stays zero", sections: "", want: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := dungeonDropCatalogTestSource{
				"monster/monster.lst":           "",
				"stackable/stackable.lst":       "",
				"equipment/equipment.lst":       "9001 `weapon/max_only.equ`\n",
				"equipment/weapon/max_only.equ": "[name] `Max Only`\n[equipment type] `[weapon]`\n[attach type] `[free]`\n" + testCase.sections,
			}
			catalog, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			item, err := resolveCurrentMagicBoxRewardItem(catalog, source, 9001)
			if err != nil {
				t.Fatalf("resolve reward item: %v", err)
			}
			if item.Durability != testCase.want {
				t.Fatalf("reward Durability = %d, want %d (item %+v)", item.Durability, testCase.want, item)
			}
		})
	}
}
