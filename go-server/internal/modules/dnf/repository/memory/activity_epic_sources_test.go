package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestActivityEpicSourcesScopePeriodsReplayAndHistoricalRoles(t *testing.T) {
	g := activityFixture(t)
	ctx := context.Background()
	boundary := time.Date(2026, 10, 26, 6, 0, 0, 0, time.FixedZone("CN", 8*3600))
	write := func(id, character string, dungeon int64, at time.Time) {
		t.Helper()
		e := repo.ActivityEvent{ID: id, AccountID: "a", CharacterID: character, DungeonID: dungeon, Metric: repo.ActivityEpic, Amount: 1, ItemID: 9001, Rarity: 4, Source: "dungeon_pickup", OccurredAt: at}
		if err := recordActivity(g, e); err != nil {
			t.Fatal(err)
		}
	}
	write("before", "1", 316, boundary.Add(-time.Second))
	write("road", "1", 6065, boundary)
	write("time", "2", 7539, boundary.Add(time.Second))
	write("time", "2", 7539, boundary.Add(time.Hour)) // 相同事件重试，不新增。
	for i := 0; i < 205; i++ {
		write(fmt.Sprintf("extra-%03d", i), "1", 316, boundary.Add(time.Minute))
	}
	ch, _, _ := g.Character.Load(ctx, "2")
	ch.Stats = map[string]int64{"delete_flag": 1}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, character, day string
		total                int64
		rows                 int
	}{
		{"累计", "", "", 208, 3}, {"周一06点后", "", "2026-10-26", 207, 3},
		{"前一服务日", "", "2026-10-25", 1, 1}, {"历史角色", "2", "", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := repo.ActivityQuery{AccountID: "a", CharacterID: tc.character, FromDay: tc.day, ThroughDay: tc.day, IncludeEpicSources: true, Limit: 1}
			summary, err := g.Activity.Summary(ctx, q)
			if err != nil || summary.Metrics[repo.ActivityEpic] != tc.total || len(summary.EpicSources) != tc.rows {
				t.Fatalf("分项查询错误：%+v %v", summary, err)
			}
			var sum int64
			for _, row := range summary.EpicSources {
				sum += row.Amount
				if tc.character != "" && row.CharacterID != tc.character {
					t.Fatal("串角色", row)
				}
			}
			if sum != tc.total {
				t.Fatalf("分项丢失或误用明细分页：%d/%d", sum, tc.total)
			}
		})
	}
	plain, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "a"})
	if err != nil || len(plain.EpicSources) != 0 || plain.Metrics[repo.ActivityEpic] != 208 {
		t.Fatal("默认查询契约改变", plain, err)
	}
	foreign, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "b", IncludeEpicSources: true})
	if err != nil || len(foreign.EpicSources) != 0 {
		t.Fatal("串账号", foreign, err)
	}
}
