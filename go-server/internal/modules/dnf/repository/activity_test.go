package repository

import (
	"testing"
	"time"
)

func TestActivityInputAndServiceDay(t *testing.T) {
	at := time.Date(2026, 10, 23, 22, 0, 0, 0, time.UTC)
	if ActivityDay(at) != "2026-10-24" || ActivityDay(at.Add(-time.Second)) != "2026-10-23" {
		t.Fatal("服务日边界错误")
	}
	for _, q := range []ActivityQuery{{}, {AccountID: "a", FromDay: "2026-10-24"}, {AccountID: "a", FromDay: "2026-02-30", ThroughDay: "2026-03-01"}, {AccountID: "a", Limit: 201}, {AccountID: "a", BeforeID: "x"}} {
		if q.Validate() == nil {
			t.Fatalf("接受无效查询：%+v", q)
		}
	}
	e := ActivityEvent{ID: "id", AccountID: "a", CharacterID: "1", Metric: ActivityEpic, Amount: 1, OccurredAt: at, DungeonID: 310, ItemID: 7, Rarity: 4, Source: "dungeon_pickup"}
	if e.Validate() != nil {
		t.Fatal("合法品质被拒绝")
	}
	e.Source = "mail"
	if e.Validate() == nil {
		t.Fatal("邮件混入副本拾取")
	}
	e.Source = "dungeon_pickup"
	e.Rarity = 3
	if e.Validate() == nil {
		t.Fatal("品质口径冲突")
	}
}
