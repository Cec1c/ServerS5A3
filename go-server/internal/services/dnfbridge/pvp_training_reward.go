package dnfbridge

import (
	"context"
	"errors"
	"sort"
	"strconv"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) handleCurrentPVPTrainingReward(session *gameSession, body []byte) error {
	if len(body) != 0 || !currentPVPDojoScene(session) {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return err
	}
	_, items, err := s.currentQuestFinishResources(ctx)
	if err != nil {
		return err
	}
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		return err
	}
	result, err := owner.ApplyPVPTrainingReward(ctx, catalog, session.accountID, strconv.Itoa(int(session.selectedCharacterID)), s.gameplayNow(), currentQuestFinishItemAllocator(items))
	if errors.Is(err, dnfquest.ErrPVPTrainingRewardNotReady) || errors.Is(err, dnfquest.ErrCharacterNotFound) || errors.Is(err, errDungeonPickupInventoryFull) {
		s.logGameEvent(session, "道场秘籍领取拒绝", "quest_id", 2674, "原因", err)
		return nil
	}
	if err != nil {
		return err
	}
	// 发送背包中实际的当前数量；重复请求只补通知，不能重放旧数量或重复加物品。
	var slots []int
	for key, stack := range result.Inventory.Slots {
		if stack.ItemID != result.ItemID || stack.Count <= 0 {
			continue
		}
		list, slot, ok := parseSceneInventorySlotKey(key)
		if ok && list == 0 && slot >= 0 && !repo.IsAccountSharedInventorySlot(list, slot) {
			slots = append(slots, int(slot))
		}
	}
	sort.Ints(slots)
	entries := make([]currentItemListEntry, 0, len(slots))
	for _, slot := range slots {
		key := currentDungeonPickupMainSlotKey(int16(slot))
		entries = append(entries, currentItemListEntryFromStack(0, int16(slot), result.Inventory.Slots[key]))
	}
	s.logGameEvent(session, "道场秘籍物品通知", "quest_id", 2674, "item_id", result.ItemID, "本次发放", result.Granted, "msg_id", 14, "classification", 0)
	return s.sendGameUpperRawClass(session, 14, buildCurrentItemUpdateBody(0, entries), 0)
}
