package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	errCurrentDungeonMonsterExperienceUnavailable  = errors.New("current dungeon monster experience is unavailable")
	errCurrentDungeonMonsterExperiencePVFInvalid   = errors.New("current dungeon monster experience PVF source is invalid")
	errCurrentDungeonMonsterExperienceStateInvalid = errors.New("current dungeon monster experience persisted state is invalid")
)

// currentDungeonMonsterExperienceResources intentionally keeps the current
// runtime PVF tables together.  The formula below takes its sequencing and
// rounding from the user-authorized 86JP domain implementation, but never
// copies its stale hard-coded base EXP table or old packet body.
type currentDungeonMonsterExperienceResources struct {
	Progression *progression.Tables
	Sources     *progression.MonsterExperienceSources
}

type currentDungeonMonsterExperienceResourcesCacheEntry struct {
	resources currentDungeonMonsterExperienceResources
}

var currentDungeonMonsterExperienceResourcesByCatalog sync.Map

func currentDungeonMonsterExperienceResourcesForCatalog(
	catalog *pvfDungeonMonsterCatalog,
) (currentDungeonMonsterExperienceResources, error) {
	if catalog == nil || catalog.source == nil {
		return currentDungeonMonsterExperienceResources{}, errCurrentDungeonMonsterExperienceUnavailable
	}
	if cached, found := currentDungeonMonsterExperienceResourcesByCatalog.Load(catalog); found {
		return cached.(currentDungeonMonsterExperienceResourcesCacheEntry).resources, nil
	}
	tables, err := progression.Load(context.Background(), catalog.source)
	if err != nil {
		return currentDungeonMonsterExperienceResources{}, err
	}
	sources, err := currentDungeonMonsterExperienceSources(catalog)
	if err != nil {
		return currentDungeonMonsterExperienceResources{}, err
	}
	resources := currentDungeonMonsterExperienceResources{Progression: tables, Sources: sources}
	actual, _ := currentDungeonMonsterExperienceResourcesByCatalog.LoadOrStore(
		catalog,
		currentDungeonMonsterExperienceResourcesCacheEntry{resources: resources},
	)
	return actual.(currentDungeonMonsterExperienceResourcesCacheEntry).resources, nil
}

// (s *Service) currentDungeonExperienceCommitResources 是**锦标赛通关经验**
// （P10）与**怪物击杀经验**共用的资源装载口：progression 表（升级阈值 / SP·TP /
// 任务经验）与怪物经验源一并取出。
//
// 为什么要有这一个入口：两条提交路径必须复用**同一份**运行时 PVF 表与**同一个**
// 提交实现，否则"升级阈值、经验上限、成长曲线"就会出现第二套。它只是把
// `currentDungeonMonsterExperienceResourcesForCatalog` 的两个字段摊开，
// 不改变任何装载语义（同一个 `sync.Map` 缓存、同一个 `progression.Load`）。
func (s *Service) currentDungeonExperienceCommitResources() (currentDungeonMonsterExperienceResources, error) {
	if s == nil {
		return currentDungeonMonsterExperienceResources{}, errCurrentDungeonMonsterExperienceUnavailable
	}
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return currentDungeonMonsterExperienceResources{}, err
	}
	return currentDungeonMonsterExperienceResourcesForCatalog(catalog)
}

// currentDungeonRuntimeCharacterIsCommitReady 报告"运行时角色状态是可提交的"：
// 会话账号与角色身份自洽、`CharacterID` 与会话选中角色同值、
// `Level` 与 `Stats["exp"]` 都在当前 EXE 的 u32 范围内。
//
// 它**不是** version precondition —— 那是 `progression.Commit` 的事（在事务内用
// 库内 `Level` / `Stats["exp"]` / `skill.Points` 与计划里的 Expected 逐字段比对）。
// 抽出来的用途是让**锦标赛通关经验**在真正进事务之前先问一次，
// 对不上就留下具名事件并拒绝（`reason=runtime_character_state_is_not_commit_ready`），
// 而不是把一个含混的 `errCurrentDungeonMonsterExperienceStateInvalid` 抛给调用方。
func (s *Service) currentDungeonRuntimeCharacterIsCommitReady(
	session *gameSession,
	runtime *runtimeDungeonState,
) bool {
	if s == nil || session == nil || runtime == nil || session.selectedCharacterID == 0 {
		return false
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	experience, present := runtime.Character.Stats["exp"]
	if accountID == "" || runtime.Character.CharacterID != characterID ||
		!present || experience < 0 || uint64(experience) > math.MaxUint32 ||
		runtime.Character.Level <= 0 {
		return false
	}
	return true
}

// currentDungeonMonsterExperienceAward records the exact inputs used for one
// accepted hostile death.  It intentionally omits contract, account honor,
// party, and result-window bonuses: none of those has a current Go owner in
// this single-character in-dungeon path.
type currentDungeonMonsterExperienceAward struct {
	MonsterID       int64
	MonsterLevel    int
	MonsterType     byte
	NamedMonster    bool
	MonsterTableEXP uint32
	DungeonWeight   float32
	DifficultyRate  float32
	MonsterTypeRate float32
	LevelPenalty    float32
	PrePenaltyGain  uint32
	Gain            uint32
}

// currentDungeonMonsterExperienceAwardFor reconstructs only the ordinary
// monster EXP domain rule used by 86JP:
//
//	runtime monsterexp.tbl base -> dungeon weight -> difficulty -> type/named
//	-> killer level penalty.
//
// Each cast is deliberately performed at the same boundaries as the C# float
// to int/uint casts.  The base table is the current runtime PVF table because
// the 86JP static table is known to differ from this server's Script.pvf.
func currentDungeonMonsterExperienceAwardFor(
	runtime *runtimeDungeonState,
	monster runtimeDungeonMonster,
	sources *progression.MonsterExperienceSources,
) (currentDungeonMonsterExperienceAward, error) {
	if runtime == nil || sources == nil || runtime.Character.Level <= 0 || monster.Spawn.MonsterID <= 0 {
		return currentDungeonMonsterExperienceAward{}, errCurrentDungeonMonsterExperienceUnavailable
	}
	monsterLevel, err := currentDungeonMonsterLevel(monster.Spawn, runtime.Dungeon.Metadata.BasisLevel)
	if err != nil {
		return currentDungeonMonsterExperienceAward{}, err
	}
	monsterType, err := currentDungeonMonsterType(monster.Spawn.Rank)
	if err != nil {
		return currentDungeonMonsterExperienceAward{}, err
	}
	base, found := sources.MonsterTableValue(int(monsterLevel))
	if !found {
		return currentDungeonMonsterExperienceAward{}, fmt.Errorf(
			"%w: monster level=%d is absent from monsterexp.tbl",
			errCurrentDungeonMonsterExperiencePVFInvalid,
			monsterLevel,
		)
	}

	weight := float32(1)
	if runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Set &&
		runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value >= 0 {
		weight = float32(runtime.Dungeon.Metadata.ExperienceIncreasingPoint.Value)
	}
	weight *= float32(currentDungeonStoryExperienceMultiplier(runtime))
	if !currentDungeonMonsterExperienceFiniteNonNegative(weight) {
		return currentDungeonMonsterExperienceAward{}, fmt.Errorf(
			"%w: dungeon weight=%v",
			errCurrentDungeonMonsterExperiencePVFInvalid,
			weight,
		)
	}

	modifiers := sources.RawModifiers()
	difficultyRate := float32(1) // exact 86JP fallback for an out-of-range index.
	difficulty := int(runtime.Request.Difficulty)
	if difficulty >= 0 && difficulty < len(modifiers.DifficultyRates) {
		difficultyRate = float32(modifiers.DifficultyRates[difficulty])
	}
	if !currentDungeonMonsterExperienceFiniteNonNegative(difficultyRate) {
		return currentDungeonMonsterExperienceAward{}, fmt.Errorf(
			"%w: difficulty=%d rate=%v",
			errCurrentDungeonMonsterExperiencePVFInvalid,
			difficulty,
			difficultyRate,
		)
	}

	monsterTypeRate := currentDungeonMonsterCSharpTypeRate(modifiers.MonsterGlobalRates, int(monsterType))
	if !currentDungeonMonsterExperienceFiniteNonNegative(monsterTypeRate) {
		return currentDungeonMonsterExperienceAward{}, fmt.Errorf(
			"%w: monster type=%d rate=%v",
			errCurrentDungeonMonsterExperiencePVFInvalid,
			monsterType,
			monsterTypeRate,
		)
	}
	named := monsterType != 3 && currentDungeonMonsterIsNamed(runtime, monster.Spawn.MonsterID)
	if named {
		monsterTypeRate *= 3
		if !currentDungeonMonsterExperienceFiniteNonNegative(monsterTypeRate) {
			return currentDungeonMonsterExperienceAward{}, fmt.Errorf(
				"%w: named monster type rate=%v",
				errCurrentDungeonMonsterExperiencePVFInvalid,
				monsterTypeRate,
			)
		}
	}

	weightedBase, err := currentDungeonMonsterExperienceCSharpInt(float32(base) * weight)
	if err != nil {
		return currentDungeonMonsterExperienceAward{}, err
	}
	prePenalty, err := currentDungeonMonsterExperienceCSharpInt(
		float32(weightedBase) * difficultyRate * monsterTypeRate,
	)
	if err != nil {
		return currentDungeonMonsterExperienceAward{}, err
	}
	levelPenalty := currentDungeonMonsterCSharpBaseExpPenalty(runtime.Character.Level, int(monsterLevel))
	gain, err := currentDungeonMonsterExperienceCSharpUint(float32(prePenalty) * levelPenalty)
	if err != nil {
		return currentDungeonMonsterExperienceAward{}, err
	}
	return currentDungeonMonsterExperienceAward{
		MonsterID:       monster.Spawn.MonsterID,
		MonsterLevel:    int(monsterLevel),
		MonsterType:     monsterType,
		NamedMonster:    named,
		MonsterTableEXP: base,
		DungeonWeight:   weight,
		DifficultyRate:  difficultyRate,
		MonsterTypeRate: monsterTypeRate,
		LevelPenalty:    levelPenalty,
		PrePenaltyGain:  prePenalty,
		Gain:            gain,
	}, nil
}

func currentDungeonMonsterIsNamed(runtime *runtimeDungeonState, monsterID int64) bool {
	if runtime == nil || monsterID <= 0 {
		return false
	}
	for _, namedID := range runtime.Dungeon.Metadata.NamedMonsters {
		if namedID == monsterID {
			return true
		}
	}
	return false
}

// currentDungeonMonsterCSharpTypeRate preserves the 86JP range fallback:
// missing or out-of-range monster-type entries use index zero, and an empty
// rate list is neutral.  Current runtime PVF has one entry, but do not invent
// distinct rank rates where its typed source does not provide them.
func currentDungeonMonsterCSharpTypeRate(values []float64, monsterType int) float32 {
	if len(values) == 0 {
		return 1
	}
	if monsterType < 0 || monsterType >= len(values) {
		return float32(values[0])
	}
	return float32(values[monsterType])
}

// currentDungeonMonsterCSharpBaseExpPenalty is the authorized 86JP domain
// rule (MonsterRewardTable.BaseExpPenalty), including its truncation inputs.
// It is not a guessed interpretation of worldmapexppenaltytable.etc.
func currentDungeonMonsterCSharpBaseExpPenalty(characterLevel, monsterLevel int) float32 {
	difference := monsterLevel - characterLevel
	if difference <= -7 {
		return 0.05
	}
	switch difference {
	case -6:
		return 0.20
	case -5:
		return 0.50
	case -4:
		return 0.75
	case -3, -2, -1, 0:
		return 1
	case 1, 2, 3:
		return 1.12
	case 4, 5:
		return 1
	case 6:
		return 0.75
	case 7:
		return 0.70
	case 8:
		return 0.60
	case 9:
		return 0.50
	default:
		return 0.05
	}
}

func currentDungeonMonsterExperienceFiniteNonNegative(value float32) bool {
	return !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0) && value >= 0
}

// C# CalcBaseExp and CalcExp both cast their non-negative float result to
// Int32.  Rejecting an out-of-range current PVF product is safer than allowing
// Go's conversion semantics to create a fabricated reward.
func currentDungeonMonsterExperienceCSharpInt(value float32) (uint32, error) {
	if !currentDungeonMonsterExperienceFiniteNonNegative(value) || value > math.MaxInt32 {
		return 0, fmt.Errorf("%w: C# int product=%v", errCurrentDungeonMonsterExperiencePVFInvalid, value)
	}
	return uint32(value), nil
}

// C# applies BaseExpPenalty after CalcExp and then casts the product to UInt32.
func currentDungeonMonsterExperienceCSharpUint(value float32) (uint32, error) {
	if !currentDungeonMonsterExperienceFiniteNonNegative(value) || value > math.MaxUint32 {
		return 0, fmt.Errorf("%w: C# uint product=%v", errCurrentDungeonMonsterExperiencePVFInvalid, value)
	}
	return uint32(value), nil
}

type currentDungeonMonsterExperienceCommitResult struct {
	Character  dnfrepo.CharacterRecord
	Skill      dnfrepo.SkillPointLedger
	Award      currentDungeonMonsterExperienceAward
	SPGain     int
	TPGain     int
	Applied    bool
	Idempotent bool
	// GrowthContractBonus is the 成长契约 (premium type 84) +20% bonus
	// experience added on top of Award.Gain at commit time.
	GrowthContractBonus   uint32
	GuildBonus            uint32
	AttendanceMarbleBonus uint32
	// HonorExpertGain is the accepted part of this award earned after the
	// DNF90 character-level cap.
	HonorExpertGain         uint32
	AdventureExperienceGain uint32
}

// recordCurrentDungeonMonsterExperience owns result-window statistics only.
// Monster experience is committed per accepted death and must not be granted
// again while building the settlement packet.
func recordCurrentDungeonMonsterExperience(
	runtime *runtimeDungeonState,
	monster runtimeDungeonMonster,
	result currentDungeonMonsterExperienceCommitResult,
) {
	if runtime == nil || result.Award.MonsterID <= 0 {
		return
	}
	base := result.Award.PrePenaltyGain
	runtime.settlementMonsterExperienceTotal = saturatingCurrentDungeonUint32Add(
		runtime.settlementMonsterExperienceTotal,
		base,
	)
	runtime.settlementMonsterGrowthContractBonus = saturatingCurrentDungeonUint32Add(
		runtime.settlementMonsterGrowthContractBonus,
		result.GrowthContractBonus,
	)
	runtime.settlementMonsterAttendanceMarbleBonus = saturatingCurrentDungeonUint32Add(runtime.settlementMonsterAttendanceMarbleBonus, result.AttendanceMarbleBonus)
	actorType, err := currentDungeonMonsterActorType(monster.Spawn)
	if err != nil {
		actorType = result.Award.MonsterType
	}
	switch actorType {
	case 1:
		runtime.settlementChampionExperience = saturatingCurrentDungeonUint32Add(
			runtime.settlementChampionExperience,
			base,
		)
	case 2:
		if !result.Award.NamedMonster {
			runtime.settlementSuperChampionExperience = saturatingCurrentDungeonUint32Add(
				runtime.settlementSuperChampionExperience,
				base,
			)
		}
	case 3:
		runtime.settlementBossExperience = saturatingCurrentDungeonUint32Add(
			runtime.settlementBossExperience,
			base,
		)
	}
}

// awardCurrentDungeonMonsterExperience commits only after the room's
// authoritative actor-death transition has succeeded.  The room owner makes
// that transition one-time for the active dungeon runtime; the progression
// UoW then keeps EXP/level and the SP ledger indivisible.
func (s *Service) awardCurrentDungeonMonsterExperience(
	ctx context.Context,
	session *gameSession,
	runtime *runtimeDungeonState,
	monster runtimeDungeonMonster,
) (currentDungeonMonsterExperienceCommitResult, error) {
	if s == nil || session == nil || runtime == nil || session.selectedCharacterID == 0 {
		return currentDungeonMonsterExperienceCommitResult{}, errCurrentDungeonMonsterExperienceUnavailable
	}
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	resources, err := currentDungeonMonsterExperienceResourcesForCatalog(catalog)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	award, err := currentDungeonMonsterExperienceAwardFor(runtime, monster, resources.Sources)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	result := currentDungeonMonsterExperienceCommitResult{Award: award}
	if award.Gain == 0 {
		return result, nil
	}
	return s.commitCurrentDungeonMonsterExperience(ctx, session, runtime, resources.Progression, result)
}

// commitCurrentDungeonMonsterExperience 是**怪物击杀经验**那一条提交入口。
// 它只负责"这一次击杀的经验该是多少"（`result.Award` 已由
// `currentDungeonMonsterExperienceAwardFor` 算好）以及成长契约加成，
// 真正的等级/经验溢出/SP·TP/名誉 Expert 处理全部在
// `commitCurrentDungeonExperience`（两条路径共用的唯一提交实现）。
//
// 本次抽取是**纯搬运**：调用序列、参数、返回结构逐字段不变。
func (s *Service) commitCurrentDungeonMonsterExperience(
	ctx context.Context,
	session *gameSession,
	runtime *runtimeDungeonState,
	tables *progression.Tables,
	result currentDungeonMonsterExperienceCommitResult,
) (currentDungeonMonsterExperienceCommitResult, error) {
	result, err := s.prepareCurrentDungeonMonsterExperienceBonuses(ctx, session, runtime, result)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	return s.commitCurrentDungeonExperience(ctx, session, runtime, tables, result)
}

// 在接受击杀时固定各项加成，连续请求稍后提交时仍使用该次击杀的资格和取整。
func (s *Service) prepareCurrentDungeonMonsterExperienceBonuses(
	ctx context.Context,
	session *gameSession,
	runtime *runtimeDungeonState,
	result currentDungeonMonsterExperienceCommitResult,
) (currentDungeonMonsterExperienceCommitResult, error) {
	// 成长契约 (premium type 84): +20% monster kill experience while active.
	// The total is what gets planned and committed; the base Gain stays the
	// pure PVF/86JP monster award for logging and tests.
	growthBonus := s.currentGrowthContractBonusExp(ctx, strings.TrimSpace(s.accountIDForSession(session)), result.Award.Gain)
	result.GrowthContractBonus = growthBonus
	guildBonus, err := s.currentGuildMonsterExperienceBonus(ctx, session, result.Award.Gain)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	// Both bonuses use base EXP, and their sum must fit the native u32 award.
	result.GuildBonus = min(guildBonus, math.MaxUint32-result.Award.Gain-growthBonus)
	marbleBonus := currentAttendanceMarbleMonsterExperienceBonus(runtime.attendanceMarbleBuffs, result.Award.Gain)
	result.AttendanceMarbleBonus = min(marbleBonus, math.MaxUint32-result.Award.Gain-result.GrowthContractBonus-result.GuildBonus)
	return result, nil
}

// commitCurrentDungeonExperience 是怪物与锦标赛的单笔经验入口；连续击杀
// 通过相同的planCurrentDungeonExperience及commitCurrentDungeonExperiencePlan集中提交：
//
//	① 怪物击杀经验（`commitCurrentDungeonMonsterExperience`，调用方已经算好
//	   `result.Award.Gain` 与成长契约加成）；
//	② 锦标赛通关经验（`commitCurrentTournamentClearRewardExperience`，
//	   `result.Award.Gain` 就是真档 `[tournament clear reward exp]` 的
//	   `CompletedRounds` 对应值，成长契约加成恒为 0 —— 厂商
//	   `CharacterExperienceCommitService.TryCommitTournamentExperience`
//	   把 `rawGain` 原样交给 `CharacterExperienceService.GrantInTransaction`，
//	   不乘任何加成）。
//
// 等级推进 / 经验上限（`planCurrentDungeonSettlementProgression` 的
// `currentAdventureCharacterLevelCap`）/ 名誉 Expert 溢出
// （`currentHonorExpertExperienceGain`）/ SP·TP 账本
// （`progression.Commit` 走角色 progression 仓储的**单一事务**）一律复用；
// 满级部分在共享规划内分流，不再同时增加普通角色EXP。
//
// version precondition：`progression.Commit` 用 `character.Level` / `Stats["exp"]` /
// `skill.Points` 做乐观校验，只有"库内状态 == 调用方看到的状态"时才会写。
// 因此调用方必须让库内状态与 `runtime.Character` 一致（两条路径都在同一处
// 前置校验里保证了这一点）。
func (s *Service) commitCurrentDungeonExperience(
	ctx context.Context,
	session *gameSession,
	runtime *runtimeDungeonState,
	tables *progression.Tables,
	result currentDungeonMonsterExperienceCommitResult,
) (currentDungeonMonsterExperienceCommitResult, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Skill == nil ||
		repositories.CharacterProgression == nil || tables == nil || result.Award.Gain == 0 {
		return currentDungeonMonsterExperienceCommitResult{}, errCurrentDungeonMonsterExperienceUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" || runtime.Character.CharacterID != characterID {
		return currentDungeonMonsterExperienceCommitResult{}, errCurrentDungeonMonsterExperienceStateInvalid
	}
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	if !found || character.CharacterID != characterID || strings.TrimSpace(character.AccountID) != accountID {
		return currentDungeonMonsterExperienceCommitResult{}, errCurrentDungeonMonsterExperienceStateInvalid
	}
	skill, found, err := loadCurrentDungeonExperienceSkillPoints(ctx, repositories.Skill, characterID)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	if !found || skill.CharacterID != characterID {
		return currentDungeonMonsterExperienceCommitResult{}, errCurrentDungeonMonsterExperienceStateInvalid
	}
	experience, present := character.Stats["exp"]
	runtimeExperience, runtimePresent := runtime.Character.Stats["exp"]
	if !present || !runtimePresent || experience < 0 || uint64(experience) > math.MaxUint32 ||
		character.Level <= 0 || character.Level != runtime.Character.Level || experience != runtimeExperience ||
		skill.Points.SyncedLevel != character.Level {
		return currentDungeonMonsterExperienceCommitResult{}, errCurrentDungeonMonsterExperienceStateInvalid
	}
	result, commit, err := s.planCurrentDungeonExperience(ctx, tables, character, skill, result)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	committed, err := s.commitCurrentDungeonExperiencePlanTimed(ctx, session, repositories, commit, []uint32{result.HonorExpertGain})
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, err
	}
	result.Applied, result.Idempotent = committed.Applied, committed.Idempotent
	if s.adventureGroupTable.Runtime().Capsule.MaximumExperience > 0 {
		result.AdventureExperienceGain = result.HonorExpertGain
	}
	return result, nil
}

// 只规划内存快照，不写库或发包；批次与单只入口共用同一套经验及熟练度规则。
func (s *Service) planCurrentDungeonExperience(ctx context.Context, tables *progression.Tables,
	character dnfrepo.CharacterRecord, skill dnfrepo.SkillPointLedger, result currentDungeonMonsterExperienceCommitResult,
) (currentDungeonMonsterExperienceCommitResult, progression.CommitPlan, error) {
	totalGain := result.Award.Gain + result.GrowthContractBonus + result.GuildBonus + result.AttendanceMarbleBonus
	honorExpertGain, err := currentHonorExpertExperienceGain(
		tables,
		character.Level,
		statU32(character, "exp", 0),
		totalGain,
	)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, progression.CommitPlan{}, err
	}
	// 满级部分只进入熟练度和成长胶囊。当前客户端用op37普通EXP差值
	// 生成系统栏提示；保持满级普通EXP不变，也避免历史超量EXP被回退。
	planned, err := planCurrentDungeonSettlementProgression(
		tables,
		character.Level,
		statU32(character, "exp", 0),
		totalGain-honorExpertGain,
		skill.Points,
	)
	if err != nil {
		return currentDungeonMonsterExperienceCommitResult{}, progression.CommitPlan{}, err
	}
	commit := progression.CommitPlanFromExperienceSkillPoints(character.CharacterID, character.AccountID, planned)
	if planned.Experience.LevelsGained > 0 {
		levelStats, statErr := s.characterPVFStatValuesForLevel(character, planned.Experience.NewLevel)
		if statErr != nil {
			return currentDungeonMonsterExperienceCommitResult{}, progression.CommitPlan{}, statErr
		}
		commit.NextCharacterStats = levelStats
	}
	if honorExpertGain > 0 {
		honorTables, honorErr := s.loadHonorTable(ctx)
		if honorErr != nil {
			return currentDungeonMonsterExperienceCommitResult{}, progression.CommitPlan{}, honorErr
		}
		currentHonor, honorErr := currentHonorExpertProgress(character)
		if honorErr != nil {
			return currentDungeonMonsterExperienceCommitResult{}, progression.CommitPlan{}, honorErr
		}
		nextHonor, honorErr := planCurrentHonorExpertProgress(honorTables, character, honorExpertGain)
		if honorErr != nil {
			return currentDungeonMonsterExperienceCommitResult{}, progression.CommitPlan{}, honorErr
		}
		commit.ExpectedCharacterStats = currentHonorExpertStats(currentHonor)
		if commit.NextCharacterStats == nil {
			commit.NextCharacterStats = make(map[string]int64, 2)
		}
		for key, value := range currentHonorExpertStats(nextHonor) {
			commit.NextCharacterStats[key] = value
		}
		result.HonorExpertGain = honorExpertGain
	}
	if commit.Expected == commit.Next {
		// EXP标量及熟练度都可能封顶，胶囊仍能在领取后继续累计。
		// 为该次规划保留受事务保护的版本，避免把新击杀误判成重放。
		revision := character.Stats[currentDungeonCapAwardRevisionStatKey]
		if revision < 0 || revision == math.MaxInt64 {
			return currentDungeonMonsterExperienceCommitResult{}, progression.CommitPlan{}, errCurrentDungeonMonsterExperienceStateInvalid
		}
		if commit.ExpectedCharacterStats == nil {
			commit.ExpectedCharacterStats = make(map[string]int64)
		}
		if commit.NextCharacterStats == nil {
			commit.NextCharacterStats = make(map[string]int64)
		}
		commit.ExpectedCharacterStats[currentDungeonCapAwardRevisionStatKey] = revision
		commit.NextCharacterStats[currentDungeonCapAwardRevisionStatKey] = revision + 1
	}
	character = dnfrepo.CloneCharacter(character)
	if character.Stats == nil {
		return currentDungeonMonsterExperienceCommitResult{}, progression.CommitPlan{}, errCurrentDungeonMonsterExperienceStateInvalid
	}
	character.Level = commit.Next.Level
	character.Stats["exp"] = int64(commit.Next.Experience)
	for key, value := range commit.NextCharacterStats {
		character.Stats[key] = value
	}
	// 技能点投影只含值字段，已随参数独立复制；不生成完整技能快照。
	skill.Points = commit.Next.SkillPoints
	result.Character = character
	result.Skill = skill
	result.SPGain = planned.SkillPoints.SPGain
	result.TPGain = planned.SkillPoints.TPGain
	return result, commit, nil
}

// currentGrowthContractEffect resolves the live account entitlement and the
// effect values from the active runtime PVF. A missing account, expired
// contract, or unavailable PVF catalog is inactive rather than inventing a
// compatibility multiplier.
func (s *Service) currentGrowthContractEffect(ctx context.Context, accountID string) (currentPremiumEffectInfo, bool) {
	if s == nil || strings.TrimSpace(accountID) == "" {
		return currentPremiumEffectInfo{}, false
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil {
		return currentPremiumEffectInfo{}, false
	}
	account, found, err := loadCurrentGrowthContractAccount(ctx, repositories.Account, strings.TrimSpace(accountID))
	if err != nil || !found {
		return currentPremiumEffectInfo{}, false
	}
	if !premium.Active(account, premium.TypeBonusExp, time.Now().UTC()) {
		return currentPremiumEffectInfo{}, false
	}
	catalog, err := s.currentPremiumCatalog()
	if err != nil || catalog == nil {
		return currentPremiumEffectInfo{}, false
	}
	effect, found := catalog.effectsByType[premium.TypeBonusExp]
	if !found {
		return currentPremiumEffectInfo{}, false
	}
	return effect, true
}

// 每次仍读取最新到期时间，续费和到期即时生效；不引入跨击杀的资格缓存。
func loadCurrentGrowthContractAccount(ctx context.Context, accounts dnfrepo.AccountRepository, accountID string) (dnfrepo.AccountRecord, bool, error) {
	if reader, ok := accounts.(dnfrepo.AccountMetadataEntryReader); ok {
		key := premium.MetadataKey(premium.TypeBonusExp)
		value, found, err := reader.LoadMetadataEntry(ctx, accountID, key)
		if err != nil || !found {
			return dnfrepo.AccountRecord{}, false, err
		}
		return dnfrepo.AccountRecord{AccountID: accountID, Metadata: map[string]string{key: value}}, true, nil
	}
	return accounts.Load(ctx, accountID)
}

// currentGrowthContractBonusExp returns the runtime-PVF [bonus exp]
// percentage while Growth Contract is active. The bonus saturates so the
// committed base+bonus never overflows the current-client u32 experience
// field.
func (s *Service) currentGrowthContractBonusExp(ctx context.Context, accountID string, base uint32) uint32 {
	if base == 0 {
		return 0
	}
	effect, active := s.currentGrowthContractEffect(ctx, accountID)
	if !active {
		return 0
	}
	return currentPercentageBonusUint32(base, effect.BonusExperiencePercent)
}

func currentPercentageBonusUint32(base uint32, percent int64) uint32 {
	if base == 0 || percent <= 0 {
		return 0
	}
	limit := uint64(math.MaxUint32 - base)
	if uint64(percent) > math.MaxUint64/uint64(base) {
		return uint32(limit)
	}
	bonus := uint64(base) * uint64(percent) / 100
	if bonus > limit {
		bonus = limit
	}
	return uint32(bonus)
}
