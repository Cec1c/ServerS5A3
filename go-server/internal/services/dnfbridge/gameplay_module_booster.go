package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfbooster "longheng.io/server/internal/modules/dnf/booster"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func boosterGameplayModule() gameplayModuleDefinition {
	opcode := uint16(dnfenum.CmdPacketUseBoosterItem)
	lotteryOpcode := uint16(dnfenum.CmdPacketUseLotteryItem)
	overflowOpcode := uint16(dnfenum.CmdPacketOverflowInfo)
	handler := func(service *Service, session *gameSession, request gameplayRequest) error {
		return service.handleCurrentBoosterItem(session, request.Body)
	}
	return gameplayModuleDefinition{
		Name: "booster-item",
		LegacyHandlers: map[uint16]gameplayHandler{
			opcode: handler,
			lotteryOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentLotteryItem(session, request.Body)
			},
			overflowOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentLotteryOverflowConfirm(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			opcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(service, session, request, "game-current-booster-blocked", "current_exe_op160_command_class_mismatch") {
					return nil
				}
				return handler(service, session, request)
			},
			lotteryOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(service, session, request, "game-lottery-item-blocked", "current_exe_op27_command_class_mismatch") {
					return nil
				}
				return service.handleCurrentLotteryItem(session, request.Body)
			},
		},
	}
}

func (s *Service) handleCurrentBoosterItem(session *gameSession, body []byte) error {
	request, err := parseCurrentBoosterOpenRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-current-booster-rejected", "body_len", len(body), "reason", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketUseBoosterItem), s.currentBoosterFailureCode(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		s.logGameEvent(session, "game-current-booster-rejected", "source_slot", request.SourceSlot, "reason", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketUseBoosterItem), s.currentBoosterFailureCode(err))
	}
	definition, err := s.prepareCurrentBooster(ctx, session, catalog, request)
	if err != nil {
		s.logGameEvent(session, "game-current-booster-rejected",
			"source_slot", request.SourceSlot,
			"selected_item", request.SelectedItemID,
			"selected_count", len(request.Selections),
			"request_kind", request.Kind,
			"reason", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketUseBoosterItem), s.currentBoosterFailureCode(err))
	}
	result, err := s.commitCurrentBooster(ctx, session, catalog, definition, request)
	if err != nil {
		s.logGameEvent(session, "game-current-booster-rejected",
			"source_slot", request.SourceSlot,
			"source_item", definition.Source.ItemID,
			"selected_item", request.SelectedItemID,
			"selected_count", len(request.Selections),
			"request_kind", request.Kind,
			"reason", err)
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketUseBoosterItem), s.currentBoosterFailureCode(err))
	}
	if err := s.sendGameUpperSuccess(
		session,
		uint16(dnfenum.CmdPacketUseBoosterItem),
		buildCurrentBoosterSuccessBody(result),
	); err != nil {
		return err
	}
	if result.SourceSlotReused {
		var removed currentItemListEntry
		removed.patchCore(result.SourceSlot, math.MaxUint32, 0)
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember),
			buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, []currentItemListEntry{removed}), 0); err != nil {
			return err
		}
		s.logGameEvent(session, "game-booster-consumed-source-object-removed",
			"source_slot", result.SourceSlot, "source_item", result.SourceItemID,
			"reason", "committed_reward_reuses_consumed_box_slot")
	}
	// 形状开关（默认 delta = 修复档）：只对**本次真正写过的 (list, slot)** 发
	// class0/msg14 逐槽增量，奖励容器不再回放整容器 —— 客户端的分页高亮读的就是这条
	// 逐槽行（见 item_grant_notice.go）。list 38 例外仍走整容器；list 7 的写入进
	// Delta 后由发送侧改期成 1500 ms 整容器。写 containers 一键回退到今天的线格式。
	grantContainerLists := result.ChangedLists
	// A paid jar also changes the character wallet, which is projected only
	// by the full main-container refresh, not an inventory-slot delta.
	if itemGrantNoticeUsesDelta() && definition.LotteryGoldCost == 0 {
		plan := buildItemGrantNoticePlan(result.GrantSlots)
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "use_booster_item", plan.Delta); err != nil {
			return err
		}
		grantContainerLists = plan.ContainerLists
		s.logPacketEvent("game-item-grant-notice-planned",
			"conn_id", session.connID,
			"operation", "use_booster_item",
			"char_id", session.selectedCharacterID,
			"mode", itemGrantNoticeDelta,
			"delta_slots", currentItemSlotRefreshesSummary(plan.Delta),
			"container_lists", plan.ContainerLists,
			"legacy_changed_lists", result.ChangedLists)
	} else if err := s.sendSelectedIncrementalItemSlotRefreshes(
		session,
		"use_booster_item",
		[]alignedcmd.ItemSlotRefresh{{
			ListType:  dnfrepo.MainInventoryListType,
			SlotIndex: result.SourceSlot,
		}},
	); err != nil {
		return err
	}
	for _, listType := range grantContainerLists {
		listBody, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, session, listType)
		if !ok {
			return errCurrentBoosterOwnerUnavailable
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), listBody); err != nil {
			return err
		}
	}
	if err := s.sendCurrentEpicPieceCredits(session, result.EpicPieceCredits, "use_booster_item"); err != nil {
		return err
	}
	if err := s.sendAlignedPremiumContractActivations(session, "use_booster_item", result.ContractActivations); err != nil {
		return err
	}
	if definition.ItemUsage != nil {
		if err := s.sendCurrentItemUsageForSession(session, "use_booster_item"); err != nil {
			return err
		}
	}
	s.logGameEvent(session, "game-current-booster-committed",
		"source_slot", result.SourceSlot,
		"source_item", result.SourceItemID,
		"source_remaining", result.SourceRemaining,
		"request_kind", request.Kind,
		"selected_item", request.SelectedItemID,
		"selected_count", len(request.Selections),
		"reward_count", len(result.Rewards),
		"rewards", fmt.Sprint(result.Rewards),
		"changed_lists", fmt.Sprint(result.ChangedLists),
		"container_lists", fmt.Sprint(grantContainerLists),
		"item_grant_notice", itemGrantNoticeMode(),
		"ack_body", "current_exe_op160_u32_source_u16_slot_u32_remaining_u32_mode_u16_rewards",
		"inventory_refresh", "current_exe_op14_source_slot_then_op13_real_repository_lists")
	return nil
}

func (s *Service) commitCurrentBooster(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	definition currentBoosterDefinition,
	request currentBoosterOpenRequest,
) (currentBoosterCommitResult, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || catalog == nil || catalog.source == nil || definition.Source.ItemID == 0 {
		return currentBoosterCommitResult{}, errCurrentBoosterOwnerUnavailable
	}
	rewardAmounts, rewardOptions, err := resolveCurrentBoosterRewardAmounts(definition, request, secureCurrentBoosterRoll)
	if err != nil {
		return currentBoosterCommitResult{}, err
	}
	if request.RewardMultiplier > 1 {
		for itemID, count := range rewardAmounts {
			multiplied := uint64(count) * uint64(request.RewardMultiplier)
			if multiplied > currentBoosterRewardUnitLimit(itemID) {
				return currentBoosterCommitResult{}, errCurrentBoosterPVFInvalid
			}
			rewardAmounts[itemID] = uint32(multiplied)
		}
	}
	goldAmount := rewardAmounts[0]
	if goldAmount > 0 && (!request.AllowLotteryGold || definition.StackableType != "[upgradable legacy]" || s.options.currentClientVersion != "official-24.9") {
		return currentBoosterCommitResult{}, errCurrentBoosterPVFInvalid
	}
	delete(rewardAmounts, 0)
	contracts := s.currentPremiumRewardContracts()
	contractRewards := make(map[int64]premium.InventoryContract)
	for itemID := range rewardAmounts {
		if contract, ok := contracts[int64(itemID)]; ok {
			contractRewards[int64(itemID)] = contract
		}
	}
	// op27的奖励展示要求真实背包实例；该不支持的组合在扣道具前拒绝。
	if request.AllowLotteryGold && len(contractRewards) > 0 {
		return currentBoosterCommitResult{}, fmt.Errorf("%w: lottery contract reward needs native projection", errCurrentBoosterPVFInvalid)
	}
	accountID, characterID, owner, err := s.currentBoosterMutationOwner(session)
	if err != nil {
		return currentBoosterCommitResult{}, err
	}
	// 只认现用PVF图鉴目录，普通材料保持原奖励投影。
	pieceRewards := make(map[uint32]struct{})
	if s.epicPieceGrantEnabled() {
		for itemID := range rewardAmounts {
			if _, ok := epicPieceCreditTarget(s, itemID); ok {
				pieceRewards[itemID] = struct{}{}
			}
		}
	}
	now := s.gameplayNow()
	result := currentBoosterCommitResult{
		SourceItemID: definition.Source.ItemID,
		SourceSlot:   request.SourceSlot,
	}
	err = owner.Open(ctx, dnfbooster.Command{
		ItemUsage:     definition.ItemUsage,
		AccountLedger: len(pieceRewards) > 0 || len(contractRewards) > 0,
		GoldCost:      definition.LotteryGoldCost,
		AccountID:     accountID,
		CharacterID:   characterID,
		UpdatedAt:     now,
		Project: func(assets *dnfbooster.Assets) (dnfbooster.Changes, error) {
			inventory := assets.Inventory
			account := assets.AccountInventory
			sourceKey := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, request.SourceSlot)
			sourceStack, found := inventory.Slots[sourceKey]
			if !found || sourceStack.ItemID != int64(definition.Source.ItemID) || sourceStack.Count <= 0 || sourceStack.Count > math.MaxUint32 {
				return dnfbooster.Changes{}, errCurrentBoosterSourceMissing
			}
			if !definition.Source.ExpirationDate.IsZero() {
				sourceStack, _ = applyCurrentPVFItemExpirationAt(sourceStack, definition.Source, now)
			}
			if err := validateCurrentBoosterSourceExpiration(sourceStack, definition.Source, now); err != nil {
				return dnfbooster.Changes{}, err
			}
			characterDirty := false
			if goldAmount > 0 {
				after, err := dnfbooster.CreditGold(assets.Character, goldAmount, definition.GoldMinimumLevel)
				if err != nil {
					return dnfbooster.Changes{}, err
				}
				result.GoldAmount, result.GoldAfter = goldAmount, after
				characterDirty = true
			}
			if request.ConsumePremiumDaily {
				if assets.Character == nil || !premium.TryConsumeDaily(assets.Character, request.PremiumDailySlot, now) {
					return dnfbooster.Changes{}, fmt.Errorf("premium slot %d daily limit reached", request.PremiumDailySlot)
				}
				characterDirty = true
			}
			rewards, err := resolveCurrentBoosterRewards(catalog, rewardAmounts, rewardOptions, sourceStack, now)
			if err != nil {
				return dnfbooster.Changes{}, err
			}

			materialKey := ""
			var materialItemID int64
			var materialCount int64
			if request.Kind == currentBoosterRequestRandom {
				materialItemID = definition.Random.MaterialItemID
				materialCount = definition.Random.MaterialCountPerUse
			}
			if materialItemID > 0 && materialCount > 0 {
				materialKey = findCurrentBoosterMaterial(inventory.Slots, materialItemID, materialCount, sourceKey)
				if materialKey == "" {
					return dnfbooster.Changes{}, fmt.Errorf("%w: material item=%d count=%d", errCurrentBoosterSourceMissing, materialItemID, materialCount)
				}
			}

			result.SourceRemaining = uint32(sourceStack.Count - 1)
			changedLists := map[byte]struct{}{dnfrepo.MainInventoryListType: {}}
			if sourceStack.Count == 1 {
				delete(inventory.Slots, sourceKey)
			} else {
				sourceStack.Count--
				entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, request.SourceSlot, sourceStack)
				sourceStack.RawEntry = append([]byte(nil), entry.data[:]...)
				inventory.Slots[sourceKey] = sourceStack
			}
			if materialKey != "" {
				materialStack := inventory.Slots[materialKey]
				materialStack.Count -= materialCount
				if materialStack.Count <= 0 {
					delete(inventory.Slots, materialKey)
				} else {
					listType, slot, parsed := parseSceneInventorySlotKey(materialKey)
					if !parsed || listType != dnfrepo.MainInventoryListType {
						return dnfbooster.Changes{}, errCurrentBoosterPVFInvalid
					}
					entry := currentItemListEntryFromStack(listType, slot, materialStack)
					materialStack.RawEntry = append([]byte(nil), entry.data[:]...)
					inventory.Slots[materialKey] = materialStack
				}
			}

			changedRewardKeys := make(map[string]struct{})
			accountDirty := false
			for _, reward := range rewards {
				if contract, ok := contractRewards[int64(reward.Definition.ItemID)]; ok {
					activation, err := premium.ActivateContractGrant(assets.Account, contract, int64(reward.Count), now)
					if err != nil {
						return dnfbooster.Changes{}, err
					}
					result.ContractActivations = append(result.ContractActivations, alignedcmd.PremiumContractActivation{ItemID: activation.ItemID, PremiumType: activation.PremiumType, Units: activation.Units, ExpireAt: activation.ExpireAt})
					result.Rewards = append(result.Rewards, currentBoosterGrantedReward{ItemID: reward.Definition.ItemID, Count: reward.Count})
					continue
				}
				if _, piece := pieceRewards[reward.Definition.ItemID]; piece {
					balance, err := dnfbooster.CreditEpicPiece(assets, reward.Definition.ItemID, reward.Count, now)
					if err != nil {
						return dnfbooster.Changes{}, err
					}
					result.EpicPieceCredits = append(result.EpicPieceCredits, alignedcmd.MailboxEpicPieceCredit{PieceID: reward.Definition.ItemID, Count: int64(reward.Count), Balance: balance})
					result.Rewards = append(result.Rewards, currentBoosterGrantedReward{ItemID: reward.Definition.ItemID, Count: reward.Count})
					continue
				}
				keys, sharedChanged, grantErr := grantCurrentBoosterRewardByOwner(inventory, account, catalog.source, reward)
				if grantErr != nil {
					return dnfbooster.Changes{}, grantErr
				}
				accountDirty = accountDirty || sharedChanged
				for _, key := range keys {
					changedRewardKeys[key] = struct{}{}
				}
				result.Rewards = append(result.Rewards, currentBoosterGrantedReward{ItemID: reward.Definition.ItemID, Count: reward.Count})
			}
			for key := range changedRewardKeys {
				stack, found := inventory.Slots[key]
				if !found || stack.ItemID <= 0 {
					return dnfbooster.Changes{}, errCurrentBoosterPVFInvalid
				}
				if stack.Extra == nil {
					stack.Extra = make(map[string]string, 6)
				}
				stack.Extra["source"] = "booster_item"
				stack.Extra["last_grant_source"] = "booster_item"
				stack.Extra["booster_source_item"] = strconv.FormatUint(uint64(definition.Source.ItemID), 10)
				listType, slot, parsed := parseSceneInventorySlotKey(key)
				if !parsed {
					return dnfbooster.Changes{}, errCurrentBoosterPVFInvalid
				}
				entry := currentItemListEntryFromStack(listType, slot, stack)
				stack.RawEntry = append([]byte(nil), entry.data[:]...)
				inventory.Slots[key] = stack
				changedLists[listType] = struct{}{}
			}
			// delta 档的发放通知集合 = 源格 + 材料格 + 每个奖励落点。源格即使被删掉也必须通知
			// （发送侧从仓储重读，空槽投影成 0xFFFFFFFF 空行），材料格同理。
			grantSlots := make(map[string]struct{}, len(changedRewardKeys)+2)
			grantSlots[sourceKey] = struct{}{}
			if materialKey != "" {
				grantSlots[materialKey] = struct{}{}
			}
			for key := range changedRewardKeys {
				grantSlots[key] = struct{}{}
			}
			result.GrantSlots = make([]alignedcmd.ItemSlotRefresh, 0, len(grantSlots))
			if replacement, present := inventory.Slots[sourceKey]; result.SourceRemaining == 0 && present {
				result.SourceSlotReused = replacement.ItemID != int64(result.SourceItemID)
			}
			for key := range grantSlots {
				listType, slot, parsed := parseSceneInventorySlotKey(key)
				if !parsed {
					return dnfbooster.Changes{}, errCurrentBoosterPVFInvalid
				}
				result.GrantSlots = append(result.GrantSlots, alignedcmd.ItemSlotRefresh{
					ListType:  listType,
					SlotIndex: slot,
				})
			}
			result.ChangedLists = make([]byte, 0, len(changedLists))
			for listType := range changedLists {
				result.ChangedLists = append(result.ChangedLists, listType)
			}
			sort.Slice(result.ChangedLists, func(i, j int) bool { return result.ChangedLists[i] < result.ChangedLists[j] })
			return dnfbooster.Changes{Account: len(result.EpicPieceCredits) > 0 || len(result.ContractActivations) > 0, AccountInventory: accountDirty, Character: characterDirty, Inventory: true}, nil
		},
	})
	if err != nil {
		return currentBoosterCommitResult{}, currentBoosterMutationError(err)
	}
	return result, nil
}

func (s *Service) currentBoosterMutationOwner(session *gameSession) (string, string, *dnfbooster.Owner, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return "", "", nil, errCurrentBoosterOwnerUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.AccountAssets == nil {
		return "", "", nil, errCurrentBoosterOwnerUnavailable
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return "", "", nil, errCurrentBoosterOwnerUnavailable
	}
	owner, err := dnfbooster.NewOwner(repositories)
	if err != nil {
		return "", "", nil, errCurrentBoosterOwnerUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	return accountID, characterID, owner, nil
}

func currentBoosterMutationError(err error) error {
	switch {
	case errors.Is(err, dnfbooster.ErrOwnerUnavailable),
		errors.Is(err, dnfbooster.ErrAccountRequired),
		errors.Is(err, dnfbooster.ErrCharacterRequired),
		errors.Is(err, dnfbooster.ErrCharacterNotFound),
		errors.Is(err, dnfbooster.ErrAccountMismatch),
		errors.Is(err, dnfbooster.ErrInventoryNotFound):
		return errors.Join(errCurrentBoosterOwnerUnavailable, err)
	default:
		return err
	}
}

// prepareCurrentBooster is a read-only fast-fail pass. The domain Owner
// repeats authorization and inventory validation inside the commit
// transaction before consuming anything.
func (s *Service) prepareCurrentBooster(
	ctx context.Context,
	session *gameSession,
	catalog *pvfDungeonDropCatalog,
	request currentBoosterOpenRequest,
) (currentBoosterDefinition, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || request.SourceSlot < 0 || catalog == nil || catalog.source == nil {
		return currentBoosterDefinition{}, errCurrentBoosterOwnerUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Inventory == nil {
		return currentBoosterDefinition{}, errCurrentBoosterOwnerUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return currentBoosterDefinition{}, err
	}
	if !found || character.CharacterID != characterID || strings.TrimSpace(character.AccountID) != strings.TrimSpace(s.accountIDForSession(session)) {
		return currentBoosterDefinition{}, errCurrentBoosterOwnerUnavailable
	}
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		return currentBoosterDefinition{}, err
	}
	if !found || inventory.CharacterID != characterID {
		return currentBoosterDefinition{}, errCurrentBoosterOwnerUnavailable
	}
	stack, found := inventory.Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, request.SourceSlot)]
	if !found || stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 || stack.Count <= 0 || stack.Count > math.MaxUint32 {
		return currentBoosterDefinition{}, errCurrentBoosterSourceMissing
	}
	definition, err := resolveCurrentBoosterDefinition(catalog, uint32(stack.ItemID), request.Kind)
	if err != nil {
		return currentBoosterDefinition{}, err
	}
	if err := validateCurrentBoosterSourceExpiration(stack, definition.Source, time.Now().UTC()); err != nil {
		return currentBoosterDefinition{}, err
	}
	if request.Kind == currentBoosterRequestSelection {
		if _, err := currentBoosterSelectedCandidates(definition, request); err != nil {
			return currentBoosterDefinition{}, err
		}
	}
	return definition, nil
}
