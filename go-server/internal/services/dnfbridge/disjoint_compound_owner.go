package dnfbridge

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	dnfdisjoint "longheng.io/server/internal/modules/dnf/disjoint"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentDisjointMutationOwner(session *gameSession) (string, string, *dnfdisjoint.Owner, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return "", "", nil, errCurrentDisjointUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.AccountAssets == nil {
		return "", "", nil, errCurrentDisjointUnavailable
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return "", "", nil, errCurrentDisjointUnavailable
	}
	owner, err := dnfdisjoint.NewOwner(repositories)
	if err != nil {
		return "", "", nil, errCurrentDisjointUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	return accountID, characterID, owner, nil
}

func currentDisjointMutationError(err error) error {
	switch {
	case errors.Is(err, dnfdisjoint.ErrOwnerUnavailable),
		errors.Is(err, dnfdisjoint.ErrAccountRequired),
		errors.Is(err, dnfdisjoint.ErrCharacterRequired),
		errors.Is(err, dnfdisjoint.ErrCharacterNotFound),
		errors.Is(err, dnfdisjoint.ErrAccountMismatch),
		errors.Is(err, dnfdisjoint.ErrInventoryNotFound):
		return errors.Join(errCurrentDisjointUnavailable, err)
	default:
		return err
	}
}

// currentDisjointItemPlan 是一件已校验、尚未消耗的来源与其 PVF 产物。
type currentDisjointItemPlan struct {
	Slot    int16
	Rewards []currentDisjointReward
}

// currentPlanDisjointItemSource 做**单件体完全相同**的一份来源校验：槽位存在、
// ItemID 合法、数量恰好 1（时装允许线路上的 0）、未被 NPC 商店锁定、PVF 定义可解析
// 且未过期、PVF 文档可读、resolve 返回非空且不超过上限。它不做任何写入，因此批量
// 可以先把每一件都校验通过、再开始消耗（任一件不合法 ⇒ 整批零消耗）。
//
// 单件（commitCurrentDisjoint）与批量（commitCurrentDisjointBatch）都走这一个函数，
// 校验顺序与失败原因逐字一致。
func currentPlanDisjointItemSource(
	catalog *pvfDungeonDropCatalog,
	inventory *dnfrepo.InventoryRecord,
	listType byte,
	sourceSlot int16,
	resolve func(dungeonDropItemDefinition, *dnfpvf.Document) ([]currentDisjointReward, error),
) (currentDisjointItemPlan, error) {
	plan := currentDisjointItemPlan{Slot: sourceSlot}
	if catalog == nil || inventory == nil || resolve == nil {
		return plan, errCurrentDisjointUnavailable
	}
	key := currentCeraShopInventorySlotKey(listType, sourceSlot)
	source, found := inventory.Slots[key]
	validCount := source.Count == 1
	if listType == currentAvatarInventoryListType {
		// Durable avatar rows may keep zero in the wire count field
		// while the slot still owns one non-stackable instance.
		validCount = source.Count == 0 || source.Count == 1
	}
	if !found || source.ItemID <= 0 || source.ItemID > math.MaxUint32 || !validCount || currentNPCShopItemLocked(source) {
		return plan, errCurrentDisjointSourceInvalid
	}
	definition, err := catalog.ResolveItem(uint32(source.ItemID))
	if err != nil || (!definition.ExpirationDate.IsZero() && !time.Now().UTC().Before(definition.ExpirationDate)) {
		return plan, errCurrentDisjointSourceInvalid
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		return plan, errors.Join(errCurrentDisjointSourceInvalid, err)
	}
	rewards, err := resolve(definition, document)
	if err != nil {
		return plan, err
	}
	if len(rewards) == 0 || len(rewards) > currentDisjointMaxRewards {
		return plan, errCurrentDisjointRewardInvalid
	}
	plan.Rewards = rewards
	return plan, nil
}

// commitCurrentDisjoint consumes one equipment instance and grants its
// resolved PVF rows through the domain-owned account+character transaction.
func (s *Service) commitCurrentDisjoint(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	sourceSlot int16,
	listType byte,
	resolve func(dungeonDropItemDefinition, *dnfpvf.Document) ([]currentDisjointReward, error),
) (currentDisjointResult, error) {
	if session == nil || session.selectedCharacterID == 0 || catalog == nil || catalog.source == nil || resolve == nil {
		return currentDisjointResult{}, errCurrentDisjointUnavailable
	}
	accountID, characterID, owner, err := s.currentDisjointMutationOwner(session)
	if err != nil {
		return currentDisjointResult{}, err
	}

	var result currentDisjointResult
	command := dnfdisjoint.Command{
		AccountID:   accountID,
		CharacterID: characterID,
		Project: func(assets *dnfdisjoint.Assets) (dnfdisjoint.Changes, error) {
			inventory := assets.Inventory
			account := assets.AccountInventory
			plan, err := currentPlanDisjointItemSource(catalog, inventory, listType, sourceSlot, resolve)
			if err != nil {
				return dnfdisjoint.Changes{}, err
			}
			delete(inventory.Slots, currentCeraShopInventorySlotKey(listType, plan.Slot))
			rewardSlots, accountDirty, err := currentGrantDisjointRewards(inventory, account, catalog, plan.Rewards)
			if err != nil {
				return dnfdisjoint.Changes{}, err
			}
			result.Rewards = rewardSlots
			return dnfdisjoint.Changes{AccountInventory: accountDirty, Inventory: true}, nil
		},
	}
	switch listType {
	case 0:
		err = owner.DisjointEquipment(ctx, command)
	case currentAvatarInventoryListType:
		err = owner.DisjointAvatar(ctx, command)
	default:
		err = errCurrentDisjointRequestInvalid
	}
	if err != nil {
		return currentDisjointResult{}, currentDisjointMutationError(err)
	}
	return result, nil
}

// commitCurrentDisjointBatch 在**一个**账号+角色资产事务里分解整批装备：
//
//	阶段一（只读）：把请求里的每个槽位都按 currentPlanDisjointItemSource 校验一遍，
//	               任一件不合法立刻返回带槽位的错误 ⇒ 事务不提交、零消耗、零发放；
//	阶段二（写入）：逐件删除来源槽并按既有 currentGrantDisjointRewards 落位产物，
//	               产物按落点合并堆叠（无色小晶块/灵魂进账号固定槽，元素结晶并进材料段）。
//
// 阶段二若仍失败（例如目标段真的放不下 → errDungeonPickupInventoryFull），同样返回
// 错误、整个事务丢弃，绝不出现"前几件成功、后几件失败"的部分成交。
func (s *Service) commitCurrentDisjointBatch(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	slots []int16,
	resolve func(dungeonDropItemDefinition, *dnfpvf.Document) ([]currentDisjointReward, error),
) (currentDisjointBatchResult, error) {
	if session == nil || session.selectedCharacterID == 0 || catalog == nil || catalog.source == nil ||
		resolve == nil || len(slots) == 0 {
		return currentDisjointBatchResult{}, errCurrentDisjointUnavailable
	}
	accountID, characterID, owner, err := s.currentDisjointMutationOwner(session)
	if err != nil {
		return currentDisjointBatchResult{}, err
	}

	var result currentDisjointBatchResult
	command := dnfdisjoint.Command{
		AccountID:   accountID,
		CharacterID: characterID,
		Project: func(assets *dnfdisjoint.Assets) (dnfdisjoint.Changes, error) {
			inventory := assets.Inventory
			account := assets.AccountInventory
			plans := make([]currentDisjointItemPlan, 0, len(slots))
			for _, slot := range slots {
				plan, err := currentPlanDisjointItemSource(catalog, inventory, dnfrepo.MainInventoryListType, slot, resolve)
				if err != nil {
					return dnfdisjoint.Changes{}, currentDisjointSlotError{Slot: slot, Err: err}
				}
				plans = append(plans, plan)
			}
			items := make([]currentDisjointBatchItem, 0, len(plans))
			accountDirty := false
			for _, plan := range plans {
				delete(inventory.Slots, currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, plan.Slot))
				rewardSlots, dirty, err := currentGrantDisjointRewards(inventory, account, catalog, plan.Rewards)
				if err != nil {
					return dnfdisjoint.Changes{}, currentDisjointSlotError{Slot: plan.Slot, Err: err}
				}
				if dirty {
					accountDirty = true
				}
				items = append(items, currentDisjointBatchItem{Slot: plan.Slot, Rewards: rewardSlots})
			}
			result.Items = items
			return dnfdisjoint.Changes{AccountInventory: accountDirty, Inventory: true}, nil
		},
	}
	if err := owner.DisjointEquipment(ctx, command); err != nil {
		return currentDisjointBatchResult{}, currentDisjointMutationError(err)
	}
	return result, nil
}
