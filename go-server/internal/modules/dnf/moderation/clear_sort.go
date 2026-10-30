package moderation

import "cmp"

// ClearLess matches repository ordering, including unknown fatigue values last
// in either direction and a stable identity tie-breaker before pagination.
func ClearLess(a, b ClearStats, q Query) bool {
	var n int
	switch q.SortBy {
	case "accountId":
		n = cmp.Compare(a.AccountID, b.AccountID)
	case "characterId":
		n = cmp.Compare(a.CharacterID, b.CharacterID)
	case "characterName":
		n = cmp.Compare(a.CharacterName, b.CharacterName)
	case "dungeonId":
		n = cmp.Compare(a.DungeonID, b.DungeonID)
	case "dungeonName":
		n = cmp.Compare(a.DungeonName, b.DungeonName)
	case "difficulty":
		n = cmp.Compare(a.Difficulty, b.Difficulty)
	case "count":
		n = cmp.Compare(a.Count, b.Count)
	case "lastClearAt":
		n = a.LastClearAt.Compare(b.LastClearAt)
	case "peakHourly":
		n = cmp.Compare(a.PeakHourly, b.PeakHourly)
	case "fastestMs", "averageMs":
		av, bv := a.FastestMS, b.FastestMS
		if q.SortBy == "averageMs" {
			av, bv = a.AverageMS, b.AverageMS
		}
		if (av == 0) != (bv == 0) {
			return av != 0
		}
		n = cmp.Compare(av, bv)
	case "fatigueSpent":
		if (a.FatigueSpent == nil) != (b.FatigueSpent == nil) {
			return a.FatigueSpent != nil
		}
		if a.FatigueSpent != nil {
			n = cmp.Compare(*a.FatigueSpent, *b.FatigueSpent)
		}
	case "averageFatigue":
		if (a.AverageFatigue == nil) != (b.AverageFatigue == nil) {
			return a.AverageFatigue != nil
		}
		if a.AverageFatigue != nil {
			n = cmp.Compare(*a.AverageFatigue, *b.AverageFatigue)
		}
	}
	if n != 0 {
		if q.SortOrder == "desc" {
			return n > 0
		}
		return n < 0
	}
	if a.CharacterID != b.CharacterID {
		return a.CharacterID < b.CharacterID
	}
	if a.DungeonID != b.DungeonID {
		return a.DungeonID < b.DungeonID
	}
	if a.Difficulty != b.Difficulty {
		return a.Difficulty < b.Difficulty
	}
	return a.AccountID < b.AccountID
}
