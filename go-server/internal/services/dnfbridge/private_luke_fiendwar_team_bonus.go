package dnfbridge

import (
	"crypto/sha256"
	"encoding/json"

	"longheng.io/server/internal/modules/dnf/raid"
)

// The private PVF's 10% token branch replaces a squad reward. Complete that
// branch with one native reward from the same effective rank pool, before the
// card is frozen/disclosed. Grant and receipt retries only consume that plan.
func addCurrentPrivateLukeFiendwarNativeReward(f *currentAntonRewardFlow, kind byte, catalog *pvfDungeonDropCatalog, plan currentAntonRewardPlan, roll func(int64) (int64, error)) (currentAntonRewardPlan, error) {
	if f == nil || f.Single || kind != 3 || plan.Choice.Type != "squad_item" {
		return plan, nil
	}
	var tokenID, amount uint32
	switch {
	case f.Mode == 2 && f.Phase == 1:
		tokenID, amount = 990027102, 4000
	case f.Mode == 3 && f.Phase == 0:
		tokenID, amount = 990027103, 5000
	default:
		return plan, nil
	}
	if plan.Choice.ContainerID != tokenID {
		return plan, nil
	}
	// Do not silently accept a changed private box or add another wallet award.
	if len(plan.Rewards) != 1 || plan.Rewards[0].Definition.ItemID != 2681917 || plan.Rewards[0].Count != amount || plan.FateGranted != nil {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	pool, _, err := f.Rewards.Pool("squad_item", f.Rank)
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	// Select the effective rank BEFORE filtering. A rank containing only a
	// token must fail, never fall back to a different/common rank's rewards.
	native := raid.AntonPhaseRewards{}
	found := false
	for _, choice := range pool {
		if choice.ContainerID == tokenID {
			found = found || choice == plan.Choice
			continue
		}
		if choice.ContainerID >= 990027101 && choice.ContainerID <= 990027103 {
			return currentAntonRewardPlan{}, raid.ErrRewardReceipt
		}
		native.Choices = append(native.Choices, choice)
	}
	if !found {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	bonus, err := resolveCurrentAntonRewardPlan(catalog, native, "squad_item", f.Rank, roll, f.CompletedAt)
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	if !currentRaidRewardVouchers(bonus).IsZero() || len(plan.Rewards)+len(bonus.Rewards) > 255 {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	plan = cloneCurrentAntonRewardPlan(plan)
	plan.Rewards = append(plan.Rewards, bonus.Rewards...)
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
