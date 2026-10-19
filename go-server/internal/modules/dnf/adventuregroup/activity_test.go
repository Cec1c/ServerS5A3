package adventuregroup

import (
	"math"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestProjectActivitySumsRecommendedDungeonClearsAndSaturates(t *testing.T) {
	got := ProjectActivity(24, []dnfrepo.CharacterRecord{
		{Stats: map[string]int64{RecommendedDungeonClearStatKey: 7}},
		{Stats: map[string]int64{RecommendedDungeonClearStatKey: -1}},
		{Stats: map[string]int64{RecommendedDungeonClearStatKey: math.MaxUint32}},
	})
	if got.ConsecutiveLoginDays != 24 {
		t.Fatalf("consecutive days=%d, want 24", got.ConsecutiveLoginDays)
	}
	if got.ContentCounts[ContentTypeRecommendedDungeon] != math.MaxUint32 {
		t.Fatalf("recommended clears=%d, want %d", got.ContentCounts[ContentTypeRecommendedDungeon], math.MaxUint32)
	}
	for index := 1; index < ContentTypeCount; index++ {
		if got.ContentCounts[index] != 0 {
			t.Fatalf("unsupported content type %d=%d, want 0", index, got.ContentCounts[index])
		}
	}
}

func TestProjectActivityKeepsTowerTotalsAndCollectionIndependent(t *testing.T) {
	got := ProjectActivity(8, []dnfrepo.CharacterRecord{
		{Stats: map[string]int64{RecommendedDungeonClearStatKey: 70000, DespairHundredClearStatKey: 2, GriefHundredClearStatKey: 3}},
		{Stats: map[string]int64{DespairHundredClearStatKey: 1, GriefHundredClearStatKey: math.MaxInt64}},
		{Stats: map[string]int64{"delete_flag": 1, RecommendedDungeonClearStatKey: 1000}},
	})
	if got.ContentCounts[ContentTypeRecommendedDungeon] != 70000 || got.ContentCounts[ContentTypeDespairHundred] != 3 || got.ContentCounts[ContentTypeGriefHundred] != math.MaxUint32 {
		t.Fatalf("累计投影错误：%+v", got.ContentCounts)
	}
	if got.CollectionCounts != [20]uint16{} || got.ConsecutiveLoginDays != 8 {
		t.Fatal("内容统计污染收藏或登录天数")
	}
}
