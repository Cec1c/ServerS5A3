package guild

import (
	r "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
)

func TestDungeonRepeatRewardCatalogAndBalance(t *testing.T) {
	base := dungeonUnlockFixture()
	for _, tail := range []string{"0 1", "123 0", "123 -1", "123", "123 1 2", "123 `one`", "2147483648 1", "123 1\n[repeat clear bonus reward]\n124 1"} {
		if _, err := ParseDungeonCatalog(base + "\n[repeat clear bonus reward]\n" + tail); err == nil {
			t.Fatalf("accepted %q", tail)
		}
	}
	if _, err := ParseDungeonCatalog(strings.Replace(base, "[dungeon index]", "[repeat clear bonus reward]\n123 1\n[dungeon index]", 1)); err == nil {
		t.Fatal("nested reward accepted")
	}
	c, err := ParseDungeonCatalog(base + "\n[repeat clear bonus reward]\n10156320 1\n")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uint32]uint32{8500: 10, 8504: 18, 8507: 18, 8505: 26, 8503: 26, 8508: 35} {
		got, err := c.RepeatRewardRunPercent(id)
		if err != nil || got != want {
			t.Fatal(id, got, want, err)
		}
	}
	ch := r.CharacterRecord{Stats: map[string]int64{DungeonRepeatRewardProgressStat: 99, DungeonRepeatRewardFractionStat: 90}}
	items, result, err := ApplyDungeonRepeatReward(&ch, c, 35)
	if err != nil || len(items) != 1 || !result.Awarded || result.Display != 100 || result.Progress != 0 || ch.Stats[DungeonRepeatRewardFractionStat] != 25 {
		t.Fatal(items, result, ch, err)
	}
	for _, units := range []uint32{0, 33, 33, 34} {
		if _, _, err := ApplyDungeonRepeatReward(&ch, c, units); err != nil {
			t.Fatal(err)
		}
	}
	if ch.Stats[DungeonRepeatRewardProgressStat] != 1 || ch.Stats[DungeonRepeatRewardFractionStat] != 25 {
		t.Fatal(ch)
	}
	for _, bad := range []int64{-1, 100, 1 << 32} {
		ch.Stats[DungeonRepeatRewardProgressStat] = bad
		if _, err := DungeonRepeatRewardProgress(ch); err == nil {
			t.Fatal(bad)
		}
	}
	ch.Stats[DungeonRepeatRewardProgressStat] = 1
	for _, bad := range []int64{-1, 100} {
		ch.Stats[DungeonRepeatRewardFractionStat] = bad
		if _, err := DungeonRepeatRewardProgress(ch); err == nil {
			t.Fatal(bad)
		}
	}
}
