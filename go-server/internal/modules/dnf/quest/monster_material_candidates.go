package quest

import "math"

type monsterMaterialScope struct{ dungeon, difficulty int64 }

// Build from the same immutable definitions used for personal drop planning.
// This index contains every quest, including inactive and linked quests.
func (c *Catalog) buildMonsterMaterialIndex() {
	c.monsterMaterialIndex = make(map[int64][]monsterMaterialScope)
	add := func(monster, dungeon, difficulty, item, count, rate int64) {
		if monster > 0 && item > 0 && item <= math.MaxUint32 && count > 0 && rate > 0 {
			c.monsterMaterialIndex[monster] = append(c.monsterMaterialIndex[monster], monsterMaterialScope{dungeon, difficulty})
		}
	}
	for _, d := range c.byID {
		for _, v := range d.MonsterRewardItems {
			add(v.MonsterCode, v.DungeonID, v.Difficulty, v.ItemID, v.Count, v.DropRate)
		}
		for _, v := range d.EnemyRewardItems {
			if v.EnemyType == 1 {
				add(v.EnemyCode, v.DungeonID, v.Difficulty, v.ItemID, v.Count, v.DropRate)
			}
		}
	}
}

// MayDropMonsterMaterial is a static negative proof, never a character-state
// cache. A missing/uninitialized catalog must keep the original transaction.
func (c *Catalog) MayDropMonsterMaterial(dungeon, difficulty, monster int64) bool {
	if c == nil || c.monsterMaterialIndex == nil {
		return true
	}
	for _, v := range c.monsterMaterialIndex[monster] {
		if (v.dungeon == -1 || v.dungeon == dungeon) && (v.difficulty < 0 || v.difficulty == difficulty) {
			return true
		}
	}
	return false
}
