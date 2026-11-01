package repository

import (
	"testing"
	"time"
)

func TestAdventureRankEventOnlyAcceptsSettlementSource(t *testing.T) {
	e := ActivityEvent{ID: "recommended:1", AccountID: "a", CharacterID: "c", Metric: ActivityRecommendedDungeon, Amount: 1, OccurredAt: time.Now(), DungeonID: 310, Source: "recommended_clear"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*ActivityEvent){
		func(e *ActivityEvent) { e.Amount = 2 },
		func(e *ActivityEvent) { e.Source = "hell_party" },
		func(e *ActivityEvent) { e.ItemID = 123 },
		func(e *ActivityEvent) { e.Rarity = 4 },
		func(e *ActivityEvent) { e.DungeonID = -1 },
	} {
		bad := e
		edit(&bad)
		if bad.Validate() == nil {
			t.Fatal("接受伪造适级结算记录", bad)
		}
	}
}
