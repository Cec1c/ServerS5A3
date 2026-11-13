package repository

import "context"

// LevelUpRewardProbe is a read-only preflight, never a full-record save input.
// Delivery must still re-read and validate the event inside its asset transaction.
type LevelUpRewardProbe struct {
	CharacterID string
	AccountID   string
	Level       int
	Pending     bool
}

type LevelUpRewardProbeReader interface {
	LoadLevelUpRewardProbe(context.Context, string) (LevelUpRewardProbe, bool, error)
}

// ProbeLevelUpRewards preserves the full-load path for stores without the
// optional projection and for projection failures. A failed full load remains
// an error, rather than becoming an authoritative absence of pending rewards.
func ProbeLevelUpRewards(ctx context.Context, characters CharacterRepository, id string) (LevelUpRewardProbe, bool, error) {
	if reader, ok := characters.(LevelUpRewardProbeReader); ok {
		probe, found, err := reader.LoadLevelUpRewardProbe(ctx, id)
		if err == nil {
			return probe, found, nil
		}
		if ctx != nil && ctx.Err() != nil {
			return LevelUpRewardProbe{}, false, ctx.Err()
		}
	}
	record, found, err := characters.Load(ctx, id)
	if err != nil || !found {
		return LevelUpRewardProbe{}, found, err
	}
	return LevelUpRewardProbe{CharacterID: record.CharacterID, AccountID: record.AccountID,
		Level: record.Level, Pending: HasPendingLevelUp(record)}, true, nil
}
