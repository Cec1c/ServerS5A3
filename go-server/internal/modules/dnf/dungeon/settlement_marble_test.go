package dungeon

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/onlineevent"
)

func TestSettlementMarbleClearIsReceiptedWithExperience(t *testing.T) {
	ctx := context.Background()
	repos, cmd := luckyStarSettlementFixture(t, "0")
	cmd.LuckyStar = nil
	cmd.UpdatedAt = time.Date(2026, 10, 19, 12, 0, 0, 0, onlineevent.ChinaCalendar)
	cmd.AttendanceMarble = &onlineevent.MarbleCatalog{ClearRewards: [3][2]int64{{4, 2}, {8, 2}, {12, 2}}}
	character, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	character.Stats[onlineevent.MarbleDayKey] = onlineevent.MarbleDay(cmd.UpdatedAt)
	character.Stats["attendance_marble_clears"] = 3
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	first, err := owner.CommitSettlement(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	state, err := onlineevent.ReadMarbleState(first.Character, cmd.AttendanceMarble, cmd.UpdatedAt)
	if err != nil || state.Clears != 4 || state.Remaining() != 4 {
		t.Fatalf("推荐通关未增加骰子机会：%+v %v", state, err)
	}
	replay, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed || replay.Character.Stats["attendance_marble_clears"] != 4 {
		t.Fatalf("重复结算增加活动次数：%+v %v", replay, err)
	}
}

func TestSettlementMarbleIgnoresNonRecommendedAndRollsBackFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		repos, cmd := luckyStarSettlementFixture(t, "0")
		cmd.AttendanceMarble = &onlineevent.MarbleCatalog{ClearRewards: [3][2]int64{{4, 2}, {8, 2}, {12, 2}}}
		if fail {
			repos.CharacterSettlement = failingLuckyUOW{repos.CharacterSettlement}
		} else {
			cmd.LuckyStar, cmd.RecommendedDungeonClear = nil, false
		}
		owner, _ := NewOwner(repos)
		_, err := owner.CommitSettlement(context.Background(), cmd)
		if (err != nil) != fail {
			t.Fatalf("失败条件不符：%v", err)
		}
		character, _, _ := repos.Character.Load(context.Background(), cmd.CharacterID)
		if character.Stats["attendance_marble_clears"] != 0 {
			t.Fatal("无效或失败通关增加了骰子机会")
		}
	}
}
