package repository

import (
	"testing"
	"time"
)

func TestBattlePassCounterChinaDayRetentionAndNoOtherStats(t *testing.T) {
	now := time.Date(2026, 10, 29, 6, 0, 0, 0, time.FixedZone("China", 28800))
	ch := CharacterRecord{Stats: map[string]int64{"gold": 42, BattlePassCounterPrefix + "2020-01-01:17:-1": 9}}
	AddBattlePassCounter(&ch, 17, -1, 3, now.Add(-time.Second))
	AddBattlePassCounter(&ch, 17, -1, 2, now)
	if ch.Stats[BattlePassCounterPrefix+"2026-10-28:17:-1"] != 3 || ch.Stats[BattlePassCounterPrefix+"2026-10-29:17:-1"] != 2 || ch.Stats["gold"] != 42 || ch.Stats[BattlePassCounterPrefix+"2020-01-01:17:-1"] != 0 {
		t.Fatal("日界/保留错误", ch.Stats)
	}
	for k := range ch.Stats {
		if k == "gold" {
			continue
		}
		if _, kind, param, ok := ReadBattlePassCounterKey(k); !ok || kind != 17 || param != -1 {
			t.Fatal("读取失败", k)
		}
	}
}
