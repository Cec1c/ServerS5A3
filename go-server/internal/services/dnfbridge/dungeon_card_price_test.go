package dnfbridge

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentDungeonPaidCardCostUsesLevelCostPairs(t *testing.T) {
	source := dungeonDropCatalogTestSource{
		currentDungeonClearRewardRulePath: "[gold card cost table]\n5 111\n10 222\n20 0\n30 4294967295\n[/gold card cost table]\n",
		// 奖励金币来自另一张表，不能替代付费价格。
		dungeonCardGoldReferencePath: "[gold drop ref table]\n5 999999 42\n10 888888 42\n",
	}
	for _, test := range []struct {
		level, want int64
	}{
		{1, 111}, {5, 111}, {9, 111}, {10, 222}, {19, 222},
		{20, 0}, {29, 0}, {30, math.MaxUint32}, {100, math.MaxUint32},
	} {
		got, err := currentDungeonPaidCardCost(source, test.level)
		if err != nil || got != test.want {
			t.Fatalf("level=%d cost=%d want=%d err=%v", test.level, got, test.want, err)
		}
	}
}

func TestCurrentDungeonPaidCardCostRejectsIncompleteOrInvalidRules(t *testing.T) {
	if _, err := currentDungeonPaidCardCost(nil, 1); !errors.Is(err, errDungeonCardPVFSourceRequired) {
		t.Fatalf("nil source err=%v", err)
	}
	if _, err := currentDungeonPaidCardCost(dungeonDropCatalogTestSource{}, 1); err == nil {
		t.Fatal("missing price file was silently treated as free")
	}
	for _, test := range []struct {
		name, document string
		level          int64
		want           error
	}{
		{"missing_section", "[other]\n1 10\n", 1, errDungeonCardPVFSectionMissing},
		{"empty_section", "[gold card cost table]\n[/gold card cost table]\n", 1, errDungeonCardPVFSectionShape},
		{"unpaired_value", "[gold card cost table]\n1 100 2\n", 1, errDungeonCardPVFSectionShape},
		{"invalid_token_between_rows", "[gold card cost table]\n1 100 `bad` 2 200\n", 1, errDungeonCardPVFSectionShape},
		{"negative_cost", "[gold card cost table]\n1 -1\n", 1, errDungeonCardPVFSectionShape},
		{"cost_over_u32", "[gold card cost table]\n1 4294967296\n", 1, errDungeonCardPVFSectionShape},
		{"zero_row_level", "[gold card cost table]\n0 100\n", 1, errDungeonCardPVFSectionShape},
		{"negative_row_level", "[gold card cost table]\n-1 100\n", 1, errDungeonCardPVFSectionShape},
		{"duplicate_level", "[gold card cost table]\n1 100\n1 200\n", 1, errDungeonCardPVFSectionShape},
		{"descending_level", "[gold card cost table]\n2 100\n1 200\n", 1, errDungeonCardPVFSectionShape},
		// 即便已命中前行，也不能忽略价格表后部的坏数据。
		{"invalid_unused_later_row", "[gold card cost table]\n1 100\n20 -1\n", 1, errDungeonCardPVFSectionShape},
		{"zero_requested_level", "[gold card cost table]\n1 100\n", 0, errDungeonCardPVFSectionShape},
		{"negative_requested_level", "[gold card cost table]\n1 100\n", -1, errDungeonCardPVFSectionShape},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := dungeonDropCatalogTestSource{currentDungeonClearRewardRulePath: test.document}
			if _, err := currentDungeonPaidCardCost(source, test.level); !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
}

func TestCurrentDungeonPaidCardPriceIsNotACommittedReward(t *testing.T) {
	snapshot := currentDungeonClearRewardSnapshot{
		CharacterID: 19, CompletionKey: "priced-card", Source: "price_only", Committed: true,
		PaidCardCost: 580,
	}
	if currentDungeonClearRewardHasCommittedValue(snapshot) {
		t.Fatal("a quoted card price was counted as an awarded asset")
	}
	if _, err := buildCurrentDungeonClearRewardBody(snapshot); !errors.Is(err, errCurrentDungeonClearRewardShape) {
		t.Fatalf("price-only snapshot err=%v", err)
	}
}

func TestRealScriptPVFDungeonPaidCardPricesUseClearRewardTable(t *testing.T) {
	pvfPath := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("设置DNF_WORLDMAP_REAL_PVF_SMOKE以核对真实PVF付费翻牌价格")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dungeon, found := table.FindDungeon(3)
	if !found || !dungeon.Metadata.BasisLevel.Set || dungeon.Metadata.BasisLevel.Value <= 0 {
		t.Fatalf("真实3号副本缺少有效基础等级：found=%t dungeon=%+v", found, dungeon)
	}
	document, err := parseDungeonCardPVFDocument(archive, currentDungeonClearRewardRulePath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := dungeonCardPVFSectionInts(document, "gold card cost table")
	if err != nil || len(rows)%2 != 0 {
		t.Fatalf("真实价格表不是等级/价格二元行：values=%d err=%v", len(rows), err)
	}
	exactCosts := make(map[int64]int64, len(rows)/2)
	for offset := 0; offset < len(rows); offset += 2 {
		exactCosts[rows[offset]] = rows[offset+1]
	}
	goldDocument, err := parseDungeonCardPVFDocument(archive, dungeonCardGoldReferencePath)
	if err != nil {
		t.Fatal(err)
	}
	goldReferences, err := parseDungeonCardPVFGoldReferences(goldDocument)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name  string
		level int64
	}{
		{"3号幽暗密林的真实基础等级", dungeon.Metadata.BasisLevel.Value},
		{"84级普通副本价格档", 84},
	} {
		cost, err := currentDungeonPaidCardCost(archive, sample.level)
		if err != nil {
			t.Fatalf("%s level=%d: %v", sample.name, sample.level, err)
		}
		want, exact := exactCosts[sample.level]
		if !exact || want <= 0 || cost != want {
			t.Fatalf("%s level=%d cost=%d 表内精确价格=%d exact=%t；真实普通价格应非零", sample.name, sample.level, cost, want, exact)
		}
		goldReference, goldFound := goldReferences[sample.level]
		if !goldFound {
			t.Fatalf("%s 缺少独立的奖励金币参考：level=%d", sample.name, sample.level)
		}
		t.Logf("真实付费翻牌价格：%s level=%d cost=%d；价格来源=%s [gold card cost table]，行数=%d；奖励金币另源=%s ref=(%d,%d)，PVF=%s",
			sample.name, sample.level, cost, currentDungeonClearRewardRulePath, len(rows)/2,
			dungeonCardGoldReferencePath, goldReference.ValueA, goldReference.ValueB, pvfPath)
	}
}
