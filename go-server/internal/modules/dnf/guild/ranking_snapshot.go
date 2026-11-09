package guild

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type rankingSnapshotCharacters struct {
	r.CharacterRepository
	snapshot r.GuildRankingSnapshot
}

func rankingSnapshotRecord(c r.GuildRankingCharacter) r.CharacterRecord {
	stats := make(map[string]int64, len(c.RaidClears)+2)
	stats["delete_flag"] = c.DeleteFlag
	stats["pvp_grade"] = c.PVPGrade
	for key, value := range c.RaidClears {
		stats[key] = value
	}
	return r.CharacterRecord{CharacterID: c.CharacterID, AccountID: c.AccountID, Slot: c.Slot, Level: c.Level, Stats: stats}
}
func (s *rankingSnapshotCharacters) ListByAccount(ctx context.Context, id string, limit int) ([]r.CharacterRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, ok := s.snapshot.Accounts[id]
	if !ok {
		return nil, ErrCharacterNotFound
	}
	if len(rows) > limit {
		return nil, ErrCharacterNotFound
	}
	result := make([]r.CharacterRecord, 0, len(rows))
	for _, c := range rows {
		result = append(result, rankingSnapshotRecord(c))
	}
	return result, nil
}
func (s *rankingSnapshotCharacters) Load(ctx context.Context, id string) (r.CharacterRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return r.CharacterRecord{}, false, err
	}
	c, ok := s.snapshot.Members[id]
	if !ok {
		return r.CharacterRecord{}, false, nil
	}
	return rankingSnapshotRecord(c), true, nil
}
