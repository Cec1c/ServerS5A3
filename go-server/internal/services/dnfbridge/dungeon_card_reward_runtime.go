package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var currentDungeonCardGoldDifficultyBonus = [...]float64{1.02, 1.38, 1.60, 1.90, 2.0}

const currentDungeonGoldCardItemMultiplier int64 = 2

func (s *Service) freezeCurrentDungeonCardRewardStateLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
) error {
	if s == nil || session == nil || runtime == nil || session.dungeon.runtime != runtime {
		return errDungeonCardAssetOwnerUnavailable
	}
	if currentDungeonUsesDedicatedTowerResult(runtime) {
		return nil
	}
	if runtime.settlementCardRewardState != nil {
		return nil
	}
	monsterCatalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return err
	}
	goldReferences, err := currentDungeonGoldReferences(monsterCatalog)
	if err != nil {
		return err
	}
	if !runtime.Dungeon.Metadata.BasisLevel.Set || runtime.Dungeon.Metadata.BasisLevel.Value <= 0 {
		return errCurrentDungeonSettlementPVFInvalid
	}
	dungeonLevel := int(runtime.Dungeon.Metadata.BasisLevel.Value)
	var paidCardCost int64
	// Tournament rewards use the frozen free gold through their dedicated
	// settlement flow and do not offer ordinary paid cards.
	if !dungeonTournamentEnabled() || runtime.Tournament == nil {
		paidCardCost, err = currentDungeonPaidCardCost(monsterCatalog.source, int64(dungeonLevel))
		if err != nil {
			return err
		}
	}
	goldReference, found := goldReferences[int64(dungeonLevel)]
	if !found {
		return fmt.Errorf("%w: dungeon_level=%d", errCurrentDungeonGoldReferenceInvalid, runtime.Dungeon.Metadata.BasisLevel.Value)
	}
	itemCatalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		return err
	}
	itemDropDocument, err := parseDungeonCardPVFDocument(monsterCatalog.source, dungeonCardDropRulePath)
	if err != nil {
		return err
	}
	itemDropReferences, err := parseDungeonCardPVFItemDropReferences(itemDropDocument)
	if err != nil {
		return err
	}
	itemDropReference, found := itemDropReferences[int64(dungeonLevel)]
	if !found {
		return fmt.Errorf("%w: dungeon_level=%d", errDungeonCardPVFSectionMissing, dungeonLevel)
	}

	dimensionCatalog, err := currentDungeonDimensionDrops(monsterCatalog)
	if err != nil {
		return err
	}
	_, dimensionChronicleActive := currentDungeonDimensionTier(dimensionCatalog, runtime.Dungeon.ID, runtime.Character.Level)
	dimensionChronicleActive = dimensionChronicleActive && len(dimensionCatalog.ChronicleByCareer) > 0
	now := s.gameplayNow().UTC()
	randomCardItem := func(rollRNG *currentDungeonDropLCG, owner uint16) (dungeonCardItemReward, bool, error) {
		if !dimensionChronicleActive {
			return currentDungeonRandomCardItem(rollRNG, itemCatalog, dungeonLevel, itemDropReference, now)
		}
		character := runtime.Character
		if owner != session.selectedCharacterID {
			var loadErr error
			character, loadErr = s.loadDimensionDropCharacter(owner)
			if loadErr != nil {
				return dungeonCardItemReward{}, false, loadErr
			}
		}
		job, parseErr := strconv.ParseUint(character.Job, 10, 8)
		if parseErr != nil {
			return dungeonCardItemReward{}, false, fmt.Errorf("异界翻牌角色职业无效：%q", character.Job)
		}
		return currentDungeonRandomDimensionCardItem(rollRNG, itemCatalog, dimensionCatalog, runtime.Dungeon.ID, character.Level, byte(job), byte(character.Stats["grow_type"])&0x0f, now)
	}
	rng := newCurrentDungeonDropLCG(runtime.Seed)
	var visit *runtimeDungeonRoomVisit
	if runtime.Session != nil {
		if scene, ok := runtime.Session.Scene(); ok {
			if ownedVisit, visitErr := runtime.currentDungeonRoomVisit(scene); visitErr == nil {
				visit = ownedVisit
				rng = newCurrentDungeonDropLCG(ownedVisit.DropRNG)
			}
		}
	}
	freeGold := currentDungeonCardGoldAmount(rng, goldReference, int(runtime.Request.Difficulty))
	freeItem, freeItemFound, err := randomCardItem(rng, session.selectedCharacterID)
	if err != nil {
		return err
	}
	paidGold := currentDungeonCardGoldAmount(rng, goldReference, int(runtime.Request.Difficulty))
	paidItem, paidItemFound, err := randomCardItem(rng, session.selectedCharacterID)
	if err != nil {
		return err
	}
	free := dungeonCardRewardBundle{
		Gold: freeGold,
	}
	if freeItemFound {
		free.Items = []dungeonCardItemReward{freeItem}
	} else {
		free.Gold += 100
	}
	paid := dungeonCardRewardBundle{
		Gold: paidGold,
		Cost: paidCardCost,
	}
	if paidItemFound {
		paid.Items = []dungeonCardItemReward{paidItem}
	} else {
		paid.Gold += 100
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	_, account, character, premiumErr := s.currentPremiumServiceRecords(ctx, session)
	cancel()
	var blackDiamond dungeonCardRewardBundle
	if runtime.partyInstance != nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		err := s.freezePartyBlackDiamondCards(session, runtime, func(owner uint16) (dungeonCardRewardBundle, error) {
			if !s.partyMemberBlackDiamondActive(ctx, owner) {
				return dungeonCardRewardBundle{}, nil
			}
			// A separate per-owner stream freezes peers without reading or mutating
			// their room RNG, so order of op46 requests cannot change the card.
			bonusRNG := newCurrentDungeonDropLCG(runtime.Seed ^ (uint32(owner) * 0x9E3779B9))
			bundle := dungeonCardRewardBundle{Gold: currentDungeonCardGoldAmount(bonusRNG, goldReference, int(runtime.Request.Difficulty))}
			item, found, err := randomCardItem(bonusRNG, owner)
			if err != nil {
				return dungeonCardRewardBundle{}, err
			}
			if found {
				bundle.Items = []dungeonCardItemReward{item}
			} else {
				bundle.Gold += 100
			}
			return bundle, nil
		})
		cancel()
		if err != nil {
			return err
		}
		blackDiamond = partyBlackDiamondCardForOwner(runtime, session.selectedCharacterID)
	} else if premiumErr == nil && premium.Active(account, premium.TypeBlackDiamond, now) {
		blackDiamond.Gold = currentDungeonCardGoldAmount(rng, goldReference, int(runtime.Request.Difficulty))
		item, found, rollErr := randomCardItem(rng, session.selectedCharacterID)
		if rollErr != nil {
			return rollErr
		}
		if found {
			blackDiamond.Items = []dungeonCardItemReward{item}
		} else {
			blackDiamond.Gold += 100
		}
	}
	if premiumErr == nil &&
		premium.Active(account, premium.DevilSlotType(premium.DevilSlotGoldCard), now) &&
		premium.DailyUsage(character, premium.DevilSlotGoldCard, now) < premium.DailyLimit(premium.DevilSlotGoldCard) {
		// 黄金卡牌契约只减免费用并消耗每日次数，不改变本局奖励数量。
		paid.Cost = 0
		paid.ConsumePremiumDaily = true
		paid.PremiumDailySlot = premium.DevilSlotGoldCard
	}
	if visit != nil {
		visit.DropRNG = rng.Seed()
	}
	plan, err := newDungeonCardRewardPlan(
		dungeonCardPlanIdentity{
			CharacterID: strconv.Itoa(int(session.selectedCharacterID)),
			DungeonID:   runtime.Dungeon.ID,
			MazeIndex:   runtime.MazeIndex,
			RunSeed:     runtime.Seed,
		},
		"current_pvf_clear_reward_generator_free_and_paid_rows",
		free,
		paid,
		blackDiamond,
	)
	if err != nil {
		return err
	}
	state, err := newDungeonCardState(plan)
	if err != nil {
		return err
	}
	if premiumErr == nil {
		state.premiumServiceBody = buildCurrentPremiumServiceDataBody(account, character, now)
	}
	runtime.settlementCardRewardState = state
	s.logGameEvent(session, "game-dungeon-card-reward-plan-frozen",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"plan_id", plan.ID,
		"free_gold", free.Gold,
		"free_item_count", len(free.Items),
		"paid_gold", paid.Gold,
		"paid_card_cost", paid.Cost,
		"paid_item_count", len(paid.Items),
		"premium_gold_card_available", paid.ConsumePremiumDaily,
		"source", plan.Source,
		"domain_source", "server_s4a12_clear_reward_generator_with_current_pvf_item_pools")
	return nil
}

// 双倍翻牌的独立道具生成工具，保留给后续活动接入；当前无活动资格调用，
// 魔王契约免费翻牌不调用此工具。可堆叠物品增加数量，装备生成独立实例。
func applyCurrentDungeonGoldCardItemMultiplier(bundle *dungeonCardRewardBundle) error {
	if bundle == nil || len(bundle.Items) == 0 {
		return nil
	}
	items := make([]dungeonCardItemReward, 0, len(bundle.Items)*int(currentDungeonGoldCardItemMultiplier))
	for _, item := range bundle.Items {
		if item.ItemID <= 0 || item.Count <= 0 ||
			item.Count > math.MaxInt64/currentDungeonGoldCardItemMultiplier {
			return fmt.Errorf(
				"%w: gold-card item=%d count=%d",
				errDungeonCardPlanInvalid,
				item.ItemID,
				item.Count,
			)
		}
		if item.Stackable {
			item.Count *= currentDungeonGoldCardItemMultiplier
			items = append(items, cloneDungeonCardItemReward(item))
			continue
		}
		if item.Count != 1 {
			return fmt.Errorf(
				"%w: gold-card equipment=%d count=%d",
				errDungeonCardPlanInvalid,
				item.ItemID,
				item.Count,
			)
		}
		for range currentDungeonGoldCardItemMultiplier {
			items = append(items, cloneDungeonCardItemReward(item))
		}
	}
	bundle.Items = items
	return nil
}

func currentDungeonRandomCardItem(
	rng *currentDungeonDropLCG,
	catalog *pvfDungeonDropCatalog,
	dungeonLevel int,
	reference dungeonCardPVFItemDropReference,
	now time.Time,
) (dungeonCardItemReward, bool, error) {
	if rng == nil || catalog == nil || dungeonLevel <= 0 {
		return dungeonCardItemReward{}, false, errDungeonCardPVFSourceRequired
	}
	rarityRoll := rng.Next(1_000_000) + 1
	rarity := int64(0)
	switch {
	case rarityRoll <= 500_000:
		rarity = 0
	case rarityRoll <= 799_900:
		rarity = 1
	case rarityRoll <= 999_900:
		rarity = 2
	default:
		rarity = 3
	}
	itemID, found, err := catalog.selectClearCardEquipment(
		rng,
		dungeonLevel,
		reference.ValueA,
		reference.ValueB,
		rarity,
	)
	if err != nil {
		return dungeonCardItemReward{}, false, err
	}

	if !found || itemID == 0 {
		return dungeonCardItemReward{}, false, nil
	}
	item, err := currentDungeonCardItemReward(rng, catalog, itemID, now)
	return item, err == nil, err
}

// 指定物品与普通翻牌共用实例初始化，保留封装、品级、耐久和放置策略。
func currentDungeonCardItemReward(rng *currentDungeonDropLCG, catalog *pvfDungeonDropCatalog, itemID uint32, now time.Time) (dungeonCardItemReward, error) {
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		return dungeonCardItemReward{}, err
	}
	definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
	if err != nil {
		return dungeonCardItemReward{}, err
	}
	extra := map[string]string{
		"source":    "dungeon_card_reward",
		"item_kind": string(definition.Kind),
		"pvf_path":  definition.PVFPath,
	}
	if definition.StackableType != "" {
		extra["stackable_type"] = definition.StackableType
	}
	if definition.StackLimit > 0 {
		extra["stack_limit"] = strconv.FormatInt(definition.StackLimit, 10)
	}
	if definition.EquipmentType != "" {
		extra["equipment_type"] = definition.EquipmentType
	}
	if definition.Durability > 0 {
		extra["durability"] = strconv.FormatUint(uint64(definition.Durability), 10)
		extra["max_durability"] = strconv.FormatUint(uint64(definition.Durability), 10)
	}
	if definition.Kind == dungeonDropItemEquipment {
		qualitySeed := rng.NextUint32()%currentEquipmentRandomQualitySeedCount + 1
		extra["quality_seed"] = strconv.FormatUint(uint64(qualitySeed), 10)
	}
	if currentNewPVFEquipmentSealFlag(definition) != 0 {
		extra["seal_flag"] = "1"
	}
	policy := currentPVFItemPlacementPolicy(definition)
	return dungeonCardItemReward{
		ItemID:           int64(itemID),
		Count:            1,
		Stackable:        definition.Kind == dungeonDropItemStackable,
		SlotStart:        definition.SlotStart,
		SlotEnd:          definition.SlotEnd,
		ExpireAt:         definition.ExpirationDate,
		Extra:            extra,
		AllowQuickSlots:  policy.AllowQuickSlots,
		PreferQuickSlots: policy.PreferQuickSlots,
		StackLimit:       definition.StackLimit,
	}, nil
}

func currentDungeonCardGoldAmount(
	rng *currentDungeonDropLCG,
	reference currentDungeonGoldReference,
	difficulty int,
) int64 {
	if rng == nil || reference.Base <= 0 {
		return 1
	}
	goldBase := reference.Base * 175 / 1000
	if goldBase < 1 {
		goldBase = 1
	}
	diffBonus := 1.0
	if difficulty >= 0 && difficulty < len(currentDungeonCardGoldDifficultyBonus) {
		diffBonus = currentDungeonCardGoldDifficultyBonus[difficulty]
	}
	amount := int64(math.Floor(float64(goldBase) * diffBonus))
	if amount < 1 {
		amount = 1
	}
	if reference.VariancePct > 0 {
		rangeSize := reference.VariancePct*2 + 1
		variance := (int64(rng.Next(int(rangeSize))) - reference.VariancePct) * amount / 100
		amount += variance
	}
	if amount < 1 {
		return 1
	}
	return amount
}

func (s *Service) selectCurrentDungeonCardLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	side dungeonCardSide,
	memberIndex byte,
	source string,
) error {
	if session == nil || runtime == nil || session.dungeon.runtime != runtime ||
		currentDungeonUsesDedicatedTowerResult(runtime) ||
		!runtime.settlementCardLayoutSent ||
		runtime.settlementPhase < currentDungeonSettlementPhaseCardsRevealed ||
		side >= dungeonCardSideCount || memberIndex >= dungeonCardSlotsPerSide {
		return nil
	}
	if side == dungeonCardSidePaid && runtime.settlementPaidCardGrant != nil && !runtime.settlementCardSideRewardCommitted[side] {
		// 队伍占位可能把原请求分配到另一张牌。首个成功应答写失败时，
		// 客户端只知道原请求；续传仍使用已收费的同一牌位。
		memberIndex = runtime.settlementCardSideMember[side]
	}
	if runtime.settlementCardSideSelectionKnown[side] &&
		runtime.settlementCardSideMember[side] != memberIndex {
		s.logGameEvent(session, "game-dungeon-card-select-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"side", side,
			"member_index", memberIndex,
			"accepted_member_index", runtime.settlementCardSideMember[side],
			"source", source,
			"reason", "row_selection_conflicts_with_first_accepted_member")
		return nil
	}
	if runtime.settlementCardRewardState == nil {
		if err := s.freezeCurrentDungeonCardRewardStateLocked(session, runtime); err != nil {
			s.logGameEvent(session, "game-dungeon-card-reward-plan-blocked",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"side", side,
				"member_index", memberIndex,
				"source", source,
				"error", err)
			return nil
		}
	}
	if !runtime.settlementCardSideSelectionKnown[side] {
		accepted, ok := reserveCurrentDungeonPartyCard(runtime.partyInstance, side, memberIndex, session.selectedCharacterID)
		if !ok {
			s.logGameEvent(session, "game-dungeon-card-select-blocked", "char_id", session.selectedCharacterID,
				"side", side, "card_index", memberIndex, "reason", "party_card_row_fully_reserved")
			return nil
		}
		if accepted != memberIndex {
			s.logGameEvent(session, "game-dungeon-card-select-reassigned", "char_id", session.selectedCharacterID,
				"side", side, "requested_card_index", memberIndex, "card_index", accepted, "source", source)
		}
		memberIndex = accepted
	}
	replay := runtime.settlementCardSideSelectionSent[side]
	runtime.settlementCardSideSelectionKnown[side] = true
	runtime.settlementCardSideMember[side] = memberIndex
	runtime.settlementCardSelected = byte(side)
	runtime.refreshCurrentDungeonFreeCardReadiness()
	if side == dungeonCardSidePaid {
		if _, _, _, err := s.grantCurrentDungeonSelectedCardAssetsLocked(session, runtime, side, memberIndex); err != nil {
			s.logGameEvent(session, "付费翻牌收费或发奖失败，恢复未选择牌面",
				"char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID,
				"member_index", memberIndex, "source", source, "error", err)
			return s.rejectCurrentDungeonPaidCardSelectionLocked(session, runtime, memberIndex)
		}
	}
	// 行状态字节 = **翻牌人的队伍槽位**（修复档；回退档恒 0 = 改动前）。
	// 客户端/厂商的 op71 语义是 `slots[牌位] = 队伍槽位`，写 0 会把队友翻的牌
	// 算到收包者自己头上（现场 trace 实证），见 dungeon_party_card_flip_row_model.go。
	flipPartySlot := currentDungeonCardFlipRowStateValue(session)
	slots71, rewardTupleCount := currentDungeonSelectedCardSlots(runtime, flipPartySlot)
	body71, err := buildCurrentDungeonOp71SuccessBody(slots71)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSelectCard), body71, currentDungeonCardResponseClass); err != nil {
		return err
	}
	// 队友投影（操作者缺陷「双方都看不到对方翻的牌」）：记进实例账本并把账本投影发给
	// 同一实例内的其它成员。回退档/单人逐字节不发。见 dungeon_party_card_flip_projection.go。
	localFlip := slots71[memberIndex]
	s.publishCurrentDungeonCardFlipToPartyLocked(
		session,
		runtime,
		side,
		memberIndex,
		localFlip.StateA,
		localFlip.StateB,
		localFlip.Rewards,
		source,
	)
	runtime.settlementCardSideSelectionSent[side] = true
	runtime.refreshCurrentDungeonFreeCardReadiness()
	if side == dungeonCardSideFree {
		s.cancelCurrentDungeonCardAutoFlipLocked(session, runtime, "free_row_selected")
	}
	s.logGameEvent(session, "game-dungeon-card-select-ack-sent",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"side", side,
		"member_index", memberIndex,
		"card_index", memberIndex,
		"flip_party_slot", flipPartySlot,
		"row_model", dungeonPartyCardFlipRowModelFromEnv().String(),
		"row_model_env", dungeonPartyCardFlipRowModelEnv,
		// 这一个牌位实际带出去的奖励块（客户端就是用它画牌面）：
		// `slot_reward_tuple_count=0` = 空牌面（运维截图里的蓝色 X）。
		"slot_state_a", localFlip.StateA,
		"slot_state_b", localFlip.StateB,
		"slot_reward_tuple_count", len(localFlip.Rewards),
		"slot_reward_tuples", currentDungeonOp71RewardTuplesForLog(localFlip.Rewards),
		"free_row_reward_mode", dungeonPartyCardFreeRowRewardMode(),
		"free_row_reward_env", dungeonPartyCardFreeRowRewardEnv,
		"replay", replay,
		"response_body_len", len(body71),
		"classification", currentDungeonCardResponseClass,
		"source", source,
		"reward_tuple_count", rewardTupleCount,
		"body_source", "current_exe_op71_proved_reader_selection_state_with_frozen_card_reward_context")

	if err := s.commitCurrentDungeonCardSideRewardLocked(session, runtime, side, source); err != nil {
		s.logGameEvent(session, "game-dungeon-card-reward-delivery-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"side", side,
			"member_index", memberIndex,
			"source", source,
			"error", err)
	}
	return nil
}

func (s *Service) commitCurrentDungeonCardRewardLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	source string,
) error {
	if runtime == nil || runtime.settlementCardRewardCommitted {
		return nil
	}
	return s.commitCurrentDungeonCardSideRewardLocked(session, runtime, dungeonCardSideFree, source)
}

func (s *Service) commitCurrentDungeonCardSideRewardLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	side dungeonCardSide,
	source string,
) error {
	if runtime == nil || side >= dungeonCardSideCount {
		return nil
	}
	if side == dungeonCardSideFree && session != nil {
		// op574 can open a native quest-completion page. The pre-settlement
		// snapshot is consumed before the score/card UI replaces that page;
		// re-project the still-active story quest after the free card commits.
		// This is also reached on a card replay, without granting its reward twice.
		defer func() {
			if err := s.sendCurrentDungeonStoryQuestAfterFreeCardLocked(session, runtime); err != nil {
				s.logGameEvent(session, "game-dungeon-story-quest-after-card-snapshot-deferred",
					"source", source, "char_id", session.selectedCharacterID,
					"dungeon_id", runtime.Dungeon.ID, "error", err)
			}
		}()
	}
	if runtime.settlementCardSideRewardCommitted[side] {
		return nil
	}
	if !runtime.settlementCardSideSelectionKnown[side] ||
		!runtime.settlementCardSideSelectionSent[side] {
		return errDungeonCardSelectionInvalid
	}
	if err := s.deliverCurrentDungeonSelectedCardRewardLocked(
		session,
		runtime,
		side,
		runtime.settlementCardSideMember[side],
		source,
	); err != nil {
		return err
	}
	runtime.settlementCardSideRewardCommitted[side] = true
	runtime.refreshCurrentDungeonFreeCardReadiness()
	if side == dungeonCardSideFree {
		runtime.advanceSettlementPhase(currentDungeonSettlementPhaseRewardCommitted)
	}
	s.logGameEvent(session, "game-dungeon-card-reward-phase-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"side", side,
		"member_index", runtime.settlementCardSideMember[side],
		"source", source,
		"next_stage", "await_current_exe_c2s_op72_or_completed_op42")
	return nil
}

func (s *Service) deliverCurrentDungeonSelectedCardRewardLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	side dungeonCardSide,
	memberIndex byte,
	source string,
) error {
	if s == nil || session == nil || runtime == nil || runtime.settlementCardRewardState == nil {
		return nil
	}
	grant, result, available, err := s.grantCurrentDungeonSelectedCardAssetsLocked(session, runtime, side, memberIndex)
	if err != nil {
		return err
	}
	if !available {
		return nil
	}
	projectionGold := grant.GoldAfter
	if side == dungeonCardSidePaid {
		// 缓存回执只用于防重复收费。续传前可能已发免费牌或发生其它
		// 金币变更，钱包通知必须投影当前余额，不能重新发送旧回执余额。
		repositories, ok := s.repositoryGroup()
		if !ok || repositories.Character == nil {
			return errDungeonCardAssetOwnerUnavailable
		}
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		character, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
		cancel()
		if err != nil {
			return err
		}
		if !found {
			return errDungeonCardAssetOwnerUnavailable
		}
		projectionGold = character.Stats["gold"]
	}
	if grant.GoldAfter != grant.GoldBefore {
		body := buildCurrentGoldStateBody(projectionGold)
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0); err != nil {
			return err
		}
	}
	refreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(grant.ItemSlots))
	for _, slot := range grant.ItemSlots {
		if slot < 0 {
			continue
		}
		refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{
			ListType:  dnfrepo.MainInventoryListType,
			SlotIndex: slot,
		})
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(
		session,
		"dungeon-card-reward",
		refreshes,
	); err != nil {
		return err
	}
	if side == dungeonCardSidePaid && runtime.settlementCardRewardState.plan.Sides[side].ConsumePremiumDaily {
		// 次数随奖励事务提交，翻牌前冻结的明细仍是旧值。提交后读取当前完整状态；
		// 写失败保留本局资产回执及未完成通知游标，续传不再次扣次或发奖。
		if err := s.sendCurrentPremiumServiceState(session, "黄金卡牌免费翻牌提交后_刷新契约次数"); err != nil {
			return err
		}
	}
	if grant.OverflowMailID != "" {
		if err := s.sendMailboxAlarmToOnlineRecipient(session.selectedCharacterID); err != nil {
			// Reward and its mailbox fallback have already committed. A packet
			// delivery failure must not turn that durable result into a retryable
			// card selection; op96 will project it on the next mailbox open.
			s.logWarn("dungeon reward mailbox alarm deferred to next mailbox open",
				"character_id", session.selectedCharacterID,
				"mail_id", grant.OverflowMailID,
				"error", err)
		}
	}
	s.logGameEvent(session, "game-dungeon-card-reward-delivered",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"selection_result", result,
		"side", side,
		"member_index", memberIndex,
		"paid_card_cost", grant.Cost,
		"gold_before", grant.GoldBefore,
		"gold_after", grant.GoldAfter,
		"gold_projection_current", projectionGold,
		"item_slots", grant.ItemSlots,
		"overflow_mail_id", grant.OverflowMailID,
		"source", source,
		"domain_source", "86jp_auto_flip_reward_delivery")
	return nil
}

func (runtime *runtimeDungeonState) refreshCurrentDungeonFreeCardReadiness() {
	if runtime == nil {
		return
	}
	runtime.settlementCardSelectionKnown = runtime.settlementCardSideSelectionKnown[dungeonCardSideFree]
	runtime.settlementCardSelectionSent = runtime.settlementCardSideSelectionSent[dungeonCardSideFree]
	runtime.settlementCardRewardCommitted = runtime.settlementCardSideRewardCommitted[dungeonCardSideFree]
}
