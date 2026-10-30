package quest

// WorldMapHuntMonsterKind is a server-resolved monster class, not a client
// packet actor byte. Unknown ranks must not count as ordinary monsters.
type WorldMapHuntMonsterKind uint8

const (
	WorldMapHuntMonsterUnknown WorldMapHuntMonsterKind = iota
	WorldMapHuntMonsterOrdinary
	WorldMapHuntMonsterBoss
)

const worldMapHuntProgressKey = "worldmap_hunt_authoritative_v1"

type worldMapHuntTarget struct {
	area, difficulty, selector, count int64
}

// The current registered auto-daily missions use -4 to introduce a worldmap
// five-tuple. Do not confuse it with the fourth field (monster selector), or
// reinterpret malformed region data as a legacy dungeon four-tuple.
func worldMapHuntTargets(values []int64) ([]worldMapHuntTarget, bool) {
	if len(values) == 0 || len(values)%5 != 0 || len(values) > 15 {
		return nil, false
	}
	targets := make([]worldMapHuntTarget, 0, len(values)/5)
	for offset := 0; offset < len(values); offset += 5 {
		t := worldMapHuntTarget{values[offset+1], values[offset+2], values[offset+3], values[offset+4]}
		if values[offset] != -4 || t.area <= 0 || t.difficulty < -1 || t.count <= 0 || t.count > 0x1ff || (t.selector != -1 && t.selector != -3) {
			return nil, false
		}
		targets = append(targets, t)
	}
	return targets, true
}

func (c *Catalog) IsWorldMapHuntQuest(id int64) bool {
	if c == nil {
		return false
	}
	d, ok := c.byID[id]
	if !ok || normalizeQuestTag(d.Type) != "hunt monster" {
		return false
	}
	_, ok = worldMapHuntTargets(d.IntData)
	return ok
}

func worldMapHuntMatchedChannel(values []int64, input HuntMonsterKillInput) (int, bool) {
	targets, ok := worldMapHuntTargets(values)
	if !ok || input.WorldMapAreaID <= 0 {
		return 0, false
	}
	for channel, t := range targets {
		if input.WorldMapAreaID != t.area || (t.difficulty >= 0 && input.Difficulty < t.difficulty) {
			continue
		}
		if t.selector == -1 && input.MonsterKind == WorldMapHuntMonsterOrdinary || t.selector == -3 && input.MonsterKind == WorldMapHuntMonsterBoss {
			return channel, true
		}
	}
	return 0, false
}

// Some indexed dungeons (for example the Bloodline) are absent from the
// worldmap's selection-page list but explicitly belong to a daily mission's
// [dungeon info]. Use that PVF evidence only for single-area daily objectives
// and a missing map association; never override a known different area.
func worldMapHuntFallbackArea(d Definition, input HuntMonsterKillInput) int64 {
	if input.WorldMapAreaID != 0 || normalizeQuestTag(d.Grade) != "auto daily mission" || len(d.DungeonInfo)%2 != 0 {
		return 0
	}
	targets, ok := worldMapHuntTargets(d.IntData)
	if !ok {
		return 0
	}
	area := targets[0].area
	for _, target := range targets {
		if target.area != area {
			return 0
		}
	}
	for n := 0; n+1 < len(d.DungeonInfo); n += 2 {
		if d.DungeonInfo[n] == input.DungeonID && (d.DungeonInfo[n+1] == -1 || d.DungeonInfo[n+1] >= 0 && input.Difficulty >= d.DungeonInfo[n+1]) {
			return area
		}
	}
	return 0
}
