package dnfbridge

import repo "longheng.io/server/internal/modules/dnf/repository"

// 顺序也是PSA2的字段顺序；其他地下城包括无法单凭副本编号区分的普通深渊。
var playerActivityEpicSourceKeys = [...]string{"epic_road", "demon_rift", "time_rift", "other_dungeons"}

func playerActivityEpicSourceKey(dungeonID int64) string {
	switch dungeonID {
	case 6062, 6063, 6064, 6065:
		return "epic_road"
	case 316:
		return "demon_rift"
	case 7539:
		return "time_rift"
	default:
		return "other_dungeons"
	}
}

func playerActivityEpicSources(summary repo.ActivitySummary, characterID string) map[string]int64 {
	values := make(map[string]int64, len(playerActivityEpicSourceKeys))
	for _, key := range playerActivityEpicSourceKeys {
		values[key] = 0
	}
	for _, row := range summary.EpicSources {
		if characterID == "" || row.CharacterID == characterID {
			values[playerActivityEpicSourceKey(row.DungeonID)] += row.Amount
		}
	}
	return values
}
