package guild

import r "longheng.io/server/internal/modules/dnf/repository"

const DungeonRepeatRewardProgressStat = "guild_dungeon_repeat_reward_progress"

// This is an explicit operating default, not a value inferred from PVF.
const DefaultDungeonRepeatRewardPercent uint32 = 10

type DungeonRepeatRewardResult struct {
	Enabled  bool
	Awarded  bool
	Progress uint32 // Committed balance, 0..99.
	Display  uint32 // This clear shows 100 when the reward is granted.
}

func (p *DungeonPolicy) repeatRewardPercent() (uint32, error) {
	if p == nil || p.RepeatRewardPercent == 0 {
		return DefaultDungeonRepeatRewardPercent, nil
	}
	if p.RepeatRewardPercent > 100 {
		return 0, ErrInvalidRegistry
	}
	return p.RepeatRewardPercent, nil
}

func DungeonRepeatRewardProgress(character r.CharacterRecord) (uint32, error) {
	value := character.Stats[DungeonRepeatRewardProgressStat]
	if value < 0 || value >= 100 {
		return 0, ErrInvalidRegistry
	}
	return uint32(value), nil
}

// Called only inside the existing per-character guild settlement transaction,
// after admission revalidation and the permanent completion receipt check.
// The caller must grant the returned item in that same transaction.
func ApplyDungeonRepeatReward(character *r.CharacterRecord, catalog *DungeonCatalog, percent uint32) ([]DungeonRewardItem, DungeonRepeatRewardResult, error) {
	if catalog == nil || character == nil {
		return nil, DungeonRepeatRewardResult{}, ErrDungeonUnlockRules
	}
	if catalog.repeatReward == nil {
		return nil, DungeonRepeatRewardResult{}, nil
	}
	if percent == 0 || percent > 100 {
		return nil, DungeonRepeatRewardResult{}, ErrInvalidRegistry
	}
	before, err := DungeonRepeatRewardProgress(*character)
	if err != nil {
		return nil, DungeonRepeatRewardResult{}, err
	}
	total := before + percent
	result := DungeonRepeatRewardResult{Enabled: true, Progress: total % 100, Display: total}
	var items []DungeonRewardItem
	if total >= 100 {
		result.Awarded = true
		result.Display = 100
		items = []DungeonRewardItem{*catalog.repeatReward}
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats[DungeonRepeatRewardProgressStat] = int64(result.Progress)
	return items, result, nil
}
