package guild

import r "longheng.io/server/internal/modules/dnf/repository"

const DungeonRepeatRewardProgressStat = "guild_dungeon_repeat_reward_progress"
const DungeonRepeatRewardRevisionStat = "guild_dungeon_repeat_reward_revision"
const DungeonRepeatRewardFractionStat = "guild_dungeon_repeat_reward_fraction"
const DungeonRepeatRewardScale uint32 = 100 // hundredths of a percent

type DungeonRepeatRewardResult struct {
	Enabled  bool
	Awarded  bool
	Progress uint32
	Display  uint32
}

// The longest prerequisite path defines the unlock tier, not the ordinary
// difficulty selector. The ETC supplies the DAG, not these operating rates.
func (c *DungeonCatalog) RepeatRewardRunPercent(id uint32) (uint32, error) {
	if c == nil || c.Rules == nil {
		return 0, ErrDungeonUnlockRules
	}
	if _, ok := c.Rules.byID[id]; !ok {
		return 0, ErrDungeonUnlockRules
	}
	var depth func(uint32) int
	depth = func(id uint32) int {
		n := 0
		for _, parent := range c.Rules.byID[id].requires {
			if d := depth(parent) + 1; d > n {
				n = d
			}
		}
		return n
	}
	tier := depth(id)
	if tier > 3 {
		tier = 3
	}
	return [...]uint32{10, 18, 26, 35}[tier], nil
}

func (c *DungeonCatalog) HasRepeatReward() bool { return c != nil && c.repeatReward != nil }

// Admission is frozen for the run. Personal kills never update purification.
func (c *DungeonCatalog) ValidateRepeatRewardEntry(e DungeonEntry) error {
	p, err := c.RepeatRewardRunPercent(e.DungeonID)
	if err != nil {
		return err
	}
	if e.CatalogDigest != c.digest || e.RepeatRewardPercent != p || e.RunID == "" || len(e.RunID) > 128 || e.GuildID == 0 || e.Owner.CharacterID == "" || e.Owner.AccountID == "" {
		return ErrPermissionDenied
	}
	return nil
}

func DungeonRepeatRewardProgress(character r.CharacterRecord) (uint32, error) {
	value, fraction := character.Stats[DungeonRepeatRewardProgressStat], character.Stats[DungeonRepeatRewardFractionStat]
	if value < 0 || value >= 100 || fraction < 0 || fraction >= int64(DungeonRepeatRewardScale) {
		return 0, ErrInvalidRegistry
	}
	return uint32(value), nil
}

// units is a difference of cumulative rounded run shares, so the whole map
// awards its exact budget. The caller grants items and saves stats atomically.
func ApplyDungeonRepeatReward(character *r.CharacterRecord, catalog *DungeonCatalog, units uint32) ([]DungeonRewardItem, DungeonRepeatRewardResult, error) {
	if catalog == nil || character == nil {
		return nil, DungeonRepeatRewardResult{}, ErrDungeonUnlockRules
	}
	if !catalog.HasRepeatReward() {
		return nil, DungeonRepeatRewardResult{}, nil
	}
	if units > 35*DungeonRepeatRewardScale {
		return nil, DungeonRepeatRewardResult{}, ErrInvalidRegistry
	}
	before, err := DungeonRepeatRewardProgress(*character)
	if err != nil {
		return nil, DungeonRepeatRewardResult{}, err
	}
	total := before*DungeonRepeatRewardScale + uint32(character.Stats[DungeonRepeatRewardFractionStat]) + units
	result := DungeonRepeatRewardResult{Enabled: true, Progress: total / DungeonRepeatRewardScale % 100, Display: total / DungeonRepeatRewardScale % 100}
	var items []DungeonRewardItem
	if total >= 100*DungeonRepeatRewardScale {
		result.Awarded, result.Display = true, 100
		items = []DungeonRewardItem{*catalog.repeatReward}
	}
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	character.Stats[DungeonRepeatRewardProgressStat] = int64(result.Progress)
	character.Stats[DungeonRepeatRewardFractionStat] = int64(total % DungeonRepeatRewardScale)
	return items, result, nil
}
