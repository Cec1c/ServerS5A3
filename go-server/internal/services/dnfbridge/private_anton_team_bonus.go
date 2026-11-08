package dnfbridge

import (
	"crypto/sha256"
	"encoding/json"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// The extra draw belongs to the team kill-phase card, not the shared material
// containers (the soul container is also used by single mode). Freeze it before
// disclosure and persistence so retries cannot reroll or grant it twice.
func addCurrentAntonTeamTokenBonus(f *currentAntonRewardFlow, kind byte, catalog *pvfDungeonDropCatalog, plan currentAntonRewardPlan, roll func(int64) (int64, error)) (currentAntonRewardPlan, error) {
	if f == nil || f.Mode != 1 || f.Single || f.Phase != 1 || kind != 3 || plan.Choice.Type != "squad_item" ||
		(plan.Choice.ContainerID != 10304480 && plan.Choice.ContainerID != 10157783) {
		return plan, nil
	}
	if roll == nil {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	draw, err := roll(100)
	if err != nil || draw < 0 || draw >= 100 {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	if draw >= 30 {
		return plan, nil
	}
	bonus, err := resolveCurrentBoosterRewards(catalog, map[uint32]uint32{2681917: 2000}, nil, repo.ItemStack{}, f.CompletedAt)
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	plan = cloneCurrentAntonRewardPlan(plan)
	plan.Rewards = append(plan.Rewards, bonus...)
	if len(plan.Rewards) > 255 {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	encoded, err := json.Marshal(struct {
		Choice  raid.AntonRewardChoice
		Rewards []currentBoosterReward
	}{plan.Choice, plan.Rewards})
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	plan.Fingerprint = sha256.Sum256(encoded)
	return plan, nil
}
