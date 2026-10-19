package raid

import (
	"context"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRecordedLifetimeClearsSurviveWeeklyResetAndReplay(t *testing.T) {
	for mode := byte(1); mode <= 3; mode++ {
		g, cmd := weeklyFixture(t)
		cmd.Mode = mode
		for i := 0; i < 2; i++ {
			if _, err := CommitWeeklyClear(context.Background(), g, cmd); err != nil {
				t.Fatal(err)
			}
		}
		cmd.CompletedAt = cmd.CompletedAt.Add(7 * 24 * time.Hour)
		cmd.InstanceID = "10112233445566778899aabbccddeeff"
		if _, err := CommitWeeklyClear(context.Background(), g, cmd); err != nil {
			t.Fatal(err)
		}
		character, _, _ := g.Character.Load(context.Background(), "1")
		if RecordedLifetimeClears(character, mode) != 2 {
			t.Fatalf("模式%d的永久回执累计错误", mode)
		}
		if used, err := WeeklyClears(character, mode, cmd.CompletedAt, cmd.Policy); err != nil || used != 1 {
			t.Fatalf("累计统计改变每周用量：%d/%v", used, err)
		}
	}
}

func TestRecordedLifetimeClearsRejectNonClearAndMalformedReceipts(t *testing.T) {
	c := repo.CharacterRecord{Stats: map[string]int64{
		"raid_clear_v1_1_00112233445566778899aabbccddeeff":      100,
		"raid_clear_v1_2_00112233445566778899aabbccddeeff":      100,
		"raid_clear_v1_1_00112233445566778899aabbccddee00":      0,
		"raid_clear_v1_1_00112233445566778899aabbccddee01":      -1,
		"raid_clear_v1_1_bad":                                   100,
		"raid_clear_v1_1_00112233445566778899aabbccddeeff_card": 100,
		"raid_weekly_clears_v1_1":                               99,
		"raid_reward_v1_1_00112233445566778899aabbccddeeff_1_1": 100,
	}}
	if RecordedLifetimeClears(c, 1) != 1 || RecordedLifetimeClears(c, 2) != 1 || RecordedLifetimeClears(c, 3) != 0 || RecordedLifetimeClears(c, 0) != 0 {
		t.Fatal("非终局回执、其它团本或无效模式混入统计")
	}
	c.Stats["delete_flag"] = 1
	if RecordedLifetimeClears(c, 1) != 0 {
		t.Fatal("已删除角色混入统计")
	}
}
