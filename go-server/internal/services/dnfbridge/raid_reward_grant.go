package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// This is a frozen result, not a bag source item. A configured legacy table
// expands to its awarded contents; ordinary booster rewards remain actual
// unopened boxes, as in the raid reward reference. Invalid tables never fall
// back to awarding their ID. Projection/claim state is owned by the caller.
type currentAntonRewardPlan struct {
	Choice      raid.AntonRewardChoice
	Rewards     []currentBoosterReward
	Fingerprint [32]byte
}

func cloneCurrentAntonRewardPlan(p currentAntonRewardPlan) currentAntonRewardPlan {
	p.Rewards = append([]currentBoosterReward(nil), p.Rewards...)
	for i := range p.Rewards {
		p.Rewards[i].Definition.UsableEquipTypes = append([]string(nil), p.Rewards[i].Definition.UsableEquipTypes...)
		p.Rewards[i].Definition.AvatarOptionChangeGrades = append([]int64(nil), p.Rewards[i].Definition.AvatarOptionChangeGrades...)
	}
	return p
}

func resolveCurrentAntonConfiguredReward(catalog *pvfDungeonDropCatalog, choice raid.AntonRewardChoice, roll func(int64) (int64, error), now time.Time) (currentAntonRewardPlan, error) {
	if catalog == nil || catalog.source == nil || choice.ContainerID == 0 || choice.Weight == 0 || choice.Flags > 1 || now.IsZero() {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	definition, err := catalog.ResolveItem(choice.ContainerID)
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	amounts := map[uint32]uint32{choice.ContainerID: 1}
	var options map[uint32]byte
	if definition.Kind == dungeonDropItemStackable {
		doc, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
		if err != nil {
			return currentAntonRewardPlan{}, err
		}
		typ := normalizeDungeonDropStackableType(magicBoxDocumentText(doc, "stackable type"))
		if typ == "[upgradable legacy]" || typ == "[random upgradable legacy]" {
			box, err := resolveCurrentBoosterDefinition(catalog, choice.ContainerID, currentBoosterRequestRandom)
			if err != nil {
				return currentAntonRewardPlan{}, err
			}
			amounts, options, err = resolveCurrentBoosterRewardAmounts(box, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, roll)
			if err != nil {
				return currentAntonRewardPlan{}, err
			}
		}
	}
	rewards, err := resolveCurrentBoosterRewards(catalog, amounts, options, repo.ItemStack{}, now.UTC())
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	if len(rewards) == 0 || len(rewards) > 255 {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	for _, r := range rewards {
		// Current NOTI600 has a uint16 quantity, so the frozen plan must be
		// representable before either persistence or UI disclosure.
		if r.Count == 0 || r.Count > math.MaxUint16 {
			return currentAntonRewardPlan{}, raid.ErrRewardReceipt
		}
	}
	encoded, err := json.Marshal(struct {
		Choice  raid.AntonRewardChoice
		Rewards []currentBoosterReward
	}{choice, rewards})
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	return currentAntonRewardPlan{Choice: choice, Rewards: rewards, Fingerprint: sha256.Sum256(encoded)}, nil
}

func resolveCurrentAntonRewardPlan(catalog *pvfDungeonDropCatalog, phase raid.AntonPhaseRewards, typ string, rank byte, roll func(int64) (int64, error), now time.Time) (currentAntonRewardPlan, error) {
	_, total, err := phase.Pool(typ, rank)
	if err != nil || roll == nil {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	draw, err := roll(int64(total))
	if err != nil || draw < 0 || draw >= int64(total) {
		return currentAntonRewardPlan{}, raid.ErrRewardReceipt
	}
	choice, err := phase.Select(typ, rank, uint32(draw))
	if err != nil {
		return currentAntonRewardPlan{}, err
	}
	return resolveCurrentAntonConfiguredReward(catalog, choice, roll, now)
}

// Validate every legacy-table leaf before charging entry material. Direct
// booster boxes are validated as actual awarded items, not automatically
// opened. A corrupt legacy table or missing leaf aborts admission.
func validateCurrentAntonRewardDependencies(catalog *pvfDungeonDropCatalog, config raid.AntonRewardConfiguration, now time.Time) error {
	seen := map[uint32]bool{}
	for _, phase := range config.Phases {
		for _, choice := range phase.Choices {
			if seen[choice.ContainerID] {
				continue
			}
			seen[choice.ContainerID] = true
			definition, err := catalog.ResolveItem(choice.ContainerID)
			if err != nil {
				return err
			}
			doc, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
			if err != nil {
				return err
			}
			typ := normalizeDungeonDropStackableType(magicBoxDocumentText(doc, "stackable type"))
			amounts := map[uint32]uint32{choice.ContainerID: 1}
			if definition.Kind == dungeonDropItemStackable && (typ == "[upgradable legacy]" || typ == "[random upgradable legacy]") {
				box, err := resolveCurrentBoosterDefinition(catalog, choice.ContainerID, currentBoosterRequestRandom)
				if err != nil {
					return err
				}
				if _, _, err := resolveCurrentBoosterRewardAmounts(box, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, func(int64) (int64, error) { return 0, nil }); err != nil {
					return err
				}
				amounts = map[uint32]uint32{}
				for _, group := range box.Random.Groups {
					for _, entry := range group.Entries {
						if entry.ItemID <= 0 || entry.ItemID > math.MaxUint32 || entry.Count <= 0 || entry.Count > math.MaxUint16 {
							return raid.ErrRewardReceipt
						}
						amounts[uint32(entry.ItemID)] = uint32(entry.Count)
					}
				}
			}
			if _, err := resolveCurrentBoosterRewards(catalog, amounts, nil, repo.ItemStack{}, now.UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}

type currentAntonRewardGrantResult struct {
	Plan           currentAntonRewardPlan
	Replayed       bool
	GrantSlots     []alignedcmd.ItemSlotRefresh
	AccountChanged bool
}

// Caller must authenticate the online/frozen raid owner, its authoritative
// phase clear, minimum participation and frozen card selection first. The
// dedicated raid domain owner persists placement and receipt together.
func grantCurrentAntonRewardPlan(ctx context.Context, group repo.Group, catalog *pvfDungeonDropCatalog, plan currentAntonRewardPlan, cmd raid.RewardCommand) (currentAntonRewardGrantResult, error) {
	if catalog == nil || catalog.source == nil || len(plan.Rewards) == 0 || plan.Fingerprint == [32]byte{} {
		return currentAntonRewardGrantResult{}, raid.ErrRewardReceipt
	}
	// Verify the plan has not been changed after freezing. No RNG is called
	// here or in the transaction, including failure and duplicate retries.
	encoded, err := json.Marshal(struct {
		Choice  raid.AntonRewardChoice
		Rewards []currentBoosterReward
	}{plan.Choice, plan.Rewards})
	if err != nil || sha256.Sum256(encoded) != plan.Fingerprint {
		return currentAntonRewardGrantResult{}, raid.ErrRewardReceipt
	}
	wantKind := map[string]byte{"party_card": 1, "pcroom_card": 2, "squad_item": 3}[plan.Choice.Type]
	if plan.Choice.Type == "fiendwar_retry" && cmd.Mode == 3 {
		wantKind = 4
	}
	if wantKind == 0 || cmd.Kind != wantKind || cmd.Fingerprint != plan.Fingerprint || cmd.Project != nil {
		return currentAntonRewardGrantResult{}, raid.ErrRewardReceipt
	}
	owner, err := raid.NewRewardOwner(group)
	if err != nil {
		return currentAntonRewardGrantResult{}, err
	}
	result := currentAntonRewardGrantResult{Plan: cloneCurrentAntonRewardPlan(plan)}
	cmd.Project = func(a *raid.RewardAssets) (raid.RewardChanges, error) {
		changes := raid.RewardChanges{}
		writes := map[string]bool{}
		for _, r := range plan.Rewards {
			keys, accountChanged, mailed, err := grantCurrentAntonRewardRow(ctx, a, catalog, r, cmd)
			if err != nil {
				return raid.RewardChanges{}, err
			}
			for _, key := range keys {
				stack := a.Inventory.Slots[key]
				stack = tagCurrentAntonRewardStack(stack, cmd)
				a.Inventory.Slots[key] = stack
				writes[key] = true
			}
			changes.Inventory = changes.Inventory || len(keys) > 0
			changes.Mailbox = changes.Mailbox || mailed
			if accountChanged {
				slot, found := currentDisjointWarehouseFixedSlots[r.Definition.ItemID]
				if !found {
					return raid.RewardChanges{}, fmt.Errorf("raid shared reward placement has no actual slot")
				}
				key := repo.AccountSharedInventorySlotKey(slot)
				a.AccountInventory.Slots[key] = tagCurrentAntonRewardStack(a.AccountInventory.Slots[key], cmd)
				changes.AccountInventory = true
			}
		}
		for key := range writes {
			list, slot, ok := parseSceneInventorySlotKey(key)
			if !ok {
				return raid.RewardChanges{}, raid.ErrRewardReceipt
			}
			result.GrantSlots = append(result.GrantSlots, alignedcmd.ItemSlotRefresh{ListType: list, SlotIndex: slot})
		}
		result.AccountChanged = changes.AccountInventory
		return changes, nil
	}
	var receipt raid.RewardResult
	if cmd.Kind == 4 {
		receipt, err = owner.RefundFiendWarEntry(ctx, cmd)
	} else {
		receipt, err = owner.Grant(ctx, cmd)
	}
	if err != nil {
		return currentAntonRewardGrantResult{}, err
	}
	result.Replayed = receipt.Replayed
	sort.Slice(result.GrantSlots, func(i, j int) bool {
		a, b := result.GrantSlots[i], result.GrantSlots[j]
		if a.ListType != b.ListType {
			return a.ListType < b.ListType
		}
		return a.SlotIndex < b.SlotIndex
	})
	return result, nil
}

func tagCurrentAntonRewardStack(stack repo.ItemStack, cmd raid.RewardCommand) repo.ItemStack {
	if stack.Extra == nil {
		stack.Extra = map[string]string{}
	}
	stack.Extra["source"] = "raid_reward"
	stack.Extra["last_grant_source"] = "raid_reward"
	stack.Extra["raid_instance"] = cmd.InstanceID
	stack.Extra["raid_phase"] = strconv.Itoa(int(cmd.Phase))
	stack.Extra["raid_reward_kind"] = strconv.Itoa(int(cmd.Kind))
	return stack
}
