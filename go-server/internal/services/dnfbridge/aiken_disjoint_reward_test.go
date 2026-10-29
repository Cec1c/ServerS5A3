package dnfbridge

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const aikenDisjointValidCustomInfo = "[custom ability type]\n`warriors dungeon`\n" +
	"[group]\n[key] `1`\n[indexes] 1 2\n[/indexes]\n[/group]\n" +
	"[grade probability] `A` 200 `B` 800 `C` 3000 `D` 6000\n[/grade probability]\n" +
	"[disjoint disable item]\n10099775\n[/disjoint disable item]\n"

func aikenDisjointTestCatalog(t *testing.T, customInfo string) *pvfDungeonDropCatalog {
	t.Helper()
	source := bridgePVFSource{
		"monster/monster.lst":          "",
		"stackable/stackable.lst":      "3037 `cube.stk`\n3166 `additional.stk`\n10099775 `legendary_soul.stk`\n",
		"equipment/equipment.lst":      "5000 `aiken_coat.equ`\n5001 `ordinary_legend_coat.equ`\n",
		"stackable/cube.stk":           "[stackable type] `[material]`\n[stack limit] 9999\n",
		"stackable/additional.stk":     "[stackable type] `[material]`\n[stack limit] 9999\n",
		"stackable/legendary_soul.stk": "[stackable type] `[material]`\n[stack limit] 9999\n",
		"equipment/aiken_coat.equ": "[equipment type] `[coat]`\n[rarity] 6\n[minimum level] 90\n[value] 1000\n" +
			"[custom ability type]\n`warriors dungeon`\n[custom ability count] 2\n",
		"equipment/ordinary_legend_coat.equ": "[equipment type] `[coat]`\n[rarity] 6\n[minimum level] 90\n[value] 1000\n",
		"etc/disjoint.etc": "[cube index]\n`[no element]` 3037\n[/cube index]\n" +
			"[cube creation const]\n150 1.12 1.35 0.07 1.48 1.80 0.70 1.53\n[/cube creation const]\n" +
			"[additional result]\n0 0 0 0 0 0 1 3166\n[/additional result]\n" +
			"[additional result const]\n0.00 0.30 8.70 8 5 50 50 8 10 0.19 5.65 8 20 0.00 1.30 5.50 6 15 0.19 5.65 8 20\n[/additional result const]\n" +
			"[additional result expand]\n1 10100115 1 10100116 1 10099773 1 10099774 1 10158124 0 1 10099775\n[/additional result expand]\n" +
			"[additional result expand const]\n25 0 100 25 0 100 20 0 100 20 0 100 25 0 100 0 16 0 100\n[/additional result expand const]\n" +
			"[avatar disjoint info]\n[fighter]\n2559000 2559003 2559006 2559009 2559006 2559001 2559004 2559007 2559010 2559007 2559002 2559005 2559008 2559011 2559014\n[/fighter]\n[/avatar disjoint info]\n",
		"etc/emblemcompound.etc":                  "[emblem compound info]\n1 2 2550001\n[/emblem compound info]\n",
		"etc/avatardisjoint/emblemlistinfo_0.etc": "[result info]\n1 2550001 100 1 0\n[/result info]\n",
	}
	if customInfo != "" {
		source[currentDisjointIndividualCustomInfoPath] = customInfo
		source["etc/customability/individualcustomability.lst"] = "1 `Options/group1_physical_attack.etc` 2 `Options/group1_magical_attack.etc`\n"
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func aikenDisjointTestRepositories(t *testing.T, slots map[string]dnfrepo.ItemStack) dnfrepo.Group {
	t.Helper()
	repositories := mustCurrentNPCShopRepositories(t, 0, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	return repositories
}

func aikenDisjointHasReward(rewards []currentDisjointReward, itemID uint32) bool {
	for _, reward := range rewards {
		if reward.ItemID == itemID {
			return true
		}
	}
	return false
}

func aikenDisjointAckHasItem(rows []currentDisjointRewardRow, itemID uint32) bool {
	for _, row := range rows {
		if row.ItemID == itemID {
			return true
		}
	}
	return false
}

func aikenDisjointInventoryCount(inventory dnfrepo.InventoryRecord, itemID int64) int64 {
	var total int64
	for _, stack := range inventory.Slots {
		if stack.ItemID == itemID {
			total += stack.Count
		}
	}
	return total
}

func TestAikenDisjointSharedRewardResolverFiltersConfiguredItemOnly(t *testing.T) {
	catalog := aikenDisjointTestCatalog(t, aikenDisjointValidCustomInfo)
	config, err := loadCurrentDisjointPVFConfig(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	disabled := config.DisabledByCustomAbilityType["warriors dungeon"]
	if _, found := disabled[10099775]; !found || len(disabled) != 1 {
		t.Fatalf("warriors dungeon disable set=%v", disabled)
	}

	for _, testCase := range []struct {
		itemID uint32
		want   bool
	}{
		{itemID: 5000, want: false},
		{itemID: 5001, want: true},
	} {
		t.Run("item_"+fmt.Sprint(testCase.itemID), func(t *testing.T) {
			definition, err := catalog.ResolveItem(testCase.itemID)
			if err != nil {
				t.Fatal(err)
			}
			document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
			if err != nil {
				t.Fatal(err)
			}
			rewards, err := currentEquipmentDisjointRewards(config, document)
			if err != nil {
				t.Fatal(err)
			}
			if got := aikenDisjointHasReward(rewards, 10099775); got != testCase.want {
				t.Fatalf("legendary soul present=%t want=%t rewards=%+v", got, testCase.want, rewards)
			}
			if !aikenDisjointHasReward(rewards, config.CubeItemID) || !aikenDisjointHasReward(rewards, 3166) {
				t.Fatalf("non-disabled rewards were lost: %+v", rewards)
			}
		})
	}
}

func TestAikenDisjointSingleEntryPreservesOtherRewards(t *testing.T) {
	catalog := aikenDisjointTestCatalog(t, aikenDisjointValidCustomInfo)
	repositories := aikenDisjointTestRepositories(t, map[string]dnfrepo.ItemStack{
		"0:60": {ItemID: 5000, Count: 1},
	})
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(60, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	if aikenDisjointAckHasItem(rows, 10099775) {
		t.Fatalf("Aiken ACK exposed legendary soul: %+v", rows)
	}
	if !aikenDisjointAckHasItem(rows, 3037) || !aikenDisjointAckHasItem(rows, 3166) {
		t.Fatalf("Aiken cube/additional rewards missing: %+v", rows)
	}
	ctx := context.Background()
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	if source, found := inventory.Slots["0:60"]; found && source.ItemID != 0 {
		t.Fatalf("Aiken source was not consumed: %+v", source)
	}
	account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if aikenDisjointInventoryCount(inventory, 10099775) != 0 {
		t.Fatal("Aiken legendary soul entered character inventory")
	}
	if stack := account.Slots["0:364"]; stack.ItemID == 10099775 {
		t.Fatalf("Aiken legendary soul entered the soul warehouse: %+v", stack)
	}
}

func TestOrdinaryLegendarySingleEntryKeepsLegendarySoul(t *testing.T) {
	catalog := aikenDisjointTestCatalog(t, aikenDisjointValidCustomInfo)
	repositories := aikenDisjointTestRepositories(t, map[string]dnfrepo.ItemStack{
		"0:60": {ItemID: 5001, Count: 1},
	})
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(60, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	if !aikenDisjointAckHasItem(rows, 10099775) {
		t.Fatalf("ordinary legendary lost its soul: %+v", rows)
	}
	ctx := context.Background()
	account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if stack := account.Slots["0:364"]; stack.ItemID != 10099775 || stack.Count <= 0 {
		t.Fatalf("ordinary legendary soul warehouse=%+v", stack)
	}
}

func TestAikenAndOrdinaryLegendaryShareBatchEntryWithoutContamination(t *testing.T) {
	catalog := aikenDisjointTestCatalog(t, aikenDisjointValidCustomInfo)
	repositories := aikenDisjointTestRepositories(t, map[string]dnfrepo.ItemStack{
		"0:60": {ItemID: 5000, Count: 1},
		"0:61": {ItemID: 5001, Count: 1},
	})
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	body := currentDisjointBatchBody([]int16{60, 61}, -1, 0)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	rows := parseCurrentDisjointRewardRows(t, ack.Body)
	soul := currentDisjointRewardRowForItem(t, rows, 10099775)
	if soul.Granted < 5 || soul.Granted > 7 {
		t.Fatalf("mixed batch soul count=%d; only the ordinary legendary item should contribute 5..7", soul.Granted)
	}
	if !aikenDisjointAckHasItem(rows, 3037) || !aikenDisjointAckHasItem(rows, 3166) {
		t.Fatalf("mixed batch lost ordinary rewards: %+v", rows)
	}
	ctx := context.Background()
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	if source, found := inventory.Slots["0:60"]; found && source.ItemID != 0 {
		t.Fatalf("Aiken batch source was not consumed: %+v", source)
	}
	if source, found := inventory.Slots["0:61"]; found && source.ItemID != 0 {
		t.Fatalf("ordinary batch source was not consumed: %+v", source)
	}
}

func TestAikenDisjointDisableTableRejectsMalformedInput(t *testing.T) {
	tests := map[string]string{
		"missing-type":      "[disjoint disable item]\n10099775\n[/disjoint disable item]\n",
		"duplicate-type":    "[custom ability type]\n`warriors dungeon`\n[custom ability type]\n`warriors dungeon`\n[disjoint disable item]\n10099775\n[/disjoint disable item]\n",
		"string-item":       "[custom ability type]\n`warriors dungeon`\n[disjoint disable item]\n`10099775`\n[/disjoint disable item]\n",
		"float-item":        "[custom ability type]\n`warriors dungeon`\n[disjoint disable item]\n1.5\n[/disjoint disable item]\n",
		"zero-item":         "[custom ability type]\n`warriors dungeon`\n[disjoint disable item]\n0\n[/disjoint disable item]\n",
		"negative-item":     "[custom ability type]\n`warriors dungeon`\n[disjoint disable item]\n-1\n[/disjoint disable item]\n",
		"oversize-item":     "[custom ability type]\n`warriors dungeon`\n[disjoint disable item]\n4294967296\n[/disjoint disable item]\n",
		"duplicate-item":    "[custom ability type]\n`warriors dungeon`\n[disjoint disable item]\n10099775 10099775\n[/disjoint disable item]\n",
		"duplicate-section": "[custom ability type]\n`warriors dungeon`\n[disjoint disable item]\n10099775\n[/disjoint disable item]\n[disjoint disable item]\n10099773\n[/disjoint disable item]\n",
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			document, err := dnfpvf.Parse(currentDisjointIndividualCustomInfoPath, text)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := currentDisjointParseDisabledByCustomAbility(document); err == nil {
				t.Fatal("malformed custom-ability disable table was accepted")
			}
		})
	}
}

func TestMalformedAikenDisableTableStopsBeforeAssetMutation(t *testing.T) {
	malformed := "[custom ability type]\n`warriors dungeon`\n[group]\n[key] `1`\n[indexes] 1 2\n[/indexes]\n[/group]\n" +
		"[grade probability] `A` 200 `B` 800 `C` 3000 `D` 6000\n[/grade probability]\n" +
		"[disjoint disable item]\n`10099775`\n[/disjoint disable item]\n"
	catalog := aikenDisjointTestCatalog(t, malformed)
	repositories := aikenDisjointTestRepositories(t, map[string]dnfrepo.ItemStack{"0:60": {ItemID: 5000, Count: 1}})
	ctx := context.Background()
	beforeInventory, _, _ := repositories.Inventory.Load(ctx, "19")
	beforeAccount, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(60, 0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(ack.Body) == 0 || ack.Body[0] != 0 {
		t.Fatalf("malformed table was not rejected: %x", ack.Body)
	}
	afterInventory, _, _ := repositories.Inventory.Load(ctx, "19")
	afterAccount, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if !reflect.DeepEqual(beforeInventory, afterInventory) || !reflect.DeepEqual(beforeAccount, afterAccount) {
		t.Fatal("malformed custom-ability table changed items or rewards")
	}
}
