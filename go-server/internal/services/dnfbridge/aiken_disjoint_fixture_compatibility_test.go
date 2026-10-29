package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// TestLegacyDisjointFixturesRemainCompatibleForOrdinaryEquipment documents the
// callers whose synthetic PVF has no individualcustominfo.etc. Those fixtures
// contain only ordinary equipment, so making the custom-ability table optional
// at load time keeps them useful without weakening the Aiken check below.
func TestLegacyDisjointFixturesRemainCompatibleForOrdinaryEquipment(t *testing.T) {
	tests := []struct {
		name  string
		build func(*testing.T) (dnfrepo.Group, *pvfDungeonDropCatalog)
	}{
		{
			name: "mustCurrentDisjointTestAssets",
			build: func(t *testing.T) (dnfrepo.Group, *pvfDungeonDropCatalog) {
				return mustCurrentDisjointTestAssets(t, nil)
			},
		},
		{
			name: "mustCurrentDisjointPlacementAssets",
			build: func(t *testing.T) (dnfrepo.Group, *pvfDungeonDropCatalog) {
				return mustCurrentDisjointPlacementAssets(t, nil)
			},
		},
		{
			name: "currentDisjointRarityFallbackAssets",
			build: func(t *testing.T) (dnfrepo.Group, *pvfDungeonDropCatalog) {
				return currentDisjointRarityFallbackAssets(t, nil)
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, catalog := testCase.build(t)
			config, err := loadCurrentDisjointPVFConfig(catalog.source)
			if err != nil {
				t.Fatalf("ordinary-only fixture must not require Aiken custom-ability data: %v", err)
			}
			if len(config.DisabledByCustomAbilityType) != 0 {
				t.Fatalf("legacy fixture unexpectedly has custom-ability disables: %v", config.DisabledByCustomAbilityType)
			}
			if config.CustomAbilityDisableLoaded {
				t.Fatal("legacy fixture without the file reported loaded custom-ability rules")
			}
			definition, err := catalog.ResolveItem(5000)
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
			if !aikenDisjointHasReward(rewards, config.CubeItemID) {
				t.Fatalf("ordinary fixture lost its cube: %+v", rewards)
			}
		})
	}
}

func TestNonAikenCustomAbilityLegendaryKeepsOrdinarySoulAndCube(t *testing.T) {
	catalog := aikenDisjointTestCatalog(t, aikenDisjointValidCustomInfo)
	config, err := loadCurrentDisjointPVFConfig(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	if !config.CustomAbilityDisableLoaded {
		t.Fatal("valid custom-ability file was not recorded as loaded")
	}
	document, err := dnfpvf.Parse("equipment/non_aiken_custom_legend.equ",
		"[equipment type] `[coat]`\n[rarity] 6\n[minimum level] 90\n[value] 1000\n"+
			"[custom ability type]\n`epic production`\n")
	if err != nil {
		t.Fatal(err)
	}
	customAbilityType, err := currentDisjointDocumentCustomAbilityType(document)
	if err != nil || customAbilityType != "epic production" {
		t.Fatalf("custom ability type=%q err=%v", customAbilityType, err)
	}
	rewards, err := currentEquipmentDisjointRewards(config, document)
	if err != nil {
		t.Fatal(err)
	}
	if !aikenDisjointHasReward(rewards, config.CubeItemID) || !aikenDisjointHasReward(rewards, 10099775) {
		t.Fatalf("non-Aiken custom equipment was filtered: %+v", rewards)
	}
}

func TestAikenMissingDisableTableFailsClosed(t *testing.T) {
	aikenDocument, err := dnfpvf.Parse("equipment/aiken_without_disable_table.equ",
		"[equipment type] `[coat]`\n[rarity] 6\n[minimum level] 90\n[value] 1000\n"+
			"[custom ability type]\n`warriors dungeon`\n[custom ability count] 2\n")
	if err != nil {
		t.Fatal(err)
	}
	_, legacyCatalog := mustCurrentDisjointTestAssets(t, nil)
	legacyConfig, err := loadCurrentDisjointPVFConfig(legacyCatalog.source)
	if err != nil {
		t.Fatal(err)
	}
	if legacyConfig.DisabledByCustomAbilityType != nil {
		t.Fatalf("legacy ordinary fixture unexpectedly has a disable table: %v", legacyConfig.DisabledByCustomAbilityType)
	}
	if legacyConfig.CustomAbilityDisableLoaded {
		t.Fatal("missing custom-ability file was marked loaded")
	}
	withoutSection := "[custom ability type]\n`warriors dungeon`\n[group]\n[key] `1`\n[indexes] 1 2\n[/indexes]\n[/group]\n" +
		"[grade probability] `A` 200 `B` 800 `C` 3000 `D` 6000\n[/grade probability]\n"
	withFileButNoSection := aikenDisjointTestCatalog(t, withoutSection)
	missingSectionConfig, err := loadCurrentDisjointPVFConfig(withFileButNoSection.source)
	if err != nil {
		t.Fatal(err)
	}
	if !missingSectionConfig.CustomAbilityDisableLoaded {
		t.Fatal("present custom-ability file with no disable section was marked unloaded")
	}
	for name, config := range map[string]currentDisjointPVFConfig{
		"missing-file":    legacyConfig,
		"missing-section": missingSectionConfig,
	} {
		t.Run(name, func(t *testing.T) {
			if rewards, err := currentEquipmentDisjointRewards(config, aikenDocument); err == nil {
				t.Fatalf("Aiken source without its disable table must fail closed, rewards=%+v", rewards)
			}
		})
	}
}

func TestAikenMissingCustomAbilityFileFailsBeforeAssetMutation(t *testing.T) {
	catalog := aikenDisjointTestCatalog(t, "")
	repositories := aikenDisjointTestRepositories(t, map[string]dnfrepo.ItemStack{
		"0:60": {ItemID: 5000, Count: 1},
	})
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
		t.Fatalf("Aiken without custom-ability rules was not rejected: %x", ack.Body)
	}
	afterInventory, _, _ := repositories.Inventory.Load(ctx, "19")
	afterAccount, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if !reflect.DeepEqual(beforeInventory, afterInventory) || !reflect.DeepEqual(beforeAccount, afterAccount) {
		t.Fatal("missing custom-ability rules changed Aiken source or rewards")
	}
}

func TestAikenDisableTableDoesNotConsumeCubeOrAdditionalReward(t *testing.T) {
	catalog := aikenDisjointTestCatalog(t, aikenDisjointValidCustomInfo)
	config, err := loadCurrentDisjointPVFConfig(catalog.source)
	if err != nil {
		t.Fatal(err)
	}
	disabled := config.DisabledByCustomAbilityType["warriors dungeon"]
	if _, blocked := disabled[config.CubeItemID]; blocked {
		t.Fatalf("cube %d was placed in the disable set", config.CubeItemID)
	}
	definition, err := catalog.ResolveItem(5000)
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
	if !aikenDisjointHasReward(rewards, config.CubeItemID) || !aikenDisjointHasReward(rewards, 3166) {
		t.Fatalf("cube/additional reward filtered with soul: %+v", rewards)
	}
	if aikenDisjointHasReward(rewards, 10099775) {
		t.Fatalf("Aiken legendary soul was not filtered: %+v", rewards)
	}
}
