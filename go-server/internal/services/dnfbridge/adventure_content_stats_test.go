package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestAdventureContentNativeOffsetsStaySeparateFromCollection(t *testing.T) {
	p := adventuregroup.Projection{ContentCounts: [7]uint32{70001, 70002, 70003, 70004, 70005, 70006, 70007}, ConsecutiveLoginDays: 8}
	p.CollectionCounts[0], p.CollectionCounts[19] = 2, 3
	body := buildCurrentAdventureInfoBodyWithState(20, adventuregroup.Summary{ManageLevel: 7}, nil, "", 20261013, p)
	raw := body[18 : 18+7424]
	for kind, offset := range []int{16, 32, 24, 28, 36, 40, 7420} {
		if got := binary.LittleEndian.Uint32(raw[offset : offset+4]); got != p.ContentCounts[kind] {
			t.Fatalf("第%d项在原生偏移%d读到%d", kind, offset, got)
		}
	}
	if binary.LittleEndian.Uint32(raw[20:24]) != 8 || raw[7348] != 0 || binary.LittleEndian.Uint16(raw[7349:7351]) != 2 || raw[7405] != 19 || binary.LittleEndian.Uint16(raw[7406:7408]) != 3 {
		t.Fatal("内容统计覆盖了登录或收藏字段")
	}
	if dir := os.Getenv("DNF90_ADVENTURE_CONTENT_FIXTURES"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "七项内容统计.bin"), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdventureContentStatsReloadFromOwnedDurableSources(t *testing.T) {
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	account := repo.AccountRecord{AccountID: "dnf:1", Metadata: map[string]string{
		adventureGroupCreatedDateMetadataKey: "2026-10-13",
		dnfpvp.TotalProgressMetadataKey:      `{"stats":{"pvp_wins":5}}`,
	}}
	if err := g.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.CharacterRecord{
		{CharacterID: "20", AccountID: "dnf:1", Slot: 0, Level: 90, Stats: map[string]int64{
			adventuregroup.RecommendedDungeonClearStatKey: 7, dnfpvp.WinsKey: 2,
			adventuregroup.DespairHundredClearStatKey: 3, adventuregroup.GriefHundredClearStatKey: 4,
			"raid_clear_v1_1_00112233445566778899aabbccddeeff": 100,
			"raid_clear_v1_2_00112233445566778899aabbccddeeff": 100,
			"raid_clear_v1_3_00112233445566778899aabbccddeeff": 100,
		}},
		{CharacterID: "21", AccountID: "dnf:1", Slot: 1, Level: 90, Stats: map[string]int64{
			adventuregroup.RecommendedDungeonClearStatKey: 5, dnfpvp.WinsKey: 1,
			adventuregroup.GriefHundredClearStatKey:            1,
			"raid_clear_v1_1_10112233445566778899aabbccddeeff": 100,
		}},
		{CharacterID: "22", AccountID: "dnf:other", Slot: 0, Level: 90, Stats: map[string]int64{dnfpvp.WinsKey: 999}},
	} {
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		s := &Service{options: options{accountID: "dnf:1"}, adventureGroupTable: loadAdventureGroupTestTables(t), repositoryProvider: func() (repo.Group, bool) { return g, true }}
		state, err := s.currentAccountAdventureGroupInfoState(ctx, repo.CharacterRecord{}, false)
		if err != nil {
			t.Fatal(err)
		}
		if state.Projection.ContentCounts != [7]uint32{12, 8, 3, 5, 2, 1, 1} {
			t.Fatalf("重建服务后的账号累计错误：%+v", state.Projection.ContentCounts)
		}
	}
	loaded, _, _ := g.Account.Load(ctx, "dnf:1")
	if loaded.Metadata[dnfpvp.TotalProgressMetadataKey] != account.Metadata[dnfpvp.TotalProgressMetadataKey] {
		t.Fatal("查看冒险团改写了决斗战绩")
	}
}
