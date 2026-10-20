package adventuregroup

import (
	"math"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	DailyLoginStateMetadataKey     = "adventure_group_daily_login"
	RecommendedDungeonClearStatKey = "adventure_recommended_dungeon_clears"
	DespairHundredClearStatKey     = "adventure_despair_100_clears"
	GriefHundredClearStatKey       = "adventure_grief_100_clears"
	CollectionTypeCount            = 20
	adventureGroupContentCountMax  = math.MaxUint32
)

const (
	ContentTypeRecommendedDungeon = iota
	ContentTypePVPWin
	ContentTypeDespairHundred
	ContentTypeGriefHundred
	ContentTypeAnton
	ContentTypeLuke
	ContentTypeFiendWar
	ContentTypeCount
)

// 内容统计是七个独立的u32；收藏分类仍是二十个u16，二者不共用字段。
type Projection struct {
	ConsecutiveLoginDays        uint32
	ContentCounts               [ContentTypeCount]uint32
	CollectionCounts            [CollectionTypeCount]uint16
	Runtime                     RuntimeState
	CollectionSelections        map[string]string
	CollectionSourceServerGroup byte
}

// AddContentCount只投影已保存的正数；原生u32显示饱和，不回写玩家数据。
func (p *Projection) AddContentCount(kind int, value int64) {
	if kind < 0 || kind >= ContentTypeCount || value <= 0 {
		return
	}
	p.ContentCounts[kind] = uint32(min(uint64(adventureGroupContentCountMax), uint64(p.ContentCounts[kind])+uint64(value)))
}

// ProjectActivity folds durable character-scoped activity into the
// account-wide values consumed by the current adventure-group packet.
func ProjectActivity(
	consecutiveLoginDays uint32,
	characters []dnfrepo.CharacterRecord,
) Projection {
	result := Projection{ConsecutiveLoginDays: consecutiveLoginDays}
	for _, character := range characters {
		if character.Stats["delete_flag"] != 0 {
			continue
		}
		result.AddContentCount(ContentTypeRecommendedDungeon, character.Stats[RecommendedDungeonClearStatKey])
		result.AddContentCount(ContentTypeDespairHundred, character.Stats[DespairHundredClearStatKey])
		result.AddContentCount(ContentTypeGriefHundred, character.Stats[GriefHundredClearStatKey])
	}
	return result
}
