package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

var realAikenDisjointItemIDs = []uint32{
	100050746, 100060616, 100070568, 100080591, 100090403,
	100100658, 100110602, 100120561, 100130573, 100140398,
	100150627, 100160543, 100170536, 100180541, 100190369,
	100200602, 100210567, 100220536, 100230541, 100240376,
	100250621, 100260583, 100270532, 100280536, 100290389,
	100300856, 100312549, 100322439, 100344655, 100352922,
	100390016,
}

func loadRealAikenDisjointCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_AIKEN_REAL_PVF_SMOKE"))
	if path == "" {
		path = strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	}
	if path == "" {
		t.Skip("set DNFBRIDGE_AIKEN_REAL_PVF_SMOKE to the matching deployed PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestRealPVFAikenDisjointRewardsExcludeLegendarySoulAndRetainOtherRows(t *testing.T) {
	catalog := loadRealAikenDisjointCatalog(t)
	config, err := loadCurrentDisjointPVFConfig(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	if !config.CustomAbilityDisableLoaded {
		t.Fatal("real custom-ability disable rules were not loaded")
	}
	disabled := config.DisabledByCustomAbilityType["warriors dungeon"]
	if _, found := disabled[10099775]; !found || len(disabled) != 1 {
		t.Fatalf("real warriors-dungeon disabled items=%v", disabled)
	}
	if len(realAikenDisjointItemIDs) != 31 {
		t.Fatalf("fixture count=%d", len(realAikenDisjointItemIDs))
	}
	for _, itemID := range realAikenDisjointItemIDs {
		itemID := itemID
		t.Run(fmt.Sprint(itemID), func(t *testing.T) {
			definition, err := catalog.ResolveItem(itemID)
			if err != nil {
				t.Fatal(err)
			}
			document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
			if err != nil {
				t.Fatal(err)
			}
			customAbilityType, err := currentDisjointDocumentCustomAbilityType(document)
			if err != nil || customAbilityType != "warriors dungeon" {
				t.Fatalf("custom ability type=%q err=%v path=%s", customAbilityType, err, definition.PVFPath)
			}
			rewards, err := currentEquipmentDisjointRewards(config, document)
			if err != nil {
				t.Fatal(err)
			}
			if aikenDisjointHasReward(rewards, 10099775) {
				t.Fatalf("Aiken path=%s received legendary soul: %+v", definition.PVFPath, rewards)
			}
			if !aikenDisjointHasReward(rewards, config.CubeItemID) || len(rewards) < 2 {
				t.Fatalf("Aiken path=%s lost non-disabled rewards: %+v", definition.PVFPath, rewards)
			}
		})
	}

	controlDefinition, err := catalog.ResolveItem(101020542)
	if err != nil {
		t.Fatal(err)
	}
	controlDocument, err := parseDungeonCardPVFDocument(catalog.source, controlDefinition.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	controlRewards, err := currentEquipmentDisjointRewards(config, controlDocument)
	if err != nil {
		t.Fatal(err)
	}
	if !aikenDisjointHasReward(controlRewards, 10099775) {
		t.Fatalf("ordinary legendary lost its soul: %+v", controlRewards)
	}
}

func TestRealPVFAikenAndOrdinaryLegendaryDisjointTransactions(t *testing.T) {
	catalog := loadRealAikenDisjointCatalog(t)
	for _, testCase := range []struct {
		name       string
		itemID     uint32
		wantSoul   bool
		sourceSlot int16
	}{
		{name: "aiken", itemID: 100120561, wantSoul: false, sourceSlot: 60},
		{name: "ordinary-legendary", itemID: 101020542, wantSoul: true, sourceSlot: 61},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repositories := aikenDisjointTestRepositories(t, map[string]dnfrepo.ItemStack{
				fmt.Sprintf("0:%d", testCase.sourceSlot): {ItemID: int64(testCase.itemID), Count: 1},
			})
			connection := &bufferConn{}
			service := currentDisjointTestService(repositories, catalog)
			if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, currentDisjointItemTestBody(testCase.sourceSlot, 0, -1, 0)); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
			rows := parseCurrentDisjointRewardRows(t, ack.Body)
			if got := aikenDisjointAckHasItem(rows, 10099775); got != testCase.wantSoul {
				t.Fatalf("legendary soul present=%t want=%t rows=%+v", got, testCase.wantSoul, rows)
			}
			ctx := context.Background()
			inventory, _, _ := repositories.Inventory.Load(ctx, "19")
			if stack, found := inventory.Slots[fmt.Sprintf("0:%d", testCase.sourceSlot)]; found && stack.ItemID != 0 {
				t.Fatalf("source was not consumed: %+v", stack)
			}
			account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
			stack := account.Slots["0:364"]
			if (stack.ItemID == 10099775 && stack.Count > 0) != testCase.wantSoul {
				t.Fatalf("soul warehouse present=%t want=%t stack=%+v", stack.ItemID == 10099775 && stack.Count > 0, testCase.wantSoul, stack)
			}
		})
	}
}
