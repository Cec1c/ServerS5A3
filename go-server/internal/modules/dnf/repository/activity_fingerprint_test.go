package repository

import (
	"testing"
	"time"
)

func TestActivityFingerprintPreservesReplayBusinessFields(t *testing.T) {
	base := ActivityEvent{AccountID: "账号\x00A", ID: "run:receipt", CharacterID: "19", CharacterName: "旧名", Metric: ActivityFatigue, Amount: 1, OccurredAt: time.Now(), DungeonID: 310, Source: "dungeon_room"}
	changed := base
	changed.CharacterName, changed.OccurredAt = "新名", base.OccurredAt.Add(48*time.Hour)
	if !SameActivityEvent(base, changed) || ActivityPayloadFingerprintV1(base) != ActivityPayloadFingerprintV1(changed) {
		t.Fatal("display name or retry timestamp changed permanent receipt identity")
	}
	mutations := []func(*ActivityEvent){func(e *ActivityEvent) { e.AccountID += "x" }, func(e *ActivityEvent) { e.ID += "x" },
		func(e *ActivityEvent) { e.CharacterID += "x" }, func(e *ActivityEvent) { e.Metric += "x" }, func(e *ActivityEvent) { e.Amount++ },
		func(e *ActivityEvent) { e.DungeonID++ }, func(e *ActivityEvent) { e.ItemID++ }, func(e *ActivityEvent) { e.Rarity++ }, func(e *ActivityEvent) { e.Source += "x" }}
	for i, mutate := range mutations {
		changed = base
		mutate(&changed)
		if SameActivityEvent(base, changed) || ActivityPayloadFingerprintV1(base) == ActivityPayloadFingerprintV1(changed) {
			t.Fatalf("business field %d was ignored", i)
		}
	}
	left, right := base, base
	left.AccountID, left.ID = "a\x00b", "c"
	right.AccountID, right.ID = "a", "b\x00c"
	if ActivityPayloadFingerprintV1(left) == ActivityPayloadFingerprintV1(right) {
		t.Fatal("length encoding allowed a delimiter ambiguity")
	}
}
