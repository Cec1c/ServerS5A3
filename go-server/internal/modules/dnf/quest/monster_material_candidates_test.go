package quest

import "testing"

func TestMonsterMaterialCandidatesPreserveWildcardsAndEnemyTypes(t *testing.T) {
	c := &Catalog{byID: map[int64]Definition{1: {MonsterRewardItems: []MonsterRewardItemEntry{{MonsterCode: 10, DungeonID: -1, Difficulty: -1, ItemID: 100, Count: 1, DropRate: 1}}, EnemyRewardItems: []EnemyRewardItemEntry{{EnemyCode: 11, EnemyType: 1, DungeonID: 5, Difficulty: 2, ItemID: 100, Count: 1, DropRate: 100}, {EnemyCode: 12, EnemyType: 2, DungeonID: -1, Difficulty: -1, ItemID: 100, Count: 1, DropRate: 100}, {EnemyCode: 13, EnemyType: 1, DungeonID: -1, Difficulty: -1, ItemID: 100, Count: 1, DropRate: 0}}}}}
	if !c.MayDropMonsterMaterial(5, 2, 999) || !(*Catalog)(nil).MayDropMonsterMaterial(5, 2, 999) {
		t.Fatal("uninitialized catalog cannot prove absence")
	}
	c.buildMonsterMaterialIndex()
	for _, v := range []struct {
		d, diff, m int64
		want       bool
	}{{5, 2, 10, true}, {9, 9, 10, true}, {5, 2, 11, true}, {4, 2, 11, false}, {5, 1, 11, false}, {5, 2, 12, false}, {5, 2, 13, false}, {5, 2, 999, false}} {
		if got := c.MayDropMonsterMaterial(v.d, v.diff, v.m); got != v.want {
			t.Errorf("%+v got%t", v, got)
		}
	}
}
