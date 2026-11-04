package guild

import (
	"reflect"
	"strings"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestDungeonRepeatRewardCatalogAndBalance(t *testing.T) {
	base := dungeonUnlockFixture()
	for _, tail := range []string{"0 1", "123 0", "123 -1", "123", "123 1 2", "123 `one`", "2147483648 1", "123 1\n[repeat clear bonus reward]\n124 1"} {
		if _, err := ParseDungeonCatalog(base + "\n[repeat clear bonus reward]\n" + tail); err == nil {
			t.Fatalf("accepted %q", tail)
		}
	}
	if _, err := ParseDungeonCatalog(strings.Replace(base, "[dungeon index]", "[repeat clear bonus reward]\n123 1\n[dungeon index]", 1)); err == nil {
		t.Fatal("nested repeat reward accepted")
	}
	catalog, err := ParseDungeonCatalog(base + "\n[repeat clear bonus reward]\n10156320 1\n")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range catalog.ItemIDs() {
		found = found || id == 10156320
	}
	if !found {
		t.Fatal("box definition not preloaded")
	}
	c := r.CharacterRecord{Stats: map[string]int64{DungeonRepeatRewardProgressStat: 90}}
	items, result, err := ApplyDungeonRepeatReward(&c, catalog, 25)
	if err != nil || !reflect.DeepEqual(items, []DungeonRewardItem{{10156320, 1}}) || result.Progress != 15 || result.Display != 100 || !result.Awarded {
		t.Fatal(items, result, err)
	}
	plain := dungeonLifecycleCatalog(t)
	before := c.Stats[DungeonRepeatRewardProgressStat]
	items, result, err = ApplyDungeonRepeatReward(&c, plain, 10)
	if err != nil || len(items) != 0 || result.Enabled || c.Stats[DungeonRepeatRewardProgressStat] != before {
		t.Fatal("catalog without repeat reward changed progress")
	}
	for _, invalid := range []int64{-1, 100, 1 << 32} {
		c.Stats[DungeonRepeatRewardProgressStat] = invalid
		if _, _, err := ApplyDungeonRepeatReward(&c, catalog, 10); err == nil {
			t.Fatal("invalid balance accepted", invalid)
		}
	}
	for _, invalid := range []uint32{0, 101} {
		if _, _, err := ApplyDungeonRepeatReward(&c, catalog, invalid); err == nil {
			t.Fatal("invalid percent accepted", invalid)
		}
	}
	if _, err := (&DungeonPolicy{RepeatRewardPercent: 101}).repeatRewardPercent(); err == nil {
		t.Fatal("invalid policy")
	}
}
