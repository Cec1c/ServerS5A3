package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	"longheng.io/server/internal/modules/dnf/guild"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	errCurrentDungeonSettlementOwnerUnavailable = dnfdungeon.ErrOwnerUnavailable
	errCurrentDungeonSettlementStateInvalid     = dnfdungeon.ErrSettlementStateInvalid
	errCurrentDungeonSettlementPVFInvalid       = errors.New("current dungeon settlement PVF reward source is invalid")
)

const (
	// Type 29 / item 193 are auction-discount tickets, not Black Diamond.
	currentDungeonBlackDiamondPremiumType int64 = premium.TypeBlackDiamond
	currentDungeonBlackDiamondBonusRate         = 0.10
)

type currentDungeonSettlementRewardResources struct {
	Progression       *progression.Tables
	ExperienceSources *progression.MonsterExperienceSources
	ClearRankCatalog  currentDungeonClearRankCatalog
}

type currentDungeonSettlementCommitResult struct {
	GuildDungeonItems []dnfdungeon.GuildDungeonGrantedItem
	DespairItems      []dnfdungeon.DespairGrantedItem
	GriefItems        []dnfdungeon.GriefGrantedItem
	Character         dnfrepo.CharacterRecord
	Skill             dnfrepo.SkillRecord
	Inventory         dnfrepo.InventoryRecord
	ExperienceGain    uint32
	PotionBonus       uint32
	HonorExpertGain   uint32
	SPGain            int
	TPGain            int
	Replayed          bool
}

// produceCurrentDungeonSettlementPlanLocked is the missing owner between the
// accepted current-EXE op46 and the frozen op34 -> op37 -> op35 send plan.
// The caller holds session.dungeon.mu, so consumed-drop receipts cannot change
// while the database snapshot and packet plan are frozen.
func (s *Service) produceCurrentDungeonSettlementPlanLocked(
	ctx context.Context,
	session *gameSession,
	runtime *runtimeDungeonState,
) error {
	if session == nil || runtime == nil || runtime.Session == nil || session.dungeon.runtime != runtime ||
		!runtime.settlementPlayResultReceived {
		return errCurrentDungeonSettlementOwnerUnavailable
	}
	if runtime.clearMapCompletionKey == "" {
		if err := ensureCurrentDungeonCompletionReceiptKey(
			session,
			runtime,
			runtime.Session.Snapshot(),
			s.gameplayNow(),
			"settlement_plan_without_matching_clear_map_quest",
		); err != nil {
			return err
		}
	}
	if runtime.settlementResultPlan != nil {
		return nil
	}
	resources, err := s.currentDungeonSettlementRewardResources(ctx)
	if err != nil {
		return err
	}
	return s.produceCurrentDungeonSettlementPlanWithResourcesLocked(ctx, session, runtime, resources)
}

func (s *Service) produceCurrentDungeonSettlementPlanWithResourcesLocked(
	ctx context.Context,
	session *gameSession,
	runtime *runtimeDungeonState,
	resources currentDungeonSettlementRewardResources,
) error {
	if session == nil || runtime == nil || runtime.Session == nil || session.dungeon.runtime != runtime ||
		!runtime.settlementPlayResultReceived {
		return errCurrentDungeonSettlementOwnerUnavailable
	}
	if runtime.clearMapCompletionKey == "" {
		if err := ensureCurrentDungeonCompletionReceiptKey(
			session,
			runtime,
			runtime.Session.Snapshot(),
			s.gameplayNow(),
			"settlement_plan_without_matching_clear_map_quest",
		); err != nil {
			return err
		}
	}
	if runtime.settlementResultPlan != nil {
		return nil
	}
	gain, err := currentDungeonClearExperience(runtime, resources)
	if err != nil {
		return err
	}
	// 成长契约 (premium type 84): +20% clear experience while active. The
	// total is committed and receipted, so an idempotent replay re-applies
	// exactly the same amount (86JP ComputeClearExp growth part).
	growthBonus := s.currentGrowthContractBonusExp(ctx, s.accountIDForSession(session), gain)
	// 评分奖励: base × ClearRankRates[bonusIndex] (86JP scoreBonusExp).
	clientRankPoint := runtime.settlementPlayResultBody[currentDungeonPlayResultClientRankPointOffset]
	scoreBonus := currentDungeonScoreBonusExp(gain, resources, clientRankPoint)
	// 黑钻加成: base × 10% (premium type 1 or 17).
	blackDiamondBonus := s.currentBlackDiamondBonusExp(ctx, s.accountIDForSession(session), gain)
	totalGain := uint32(min(uint64(gain)+uint64(growthBonus)+uint64(scoreBonus)+uint64(blackDiamondBonus), uint64(math.MaxUint32)))
	presentation, err := currentDungeonSettlementPresentationForRuntime(
		runtime,
		resources.ClearRankCatalog,
		clientRankPoint,
	)
	if err != nil {
		return err
	}
	result, err := s.commitCurrentDungeonSettlementWithPotion(
		ctx,
		session,
		runtime,
		resources.Progression,
		totalGain,
		gain,
	)
	if err != nil {
		return err
	}
	permissionBody, permissionState, permissionUpdated, permissionErr := s.commitCurrentDungeonPermission(
		ctx,
		session,
		runtime,
	)
	if permissionErr != nil {
		s.logGameEvent(session, "game-dungeon-permission-clear-state-update-failed",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"difficulty", runtime.Request.Difficulty,
			"source", "settlement_plan_after_reward_commit",
			"error", permissionErr)
	}
	if err := s.freezeCurrentDungeonCardRewardStateLocked(session, runtime); err != nil {
		s.logGameEvent(session, "game-dungeon-card-reward-plan-blocked",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"completion_key", runtime.clearMapCompletionKey,
			"source", "settlement_plan_pre_op35_freeze",
			"error", err)
	}
	drops, err := s.currentDungeonCommittedDropReceiptsLocked(session, runtime, result.Inventory)
	if err != nil {
		return err
	}
	cardSlots := currentDungeonClearRewardCardSlotsFromRuntime(runtime, currentDungeonCardFlipRowStateValue(session))
	var bonusPresentation [currentDungeonClearRewardBonusFieldCount]uint32
	bonusPresentation[currentDungeonClearRewardBonusClearAncientElixirIndex] = result.PotionBonus
	bonusPresentation[currentDungeonClearRewardBonusClearBlackDiamondIndex] = blackDiamondBonus
	bonusPresentation[currentDungeonClearRewardBonusClearGrowthContractIndex] = growthBonus
	bonusPresentation[currentDungeonClearRewardBonusMonsterGrowthContractIndex] = runtime.settlementMonsterGrowthContractBonus
	notice := currentDungeonPlayResultNotice{
		RankGrade:       presentation.RankGrade,
		ClearTimeMS:     presentation.ClearTimeMS,
		TimeBonusPoint:  presentation.TimeBonusPoint,
		RankPoint:       clientRankPoint,
		AllVisitedClear: presentation.AllVisitedClear,
		Participants: []currentDungeonPlayResultParticipant{{
			ObjectKey:   session.selectedCharacterID,
			ClearTimeMS: presentation.ClearTimeMS,
		}},
	}
	reward := currentDungeonClearRewardSnapshot{
		CharacterID:             session.selectedCharacterID,
		CompletionKey:           runtime.clearMapCompletionKey,
		Source:                  "runtime_pvf_dungeon_clear_and_committed_pickups",
		Committed:               true,
		CommittedExperienceGain: result.ExperienceGain,
		CommittedSPGain:         result.SPGain,
		CommittedTPGain:         result.TPGain,
		Base: [4]uint32{
			currentDungeonClearRewardBaseClearExperienceIndex: gain,
			currentDungeonClearRewardBaseScoreBonusIndex:      scoreBonus,
		},
		Bonus:     bonusPresentation,
		Score:     currentDungeonSettlementScore(runtime),
		Drops:     drops,
		CardSlots: cardSlots,
		Tail: currentDungeonClearRewardTail{
			ShowResult:      true,
			MonsterTotalExp: runtime.settlementMonsterExperienceTotal,
		},
	}
	if runtime.settlementCardRewardState != nil {
		runtime.settlementCardRewardState.mu.Lock()
		bonus := cloneDungeonCardRewardBundle(runtime.settlementCardRewardState.plan.BlackDiamondBonus)
		runtime.settlementCardRewardState.mu.Unlock()
		if bonus.Gold > 0 || len(bonus.Items) > 0 {
			currentDungeonProjectCardBundleToClearRewardSlots(&reward.AuxGroupsA, int(currentDungeonCardFlipRowStateValue(session)), bonus)
		}
	}
	// 2026-10-02 (V22)：投影摘要进日志 —— 这是"队友看到黑钻角色额外翻牌"唯一的线上自证点。
	// 全员普通时 projected 为空，不写额外行（零噪音）。
	if projected := projectPartyBlackDiamondCards(runtime, &reward.AuxGroupsA); len(projected) > 0 {
		s.logGameEvent(session, "game-dungeon-black-diamond-cards-projected",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"slot_owner_rows", strings.Join(projected, ","),
			"body_source", "projectPartyBlackDiamondCards_into_kind1_slots")
	}
	plan, err := buildCurrentDungeonSettlementPacketPlan(
		clientRankPoint,
		notice,
		reward,
		result.Character,
		result.Skill.Points,
	)
	if err != nil {
		return err
	}
	if len(permissionBody) != 0 {
		plan.DungeonPermissionBody = append([]byte(nil), permissionBody...)
	}
	plan.GuildDungeonItemSlots = guildDungeonRefreshSlots(result.GuildDungeonItems)
	if currentDungeonIsDespairTower(runtime) {
		floor, err := currentDespairFloor(runtime.Dungeon)
		if err != nil {
			return err
		}
		items := make([]currentDespairResultItem, 0, len(result.DespairItems))
		for _, item := range result.DespairItems {
			items = append(items, currentDespairResultItem{ItemID: item.ItemID, Quantity: item.Quantity})
			plan.DespairItemSlots = append(plan.DespairItemSlots, alignedcmd.ItemSlotRefresh{ListType: item.ListType, SlotIndex: item.Slot})
		}
		plan.DespairResultBody, err = buildCurrentDespairResultBody(presentation.ClearTimeMS, uint16(floor), items)
		if err != nil {
			return err
		}
	}
	if runtime.Dungeon.Metadata.IntegerValues["tower of grief"] == 1 {
		floor, err := currentGriefFloor(runtime.Dungeon)
		if err != nil {
			return err
		}
		items := make([]currentDespairResultItem, len(result.GriefItems))
		for i, item := range result.GriefItems {
			items[i] = currentDespairResultItem{ItemID: item.ItemID, Quantity: item.Quantity}
			if item.ItemID != 0 {
				plan.GriefItemSlots = append(plan.GriefItemSlots, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: item.Slot})
			}
		}
		plan.GriefResultBody, err = buildCurrentGriefResultBody(presentation.ClearTimeMS, uint16(floor), items)
		if err != nil {
			return err
		}
	}
	runtime.Character = dnfrepo.CloneCharacter(result.Character)
	runtime.settlementResultPlan = &plan
	s.logGameEvent(session, "game-dungeon-settlement-plan-frozen",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"completion_key", runtime.clearMapCompletionKey,
		"experience_gain", result.ExperienceGain,
		"story_experience_quest_id", runtime.storyExperience.QuestID,
		"story_experience_rate_percent", runtime.storyExperience.RatePercent,
		"growth_contract_bonus", growthBonus,
		"sp_gain", result.SPGain,
		"tp_gain", result.TPGain,
		"remaining_sp", result.Skill.Points.RemainingSP,
		"remaining_tp", result.Skill.Points.RemainingTP,
		"op34_rank_grade", presentation.RankGrade,
		"op34_rank_point", clientRankPoint,
		"op34_clear_time_ms", presentation.ClearTimeMS,
		"op34_all_visited_clear", presentation.AllVisitedClear,
		"op34_time_bonus_point", presentation.TimeBonusPoint,
		"picked_drop_receipts", len(drops),
		"replayed", result.Replayed,
		"op35_clear_base_exp", gain,
		"op35_clear_score_bonus_exp", scoreBonus,
		"op35_clear_growth_contract_bonus", growthBonus,
		"op35_clear_black_diamond_bonus", blackDiamondBonus,
		"op35_monster_base_exp", runtime.settlementMonsterExperienceTotal,
		"op35_monster_growth_contract_bonus", runtime.settlementMonsterGrowthContractBonus,
		"op35_show_result", true,
		"op35_free_card_pairs", len(cardSlots[0]),
		"op35_champion_experience", runtime.settlementChampionExperience,
		"op35_super_champion_experience", runtime.settlementSuperChampionExperience,
		"op35_boss_experience", runtime.settlementBossExperience,
		"card_rewards", runtime.settlementCardRewardState != nil,
		"dungeon_permission_updated", permissionUpdated,
		"dungeon_permission_state", permissionState,
		"unknown_op35_business_fields", "zero_except_proved_result_gate_score_experience_and_card_reward_context")
	return nil
}

func currentDungeonSettlementScore(runtime *runtimeDungeonState) [currentDungeonClearRewardScoreFieldCount]uint32 {
	if runtime == nil {
		return [currentDungeonClearRewardScoreFieldCount]uint32{}
	}
	// Current NoPack consumes Score[1..3] as the champion,
	// super-champion, and boss result breakdown. Score[0] remains neutral;
	// aggregate monster EXP has its dedicated tail field. Clamp every
	// classified subtotal to that aggregate so damaged runtime state cannot
	// overstate it.
	return [currentDungeonClearRewardScoreFieldCount]uint32{
		0,
		min(runtime.settlementChampionExperience, runtime.settlementMonsterExperienceTotal),
		min(runtime.settlementSuperChampionExperience, runtime.settlementMonsterExperienceTotal),
		min(runtime.settlementBossExperience, runtime.settlementMonsterExperienceTotal),
	}
}

func currentDungeonClearRewardCardSlotsFromRuntime(runtime *runtimeDungeonState, partySlot ...byte) [currentDungeonClearRewardGroupCount][]currentDungeonClearRewardPair {
	var slots [currentDungeonClearRewardGroupCount][]currentDungeonClearRewardPair
	if runtime == nil || runtime.settlementCardRewardState == nil {
		return slots
	}
	runtime.settlementCardRewardState.mu.Lock()
	plan := cloneDungeonCardRewardPlan(runtime.settlementCardRewardState.plan)
	runtime.settlementCardRewardState.mu.Unlock()
	owner := 0
	if runtime.partyInstance != nil && len(partySlot) > 0 && partySlot[0] < dungeonCardSlotsPerSide {
		owner = int(partySlot[0])
	}
	currentDungeonProjectCardBundleToClearRewardSlots(&slots, owner, plan.Sides[dungeonCardSideFree])
	currentDungeonProjectCardBundleToClearRewardSlots(
		&slots,
		dungeonCardSlotsPerSide+owner,
		plan.Sides[dungeonCardSidePaid],
	)
	return slots
}

func currentDungeonProjectCardBundleToClearRewardSlots(
	slots *[currentDungeonClearRewardGroupCount][]currentDungeonClearRewardPair,
	base int,
	bundle dungeonCardRewardBundle,
) {
	if slots == nil || base < 0 || base >= len(slots) {
		return
	}
	displayItems := currentDungeonCardDisplayItems(bundle)
	pairs := make([]currentDungeonClearRewardPair, 0, 1+len(displayItems))
	if bundle.Gold > 0 && bundle.Gold <= math.MaxUint32 {
		pairs = append(pairs, currentDungeonClearRewardPair{Key: 0, Value: uint32(bundle.Gold)})
	}
	for _, item := range displayItems {
		if item.ItemID <= 0 || item.ItemID > math.MaxUint32 || item.Count <= 0 {
			continue
		}
		count := item.Count
		if count > math.MaxUint32 {
			count = math.MaxUint32
		}
		pairs = append(pairs, currentDungeonClearRewardPair{
			Key:   uint32(item.ItemID),
			Value: uint32(count),
		})
	}
	// The eight op35 groups are party-member seats (free 0..3, paid 4..7),
	// not separate gold/item card faces. Keep every reward tuple for one side
	// in that member's single group so the free-row reveal can render the item
	// while retaining the accompanying gold amount.
	slots[base] = pairs
}

func (s *Service) currentDungeonSettlementRewardResources(ctx context.Context) (currentDungeonSettlementRewardResources, error) {
	if s == nil {
		return currentDungeonSettlementRewardResources{}, errCurrentDungeonSettlementOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return currentDungeonSettlementRewardResources{}, err
	}
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		return currentDungeonSettlementRewardResources{}, err
	}
	sources, err := progression.LoadMonsterExperienceSources(archive)
	if err != nil {
		return currentDungeonSettlementRewardResources{}, err
	}
	clearRankCatalog, err := loadCurrentDungeonClearRankCatalog(archive)
	if err != nil {
		return currentDungeonSettlementRewardResources{}, err
	}
	return currentDungeonSettlementRewardResources{
		Progression:       tables,
		ExperienceSources: sources,
		ClearRankCatalog:  clearRankCatalog,
	}, nil
}

func currentDungeonClearExperience(
	runtime *runtimeDungeonState,
	resources currentDungeonSettlementRewardResources,
) (uint32, error) {
	if runtime == nil || resources.Progression == nil || resources.ExperienceSources == nil ||
		!runtime.Dungeon.Metadata.BasisLevel.Set || runtime.Dungeon.Metadata.BasisLevel.Value <= 0 {
		return 0, errCurrentDungeonSettlementPVFInvalid
	}
	// The dungeon field is optional in the runtime PVF: ordinary dungeon 3
	// (幽暗密林) omits it. The established domain rule is neutral weight 1.0
	// when absent or negative; an explicit non-negative value remains exact.
	weight := 1.0
	if runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set &&
		runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value >= 0 {
		weight = runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value
	}
	weight *= currentDungeonStoryExperienceMultiplier(runtime)
	if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
		return 0, errCurrentDungeonSettlementPVFInvalid
	}
	base, err := resources.Progression.DungeonClearBaseExperience(int(runtime.Dungeon.Metadata.BasisLevel.Value))
	if err != nil || base == 0 {
		return 0, fmt.Errorf("%w: base=%d: %v", errCurrentDungeonSettlementPVFInvalid, base, err)
	}
	modifiers := resources.ExperienceSources.RawModifiers()
	difficulty := int(runtime.Request.Difficulty)
	if difficulty < 0 || difficulty >= len(modifiers.DifficultyRates) {
		return 0, errCurrentDungeonSettlementPVFInvalid
	}
	difficultyRate := modifiers.DifficultyRates[difficulty]
	if math.IsNaN(difficultyRate) || math.IsInf(difficultyRate, 0) || difficultyRate <= 0 {
		return 0, errCurrentDungeonSettlementPVFInvalid
	}
	value := math.Floor(float64(base) * weight * difficultyRate)
	if value <= 0 || value > math.MaxUint32 {
		return 0, fmt.Errorf("%w: computed=%v", errCurrentDungeonSettlementPVFInvalid, value)
	}
	return uint32(value), nil
}

// currentDungeonScoreBonusExp calculates the score bonus experience from the
// PVF [clear rank exp bonusrate] table. The bonus index is the number of rank
// grade thresholds the client rank point meets or exceeds (86JP
// MonsterRewardTable.GetClearRankBonusIndex).
func currentDungeonScoreBonusExp(base uint32, resources currentDungeonSettlementRewardResources, clientRankPoint byte) uint32 {
	if base == 0 || resources.ExperienceSources == nil {
		return 0
	}
	modifiers := resources.ExperienceSources.RawModifiers()
	bonusIndex := 0
	for _, threshold := range resources.ClearRankCatalog.gradeThresholds {
		if clientRankPoint >= threshold {
			bonusIndex++
		}
	}
	if bonusIndex <= 0 || bonusIndex > len(modifiers.ClearRankRates) {
		return 0
	}
	rate := modifiers.ClearRankRates[bonusIndex-1]
	if rate <= 0 {
		return 0
	}
	bonus := math.Floor(float64(base) * rate)
	if bonus <= 0 || bonus > math.MaxUint32 {
		return 0
	}
	return uint32(bonus)
}

// currentBlackDiamondBonusExp returns +10% clear experience when the account
// has an active Black Diamond entitlement. Auction coupons do not qualify.
func (s *Service) currentBlackDiamondBonusExp(ctx context.Context, accountID string, base uint32) uint32 {
	if s == nil || base == 0 || strings.TrimSpace(accountID) == "" {
		return 0
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil {
		return 0
	}
	account, found, err := repositories.Account.Load(ctx, strings.TrimSpace(accountID))
	if err != nil || !found {
		return 0
	}
	now := s.gameplayNow()
	if !premium.Active(account, currentDungeonBlackDiamondPremiumType, now) {
		return 0
	}
	bonus := math.Floor(float64(base) * currentDungeonBlackDiamondBonusRate)
	if bonus <= 0 || bonus > math.MaxUint32-float64(base) {
		return 0
	}
	return uint32(bonus)
}

func (s *Service) commitCurrentDungeonSettlement(ctx context.Context, session *gameSession, runtime *runtimeDungeonState, tables *progression.Tables, gain uint32) (currentDungeonSettlementCommitResult, error) {
	return s.commitCurrentDungeonSettlementWithPotion(ctx, session, runtime, tables, gain, 0)
}

func (s *Service) commitCurrentDungeonSettlementWithPotion(
	ctx context.Context,
	session *gameSession,
	runtime *runtimeDungeonState,
	tables *progression.Tables,
	gain uint32,
	potionBase uint32,
) (currentDungeonSettlementCommitResult, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterSettlement == nil || tables == nil || gain == 0 {
		return currentDungeonSettlementCommitResult{}, errCurrentDungeonSettlementOwnerUnavailable
	}
	owner, err := dnfdungeon.NewOwner(repositories)
	if err != nil {
		return currentDungeonSettlementCommitResult{}, errCurrentDungeonSettlementOwnerUnavailable
	}
	if runtime == nil {
		return currentDungeonSettlementCommitResult{}, errCurrentDungeonSettlementStateInvalid
	}
	if runtime.semesterRun != nil && !runtime.clearMapCompletionPhaseAPersisted {
		return currentDungeonSettlementCommitResult{}, errCurrentDungeonSettlementStateInvalid
	}
	runtimeExperience, present := runtime.Character.Stats["exp"]
	if !present || runtimeExperience < 0 || uint64(runtimeExperience) > math.MaxUint32 {
		return currentDungeonSettlementCommitResult{}, errCurrentDungeonSettlementStateInvalid
	}
	now := s.gameplayNow()
	predictedGain := gain
	if potionBase > 0 {
		character, found, err := s.loadExperiencePotionCharacter(ctx, session)
		if err != nil {
			return currentDungeonSettlementCommitResult{}, err
		}
		if !found {
			return currentDungeonSettlementCommitResult{}, errCurrentDungeonSettlementStateInvalid
		}
		predictedGain += min(experiencebuff.Bonus(character, experiencebuff.Clear, potionBase, now), math.MaxUint32-gain)
	}
	honorExpertGain, err := currentHonorExpertExperienceGain(
		tables,
		runtime.Character.Level,
		uint32(runtimeExperience),
		predictedGain,
	)
	if err != nil {
		return currentDungeonSettlementCommitResult{}, err
	}
	// Only load the independent PVF honor table for an award that actually
	// reaches the character-level-cap split.
	var honorTables *dnfhonor.Tables
	if honorExpertGain > 0 {
		loaded, loadErr := s.loadHonorTable(ctx)
		if loadErr != nil {
			return currentDungeonSettlementCommitResult{}, loadErr
		}
		honorTables = loaded
	}
	adventureRuntime := s.adventureGroupTable.Runtime()
	var weekly *dnfdungeon.WeeklyClearContract
	if s.currentWeeklyAttendanceEnabled() && currentDungeonIsRecommendedClear(runtime) {
		season, err := s.currentWeeklySeason(session)
		if err != nil {
			return currentDungeonSettlementCommitResult{}, err
		}
		if season.Week(s.gameplayNow()) >= 0 {
			catalog, err := s.currentWeeklyCatalog(ctx)
			if err != nil {
				return currentDungeonSettlementCommitResult{}, err
			}
			weekly = &dnfdungeon.WeeklyClearContract{Catalog: catalog, Season: season}
		}
	}
	luckyPolicy, err := s.currentDungeonLuckyStarPolicy(runtime)
	if err != nil {
		return currentDungeonSettlementCommitResult{}, err
	}
	despairFloor, err := currentDespairFloor(runtime.Dungeon)
	if err != nil {
		return currentDungeonSettlementCommitResult{}, err
	}
	if despairFloor != 0 {
		if err := s.freezeCurrentDespairRewardsLocked(runtime); err != nil {
			return currentDungeonSettlementCommitResult{}, err
		}
	}
	griefFloor, err := currentGriefFloor(runtime.Dungeon)
	if err != nil {
		return currentDungeonSettlementCommitResult{}, err
	}
	if griefFloor != 0 {
		if err := s.freezeCurrentGriefRewardsLocked(runtime); err != nil {
			return currentDungeonSettlementCommitResult{}, err
		}
	}
	if err := s.freezeCurrentGuildDungeonRewards(runtime); err != nil {
		return currentDungeonSettlementCommitResult{}, err
	}
	completionKey := runtime.clearMapCompletionKey
	if runtime.guildDungeonClear != nil {
		completionKey = runtime.guildDungeonClear.Entry.CompletionKey()
	}
	rankingClear, err := s.currentGuildRankingClear(session, runtime)
	if err != nil {
		return currentDungeonSettlementCommitResult{}, err
	}
	var essence *guild.EssenceCatalog
	var essenceCurrent func() bool
	contribution, err := s.currentGuildContributionClear(session, runtime, completionKey)
	if err != nil {
		return currentDungeonSettlementCommitResult{}, err
	}
	wantEssence := s.options.guildEssenceEnabled && (runtime.guildDungeonClear != nil || currentDungeonIsRecommendedClear(runtime))
	if wantEssence || contribution != nil {
		identity, bound := s.boundGameSessionCharacterSnapshot(session)
		if !bound {
			return currentDungeonSettlementCommitResult{}, errCurrentDungeonSettlementStateInvalid
		}
		release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
		if err != nil {
			return currentDungeonSettlementCommitResult{}, err
		}
		defer release()
		if wantEssence {
			essence, err = s.currentGuildEssenceCatalog()
			if err != nil {
				return currentDungeonSettlementCommitResult{}, err
			}
		}
		essenceCurrent = func() bool { return s.boundGameSessionCharacterCurrent(identity) }
	}
	result, err := owner.CommitSettlement(ctx, dnfdungeon.SettlementCommand{
		GuildContribution: contribution,
		GuildEssence:      essence, GuildEssenceRevalidate: essenceCurrent,
		GuildRanking:            rankingClear,
		GuildDungeon:            runtime.guildDungeonClear,
		DespairClearedFloor:     despairFloor,
		GriefClearedFloor:       griefFloor,
		GriefRewards:            runtime.settlementGriefRewards,
		DespairRewards:          runtime.settlementDespairRewards,
		AccountID:               s.accountIDForSession(session),
		CharacterID:             fmt.Sprintf("%d", session.selectedCharacterID),
		CompletionKey:           completionKey,
		Tables:                  tables,
		Experience:              gain,
		ClearPotionBase:         potionBase,
		RecommendedDungeonClear: currentDungeonIsRecommendedClear(runtime),
		GuildOrdinaryClear:      currentDungeonEarnsGuildMileage(runtime),
		LuckyStar:               luckyPolicy,
		MonthlyAttendance:       s.currentMonthlyAttendanceEnabled(),
		WeeklyAttendance:        weekly,
		AdventureRuntime:        &adventureRuntime,
		HonorExpertTables:       honorTables,
		MaximumCharacterLevel:   currentAdventureCharacterLevelCap,
		UpdatedAt:               now,
	})
	if err != nil {
		return currentDungeonSettlementCommitResult{}, err
	}
	runtime.settlementLuckyStar = result.LuckyStar
	if contribution != nil {
		if err := s.projectCurrentGuildContribution(session, result.GuildContribution); err != nil {
			s.logGameEvent(session, "game-guild-contribution-clear-projection-deferred", "error", err)
		}
	}
	if essence != nil {
		if err := s.projectCurrentGuildEssenceIncome(session, result.GuildEssence, result.Replayed); err != nil {
			s.logGameEvent(session, "game-guild-essence-projection-deferred", "error", err)
		}
	}
	if result.GuildMileage.GuildID != 0 {
		s.refreshCurrentDungeonGuildMileage(session, result.GuildMileage.GuildID)
	}
	if s.currentMonthlyAttendanceEnabled() {
		// Experience/clear receipt is already committed. Mail delivery is
		// retried from that durable account state if transport or mail fails.
		if err := s.sendCurrentMonthlyAttendance(session, false); err != nil {
			s.logGameEvent(session, "game-monthly-attendance-settlement-deferred", "error", err)
		}
	}
	if weekly != nil {
		if err := s.sendCurrentWeeklyAttendance(session, false, false); err != nil {
			s.logGameEvent(session, "game-weekly-attendance-settlement-deferred", "error", err)
		}
	}
	return currentDungeonSettlementCommitResult{
		GuildDungeonItems: result.GuildDungeonItems,
		DespairItems:      result.DespairItems,
		GriefItems:        result.GriefItems,
		Character:         result.Character,
		Skill:             result.Skill,
		Inventory:         result.Inventory,
		ExperienceGain:    result.ExperienceGain,
		PotionBonus:       result.PotionBonus,
		HonorExpertGain:   result.HonorExpertGain,
		SPGain:            result.SPGain,
		TPGain:            result.TPGain,
		Replayed:          result.Replayed,
	}, nil
}

func planCurrentDungeonSettlementProgression(
	tables *progression.Tables,
	level int,
	total uint32,
	gain uint32,
	points dnfrepo.SkillPointState,
) (progression.ExperienceSkillPointPlan, error) {
	return dnfdungeon.PlanSettlementProgressionAtCap(
		tables,
		level,
		total,
		gain,
		points,
		currentAdventureCharacterLevelCap,
	)
}

// ── 操作者缺陷「单人挑战深渊 通关后无法结算」的根因修复 ────────────────────────
//
// 现场（`packet_log.txt`，2026-09-28，`conn_id=game-000024` char6 单人深渊 310）：
//
//	17:14:06.437 `game-dungeon-abyss-epic-fragment-granted … amount=7`（保底碎片，地上）
//	17:14:08.3–09.6 七条 `game-dungeon-pickup-accepted epic_piece=true … destination_slot=64
//	                 asset_owner=account_epic_piece_metadata_counter`（**捡起来了**）
//	17:14:52.885 `game-dungeon-ordinary-final-clear-committed room=5:2` → op115 → **op31**
//	17:14:52.924 客户端 op46（`game-dungeon-set-play-result-captured`）
//	17:14:54.680 `game-dungeon-settlement-plan-blocked … error=dungeon settlement persisted
//	              state is invalid`   ← **结算计划在这里被整条挡掉**
//	17:15:37/40   客户端 op123 被拒（`character-statistic-blocked`）、op42 被拒
//	              （`giveup-game-blocked … run_status=completed …`）⇒ 操作者看到的就是
//	              "通关了但不能结算"（没有 op34/op37/op35、没有翻牌界面）。
//
// 根因：片段**故意不占背包槽位**（`dungeon_drop_pickup.go:410-506`：只入账号级史诗图鉴
// 账本，目的槽位写哨兵 `currentEpicPiecePickupEffectSlot = 64`），而结算的"已拾取掉落
// 收据"校验要求**每一条已消耗掉落**都能在背包里按目的槽位找到同一件物品
// （`currentDungeonCommittedDropReceipts` 里的 `inventory.Slots[PickupMainSlotKey(slot)]`）。
// 碎片两条都不满足 ⇒ 整局结算计划失败。**只有深渊会往地上放碎片**，所以只有深渊局撞上：
// 全日志 6 条 `settlement-plan-blocked` **全部**是深渊局（dungeon 310/314），
// 而 26 条 `settlement-plan-frozen` 全是普通局。
//
// 修复 = 把"账号级账本资产"从背包收据校验里豁免：它已经被
// `addCurrentEpicPieceBalance` 记账 + 回包 + `EpicPieceBalance` 收据钉住，
// 不进背包是**设计**而不是状态损坏。豁免掉的每一条都写
// `game-dungeon-settlement-drop-receipt-exempt`（带房间/房间类型/Boss 状态/决定）。
//
// 开关 `DNFBRIDGE_ABYSS_SETTLEMENT_FRAGMENT_RECEIPT`：未设置/空/任意值 = 豁免档（默认，修复）；
// 只有字面量 `legacy` 才回到"严格要求背包槽位"（= 逐字节复现这条缺陷，便于 A/B）。
const abyssSettlementFragmentReceiptEnv = "DNFBRIDGE_ABYSS_SETTLEMENT_FRAGMENT_RECEIPT"

const abyssSettlementFragmentReceiptLegacyValue = "legacy"

type abyssSettlementFragmentReceiptMode int

const (
	// abyssSettlementFragmentReceiptExempt 是默认档，也是修复档（零值 = 修复）。
	abyssSettlementFragmentReceiptExempt abyssSettlementFragmentReceiptMode = iota
	// abyssSettlementFragmentReceiptLegacy 是改动前的严格档。
	abyssSettlementFragmentReceiptLegacy
)

func readAbyssSettlementFragmentReceiptMode(value string) abyssSettlementFragmentReceiptMode {
	if strings.EqualFold(strings.TrimSpace(value), abyssSettlementFragmentReceiptLegacyValue) {
		return abyssSettlementFragmentReceiptLegacy
	}
	return abyssSettlementFragmentReceiptExempt
}

func (mode abyssSettlementFragmentReceiptMode) String() string {
	if mode == abyssSettlementFragmentReceiptLegacy {
		return abyssSettlementFragmentReceiptLegacyValue
	}
	return "exempt"
}

func (mode abyssSettlementFragmentReceiptMode) exempts() bool {
	return mode == abyssSettlementFragmentReceiptExempt
}

func abyssSettlementFragmentReceiptModeFromEnv() abyssSettlementFragmentReceiptMode {
	return readAbyssSettlementFragmentReceiptMode(os.Getenv(abyssSettlementFragmentReceiptEnv))
}

// ── 操作者缺陷「组队死亡后无法结算 / 两边客户端假死」的根因修复 ─────────────────────
//
// 现场（`packet_log.txt`，2026-09-29，双开 party_id=1，`conn_id=game-002698` char1 与
// `conn_id=game-002700` char4，深渊 310）：
//
//	05:52:50.247 `game-dungeon-party-instance-created … reason=first_party_member_published_shared_room_and_drop_ledger`
//	05:52:50.247 `game-dungeon-party-instance-joined conn_id=game-002700 char_id=4 member_count=2`
//	239926 char4 `game-dungeon-pickup-accepted drop_object_key=410 … destination_slot=121`
//	240575 char4 `game-dungeon-pickup-accepted drop_object_key=443 … destination_slot=124`
//	240584 char1 `game-dungeon-pickup-accepted drop_object_key=441 … destination_slot=129`
//	240591 char1 `game-dungeon-pickup-accepted drop_object_key=442 … destination_slot=132`
//	241014 char1 `game-dungeon-pickup-accepted drop_object_key=461 … destination_slot=133`
//	241327 char1 `game-dungeon-pickup-accepted drop_object_key=470 … destination_slot=129`
//	05:58:28.226 `game-dungeon-ordinary-final-clear-committed … target_object_key=468`（Boss 已击杀）
//	05:58:28.245 op31 → 05:58:28.310 char4 `game-dungeon-settlement-plan-blocked … error=dungeon
//	              settlement persisted state is invalid`（`source=first_op46_capture`）
//	05:58:28.336 char1 同一条 `…-plan-blocked`（**两边都挡**）
//	⇒ 没有 op34/op37/op35、没有翻牌、没有退场路径 ⇒ 操作者看到的"两个客户端假死
//	  （有 FPS 但不能返回城镇）"。
//
// 根因（**用字节钉死，两步**）：
//
//	① 提交**成功**了 —— 不是事务失败：char1 的收据四个 hashCode 与
//	   `dungeon-clear:char:1:dungeon:310:…:completed_at_unix_nano:1790661508252052771:target:468:…`
//	   的 sha256 逐字相等（8341366568711423369/7769070053022770789/1138846381618873267），
//	   char4 的四个值等于其存库负数的无符号形式；`dnf_character_stats` 里
//	   `dungeon_settlement_receipt_new_experience=1586095` 与 char4 当前 `exp` 相等。
//	   ⇒ `CommitSettlement` 通过，`ErrSettlementStateInvalid` 只可能来自**后面的掉落收据校验**。
//	② 组队副本实例的掉落账本是**两个会话共用一份**（`partySharedDungeonDropOwnerLocked`
//	   把 `runtime.DropOwner` 指向 `instance.dropOwner`；线上日志
//	   `party-instance-created … reason=first_party_member_published_shared_room_and_drop_ledger` 就是它），
//	   而**独立掉落档**（`DNFBRIDGE_ABYSS_PARTY_DROP_OWNER` 未设置 = `independent`）下
//	   每名成员各有一份**属于自己的**掉落。结算读收据时却把**整份共享账本**逐条拿去和
//	   **本角色自己的背包**对：别人的掉落必然找不到 ⇒ 整局结算计划失败。
//
// 数据库逐字对照（`bash /root/dbq2.sh 'SELECT collection_name,entry_key,item_id,item_count FROM dnf_inventory_items …'`）：
//
//	char1 `slots 0:121 = item 3330` vs char4 的收据要求 `0:121 = item 3030` ⇒ ItemID 不等
//	char1 **没有** `0:129/0:132/0:133`（那是 char4 收据要的槽位）
//	char1 自己的四条（`0:129=3028 含两件`、`0:132=3142`、`0:133=3052`）**全部自洽**
//	⇒ 把"自己那几条"全部验完也解释不了失败，**唯一剩下的失败源就是别人的掉落**。
//
// 修复 = 结算收据只统计**属于本角色**的已消耗掉落。归属判据用掉落自己已经带的权威字段
// `OwnerActorObjectKey`（op38 行里那一个，拾取时 `owned()` 也是拿它比的），**不新造判据**：
//
//	跳过条件 = owner != 0 且 owner != 本会话 actor key 且 !PartyShared 且 !MapOwnedPassiveObject。
//
// `PartyShared`（`DNFBRIDGE_ABYSS_PARTY_DROP_OWNER=shared` 回退档）与
// `MapOwnedPassiveObject`（op29 预生成建筑掉落，本来就没有 owner）**不受影响**，
// 所以那条回退档的行为逐字节不变；真正损坏的状态（目的槽位 0、包里 ItemID 不符）
// 仍然照旧 fail-closed。
//
// 开关 `DNFBRIDGE_PARTY_SETTLEMENT_RECEIPT_OWNER`：未设置/空/任意值 = 过滤档（默认，修复）；
// 只有字面量 `legacy` 才回到"整份账本都要在背包里"（= 逐字节复现这条缺陷，便于 A/B）。
const partySettlementReceiptOwnerEnv = "DNFBRIDGE_PARTY_SETTLEMENT_RECEIPT_OWNER"

const partySettlementReceiptOwnerLegacyValue = "legacy"

// partySettlementReceiptOwnerMode 是结算掉落收据的归属档位。
type partySettlementReceiptOwnerMode int

const (
	// partySettlementReceiptOwnerFilter 是默认档，也是修复档（零值 = 修复）。
	partySettlementReceiptOwnerFilter partySettlementReceiptOwnerMode = iota
	// partySettlementReceiptOwnerLegacy 是改动前的"整份账本"档。
	partySettlementReceiptOwnerLegacy
)

func readPartySettlementReceiptOwnerMode(value string) partySettlementReceiptOwnerMode {
	if strings.EqualFold(strings.TrimSpace(value), partySettlementReceiptOwnerLegacyValue) {
		return partySettlementReceiptOwnerLegacy
	}
	return partySettlementReceiptOwnerFilter
}

func (mode partySettlementReceiptOwnerMode) String() string {
	if mode == partySettlementReceiptOwnerLegacy {
		return partySettlementReceiptOwnerLegacyValue
	}
	return "own"
}

// filtersForeignOwned 报告该档位是否只统计属于本角色的已消耗掉落。
func (mode partySettlementReceiptOwnerMode) filtersForeignOwned() bool {
	return mode == partySettlementReceiptOwnerFilter
}

func partySettlementReceiptOwnerModeFromEnv() partySettlementReceiptOwnerMode {
	return readPartySettlementReceiptOwnerMode(os.Getenv(partySettlementReceiptOwnerEnv))
}

// currentDungeonSettlementExemptDropReceipt 是被豁免的一条已消耗掉落（只用于事件）。
type currentDungeonSettlementExemptDropReceipt struct {
	ObjectKey       uint32
	SceneSlot       uint16
	ItemID          uint32
	DestinationSlot uint16
	StackCount      uint32
	AssetOwner      string
}

// currentDungeonSettlementForeignDropReceipt 是被"不属于本角色"而跳过的已消耗掉落（只用于事件）。
type currentDungeonSettlementForeignDropReceipt struct {
	ObjectKey           uint32
	SceneSlot           uint16
	ItemID              uint32
	DestinationSlot     uint16
	StackCount          uint32
	OwnerActorObjectKey uint16
	Room                runtimeDungeonRoomKey
}

// currentDungeonCommittedDropReceiptsLocked 是结算计划读掉落收据的**会话入口**。
// 默认档做两件事：① 把账号级史诗碎片拾取从"必须在背包里找到槽位"里豁免掉；
// ② 只统计**属于本角色**的已消耗掉落（组队共享账本里别人的掉落不算本角色的收据）。
// 两件事各有可判读事件。穿戴拾取装备的核验独立于这两个开关；没有提供
// 穿戴记录的纯函数入口仍保留原背包判据。
func (s *Service) currentDungeonCommittedDropReceiptsLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	inventory dnfrepo.InventoryRecord,
) ([]currentDungeonClearRewardDrop, error) {
	exemptMode := abyssSettlementFragmentReceiptModeFromEnv()
	ownerMode := partySettlementReceiptOwnerModeFromEnv()
	selfActorObjectKey := uint16(0)
	if session != nil {
		selfActorObjectKey = currentSceneActorObjectKey(session.selectedCharacterID)
	}
	drops, exempted, foreign, err := currentDungeonCommittedDropReceiptsFiltered(
		runtime,
		inventory,
		exemptMode.exempts(),
		ownerMode.filtersForeignOwned(),
		selfActorObjectKey,
	)
	// A picked equipment item can be worn before clear (the tutorial requires
	// this). It no longer owns a bag slot; verify its committed pickup row in
	// this character's equipment aggregate instead of fabricating an op35 row.
	if errors.Is(err, errCurrentDungeonSettlementStateInvalid) && s != nil && session != nil {
		if repositories, ok := s.repositoryGroup(); ok && repositories.Equipment != nil {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			equipment, found, loadErr := repositories.Equipment.Load(ctx, fmt.Sprint(session.selectedCharacterID))
			cancel()
			if loadErr != nil {
				return nil, loadErr
			}
			if found && equipment.CharacterID == inventory.CharacterID && equipment.CharacterID == fmt.Sprint(session.selectedCharacterID) {
				drops, exempted, foreign, err = currentDungeonCommittedDropReceiptsFiltered(
					runtime, inventory, exemptMode.exempts(), ownerMode.filtersForeignOwned(), selfActorObjectKey, equipment,
				)
				if err == nil {
					s.logGameEvent(session, "game-dungeon-settlement-equipped-pickup-verified",
						"char_id", session.selectedCharacterID,
						"dungeon_id", runtime.Dungeon.ID,
						"bag_projection_count", len(drops),
						"source", "committed_pickup_op39_op14_and_owned_persisted_equipment",
						"bag_item_fabricated", false)
				}
			}
		}
	}
	if err != nil || s == nil || session == nil || runtime == nil ||
		(len(exempted) == 0 && len(foreign) == 0) {
		return drops, err
	}
	room, roomType, mapID := "", "unknown", int64(0)
	bossRoom, coordinateMatched := false, false
	if runtime.Session != nil {
		if scene, ok := runtime.Session.Scene(); ok {
			state := currentDungeonHellPartyClearState(runtime, scene)
			room, roomType, mapID = state.Room, state.roomType(), state.RoomMapID
			bossRoom, coordinateMatched = state.BossRoom, state.BossCoordinateMatched
		}
	}
	runStatus := ""
	if runtime.Session != nil {
		runStatus = string(runtime.Session.Snapshot().Run.Status)
	}
	for _, drop := range exempted {
		if drop.AssetOwner == "character_inventory_committed_consumption" {
			s.logGameEvent(session, "game-dungeon-settlement-consumed-pickup-receipt",
				"char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID,
				"drop_object_key", drop.ObjectKey, "item_id", drop.ItemID,
				"message", "已确认拾取物品在本局正常用完，结算不再要求背包现存行，不重复发放")
			continue
		}
		s.logGameEvent(session, "game-dungeon-settlement-drop-receipt-exempt",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", room,
			"map_id", mapID,
			"room_type", roomType,
			"boss_room", bossRoom,
			"boss_coordinate_matched", coordinateMatched,
			"run_status", runStatus,
			"completion_key", runtime.clearMapCompletionKey,
			"drop_object_key", drop.ObjectKey,
			"scene_slot", drop.SceneSlot,
			"item_id", drop.ItemID,
			"stack_count", drop.StackCount,
			"destination_slot", drop.DestinationSlot,
			"asset_owner", drop.AssetOwner,
			"decision", "settlement_plan_proceeds_without_a_bag_slot_for_this_receipt",
			"reason", "account_level_epic_piece_grant_has_no_inventory_slot_by_design",
			"switch", exemptMode.String(),
			"env", abyssSettlementFragmentReceiptEnv,
			"source_vendor", "vendor_epic_piece_ledger_is_account_scoped_not_a_character_bag_row")
	}
	for _, drop := range foreign {
		s.logGameEvent(session, "game-dungeon-settlement-drop-receipt-foreign-skip",
			"char_id", session.selectedCharacterID,
			"self_actor_object_key", selfActorObjectKey,
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", room,
			"map_id", mapID,
			"room_type", roomType,
			"boss_room", bossRoom,
			"boss_coordinate_matched", coordinateMatched,
			"run_status", runStatus,
			"completion_key", runtime.clearMapCompletionKey,
			"drop_object_key", drop.ObjectKey,
			"scene_slot", drop.SceneSlot,
			"item_id", drop.ItemID,
			"stack_count", drop.StackCount,
			"destination_slot", drop.DestinationSlot,
			"owner_actor_object_key", drop.OwnerActorObjectKey,
			"drop_room", dungeonRoomCoordinate(drop.Room.X, drop.Room.Y),
			"drop_map_id", drop.Room.MapID,
			"decision", "settlement_plan_excludes_a_teammates_consumed_drop",
			"reason", "party_instance_shares_one_drop_ledger_but_each_member_owns_its_own_drops",
			"switch", ownerMode.String(),
			"env", partySettlementReceiptOwnerEnv,
			"source_vendor", "vendor_independent_drop_each_object_belongs_to_exactly_one_member")
	}
	return drops, err
}

// 兼容入口保持碎片与归属策略；新增的已提交使用凭证在各档均有效。
// 普通拾取仍须对应现存背包行，正常用完的物品只验证拾取与使用凭证。
func currentDungeonCommittedDropReceipts(
	runtime *runtimeDungeonState,
	inventory dnfrepo.InventoryRecord,
) ([]currentDungeonClearRewardDrop, error) {
	drops, _, _, err := currentDungeonCommittedDropReceiptsFiltered(runtime, inventory, false, false, 0)
	return drops, err
}

// currentDungeonCommittedDropReceiptsWithExemptions 是"碎片豁免"这一层的历史入口（逐字节保留）。
func currentDungeonCommittedDropReceiptsWithExemptions(
	runtime *runtimeDungeonState,
	inventory dnfrepo.InventoryRecord,
	exemptAccountLedgerDrops bool,
) ([]currentDungeonClearRewardDrop, []currentDungeonSettlementExemptDropReceipt, error) {
	drops, exempted, _, err := currentDungeonCommittedDropReceiptsFiltered(
		runtime,
		inventory,
		exemptAccountLedgerDrops,
		false,
		uint16(0),
	)
	return drops, exempted, err
}

// currentDungeonCommittedDropReceiptsFiltered 是收据校验的**唯一实现**。
//
// `exemptAccountLedgerDrops=false` 且 `filterForeignOwned=false` 时逐字节等于改动前
// （两个开关都关掉的那条回退档走的就是这一支）。
//
// `filterForeignOwned=true` 时：一条已消耗掉落只有在它**属于本角色**（或者根本不该有归属）
// 时才参与"必须在背包里找到"的校验，否则记进 foreign 并跳过。归属判据只读掉落自带的
// `OwnerActorObjectKey`（op38 行里的那一个，`owned()` 也用它）。
func currentDungeonCommittedDropReceiptsFiltered(
	runtime *runtimeDungeonState,
	inventory dnfrepo.InventoryRecord,
	exemptAccountLedgerDrops bool,
	filterForeignOwned bool,
	selfActorObjectKey uint16,
	equipment ...dnfrepo.EquipmentRecord,
) (
	[]currentDungeonClearRewardDrop,
	[]currentDungeonSettlementExemptDropReceipt,
	[]currentDungeonSettlementForeignDropReceipt,
	error,
) {
	if runtime == nil || runtime.DropOwner == nil {
		return nil, nil, nil, nil
	}
	// 结算读取共享掉落账本：套一层实例锁，避免与另一个会话的掉落写入并发。
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	bySlot := make(map[uint16]currentDungeonClearRewardDrop)
	exempted := make([]currentDungeonSettlementExemptDropReceipt, 0, 16)
	foreign := make([]currentDungeonSettlementForeignDropReceipt, 0, 16)
	for _, drop := range runtime.DropOwner.byObjectKey {
		if drop == nil || drop.Status != runtimeDungeonDropConsumed {
			continue
		}
		if drop.Item.ItemID == 0 {
			continue
		}
		// 别人的掉落不是本角色的收据：组队副本实例共用一份账本，独立掉落档下
		// 每个对象恰好属于一个人。`PartyShared` / `MapOwnedPassiveObject` 没有
		// "必须是自己"的语义，因此不受本过滤影响。
		if filterForeignOwned && drop.OwnerActorObjectKey != 0 &&
			drop.OwnerActorObjectKey != selfActorObjectKey &&
			!drop.PartyShared && !drop.MapOwnedPassiveObject {
			foreign = append(foreign, currentDungeonSettlementForeignDropReceipt{
				ObjectKey:           drop.ObjectKey,
				SceneSlot:           drop.SceneSlot,
				ItemID:              drop.Item.ItemID,
				DestinationSlot:     drop.DestinationSlot,
				StackCount:          drop.Amount,
				OwnerActorObjectKey: drop.OwnerActorObjectKey,
				Room:                drop.Room,
			})
			continue
		}
		if drop.DestinationSlot == 0 {
			return nil, nil, nil, errCurrentDungeonSettlementStateInvalid
		}
		// 账号级史诗碎片：`destination_slot` 是哨兵、物品在账号账本里，**不进背包**。
		if exemptAccountLedgerDrops && drop.DestinationSlot == currentEpicPiecePickupEffectSlot {
			exempted = append(exempted, currentDungeonSettlementExemptDropReceipt{
				ObjectKey:       drop.ObjectKey,
				SceneSlot:       drop.SceneSlot,
				ItemID:          drop.Item.ItemID,
				DestinationSlot: drop.DestinationSlot,
				StackCount:      drop.Amount,
				AssetOwner:      "account_epic_piece_metadata_counter",
			})
			continue
		}
		if committedDungeonPickupUsedUp(drop, inventory) {
			exempted = append(exempted, currentDungeonSettlementExemptDropReceipt{
				ObjectKey: drop.ObjectKey, SceneSlot: drop.SceneSlot, ItemID: drop.Item.ItemID,
				DestinationSlot: drop.DestinationSlot, StackCount: drop.Amount,
				AssetOwner: "character_inventory_committed_consumption",
			})
			continue
		}
		slot, stack, found := currentDungeonCommittedPickupSlot(drop, inventory)
		if !found {
			if len(equipment) == 1 && currentDungeonCommittedPickupIsEquipped(drop, inventory, equipment[0]) {
				continue
			}
			return nil, nil, nil, errCurrentDungeonSettlementStateInvalid
		}
		bySlot[slot] = currentDungeonClearRewardDrop{
			ObjectKey: slot, TemplateID: drop.Item.ItemID, StackCount: uint32(stack.Count),
		}
	}
	result := make([]currentDungeonClearRewardDrop, 0, len(bySlot))
	for _, drop := range bySlot {
		result = append(result, drop)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ObjectKey < result[j].ObjectKey })
	return result, exempted, foreign, nil
}
