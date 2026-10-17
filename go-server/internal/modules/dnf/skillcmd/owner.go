package skillcmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

var (
	ErrOwnerUnavailable               = errors.New("skill owner unavailable")
	ErrCharacterRequired              = errors.New("selected character id required")
	ErrCharacterNotFound              = errors.New("character record not found")
	ErrCharacterOwner                 = errors.New("selected character does not belong to account")
	ErrSkillRulesMissing              = errors.New("skill PVF rules are unavailable")
	ErrSkillNotFound                  = errors.New("job-scoped skill is not present in PVF")
	ErrSkillDelta                     = errors.New("skill level delta is invalid")
	ErrSkillLevel                     = errors.New("skill level rule rejected mutation")
	ErrSkillGrowType                  = errors.New("skill grow type rule rejected mutation")
	ErrSkillPrerequisite              = errors.New("skill prerequisite rule rejected mutation")
	ErrSkillPointLedger               = errors.New("skill point ledger is invalid or stale")
	ErrSkillPoints                    = errors.New("insufficient skill points")
	ErrSkillTree                      = errors.New("skill tree is not proven by current EXE evidence")
	ErrSkillTreeAlreadyExpanded       = errors.New("角色已经购买技能类型扩展")
	ErrSkillTreeUnavailable           = fmt.Errorf("%w: alternate skill tree is unavailable", ErrSkillTree)
	ErrSkillTreeStateMismatch         = errors.New("skill tree switch current index is stale")
	ErrSkillSlot                      = errors.New("skill UI slot allocation failed")
	ErrSkillSlotContext               = errors.New("skill slot context is outside the current EXE writer range")
	ErrSkillSlotMode                  = errors.New("skill slot mode is outside the current EXE writer values")
	ErrSkillRefundUnavailable         = errors.New("skill refund requires the settlement transaction boundary")
	ErrSkillRefundConsumableRequired  = errors.New("skill refund requires one 遗忘河之水 (item 3) in the main inventory")
	ErrSkillCommandStorageUnavailable = errors.New("skill command storage is unavailable")
	ErrSkillCommandEmpty              = errors.New("skill command customization carried no records")
	ErrSkillCommandUnlearned          = errors.New("skill command targets a skill the character has not learned")
	ErrSkillCommandTooLong            = errors.New("skill command byte vector is too long")
)

// maxSkillCommandBytes bounds one op331 command vector. Live current EXE
// writers produce 1..5 bytes per record (see the 14 distinct HK captures).
const maxSkillCommandBytes = 32

const (
	currentEXEProvenBuySkillTree byte = 0
	currentEXESkillSlotCount          = 204
	currentEXEReservedSlotStart       = 138
	currentEXEReservedSlotEnd         = 150
	currentEXESkillTreeCount          = 2
	currentEXESkillTreeIndexStat      = "skill_tree_index"

	// forgetRiverWaterItemID is the 遗忘河之水 skill-reset consumable
	// (stackable/cash/river_lethe.stk, 86JP ForgetRiverWaterItemTemplateId).
	forgetRiverWaterItemID int64 = 3
)

// OverSkillLevelBonus is how many character levels the 达人契约 (premium type 27)
// lets a skill be learned early. Every learnable-cap decision must add it while
// the contract is active — including the dnfbridge login cap repair, which
// otherwise clamps (and refunds) the levels the contract legitimately bought.
const OverSkillLevelBonus = 5

type Owner struct {
	characters dnfrepo.CharacterRepository
	skills     dnfrepo.SkillRepository
	skillTx    dnfrepo.CharacterSkillUnitOfWork
	progressTx dnfrepo.CharacterProgressionUnitOfWork
	accounts   dnfrepo.AccountRepository
	settlement dnfrepo.CharacterSettlementUnitOfWork
	// settings owns the per-skill custom command vector (op331/op332), which
	// has no column in the skill record and is stored in the settings family.
	settings dnfrepo.SettingsRepository
	catalog  *dnfskill.Table
	initial  map[uint16]int
	baseline *dnfrepo.SkillPointState
}

type OwnerOptions struct {
	Catalog       *dnfskill.Table
	InitialLevels map[uint16]int
	PointBaseline *dnfrepo.SkillPointState
}

type PlanResult struct {
	AccountID         string
	CharacterID       string
	Operation         string
	Known             bool
	SkillCount        int
	CooldownCount     int
	RequestedSkillIDs []int64
	RefundCount       int
}

type MutationEntry struct {
	SkillID     uint16
	Level       int
	TP          bool
	Slot        int
	CommandData []byte
}

type MutationResult struct {
	CharacterID string
	SkillTree   byte
	FinalMode   byte
	Points      dnfrepo.SkillPointState
	Entries     []MutationEntry
	// ConsumedRefundItem marks that the batch unlearned skills and consumed
	// one 遗忘河之水 from ConsumedRefundItemSlot (main list) in the same
	// transaction. False when the refund is free (默认档 or 遗忘河水契约).
	ConsumedRefundItem     bool
	ConsumedRefundItemSlot int16
	// AppliedRefund marks that at least one entry actually lowered a learned
	// skill's level in this batch. The op29 ACK only carries the batch entries,
	// so the bridge needs this to push the op19 skill-page refresh that makes
	// the client redraw the new (lower) level.
	AppliedRefund bool
	// ExpiredContractSkillsReset marks that the 达人契约 expiry sweep reset
	// over-level skills (and broken dependents) before this batch; the client
	// needs a full skill refresh to see the removals.
	ExpiredContractSkillsReset bool
}

type SlotMutationResult struct {
	CharacterID string
	SkillTree   byte
	From        byte
	To          byte
	FromSkillID uint16
	ToSkillID   uint16
	ToOccupied  bool
}

type ResetMutationResult struct {
	CharacterID string
	SkillTree   byte
	Mode        byte
	SkillCount  int
	Points      dnfrepo.SkillPointState
}

type TreeSwitchMutationResult struct {
	CharacterID string
	Current     byte
	Target      byte
}

func NewOwner(repos dnfrepo.Group, options ...OwnerOptions) (*Owner, error) {
	if repos.Character == nil || repos.Skill == nil {
		return nil, ErrOwnerUnavailable
	}
	owner := &Owner{
		characters: repos.Character,
		skills:     repos.Skill,
		skillTx:    repos.CharacterSkills,
		progressTx: repos.CharacterProgression,
		accounts:   repos.Account,
		settlement: repos.CharacterSettlement,
		settings:   repos.Settings,
	}
	if len(options) > 0 {
		owner.catalog = options[0].Catalog
		owner.initial = cloneInitialLevels(options[0].InitialLevels)
		owner.baseline = clonePointBaseline(options[0].PointBaseline)
	}
	return owner, nil
}

// ApplyTreeSwitch validates the live request's current tree against the
// persisted selected-tree index and commits the opposite index before the
// op260 success response is exposed. CharacterProgression is used so this
// write serializes with skill/SP/TP mutations for the same character.
func (o *Owner) ApplyTreeSwitch(ctx context.Context, cmd Command) (TreeSwitchMutationResult, error) {
	if o == nil || o.characters == nil || o.skills == nil || o.progressTx == nil {
		return TreeSwitchMutationResult{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return TreeSwitchMutationResult{}, ErrCharacterRequired
	}
	if cmd.SkillTree >= currentEXESkillTreeCount {
		return TreeSwitchMutationResult{}, fmt.Errorf("%w: request current=%d", ErrSkillTree, cmd.SkillTree)
	}

	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	result := TreeSwitchMutationResult{CharacterID: characterID}
	err := o.progressTx.WithinCharacterProgression(ctx, characterID, func(characters dnfrepo.CharacterRepository, skills dnfrepo.SkillRepository) error {
		character, ok, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrCharacterNotFound
		}
		if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
			return ErrCharacterOwner
		}

		record, found, err := skills.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || !record.Trees.Expanded {
			return ErrSkillTreeUnavailable
		}
		if err := validateActiveSkillTree(record, cmd.SkillTree); err != nil {
			return err
		}
		if len(record.Trees.Other.Skills) == 0 {
			return ErrSkillRulesMissing
		}
		record = dnfrepo.CloneSkill(record)
		if o.baseline != nil {
			points, _, err := dnfskill.SyncPointState(record.Points, *o.baseline)
			if err != nil {
				return err
			}
			record.Points = points
		}
		if _, err := SyncOtherSkillPoints(&record); err != nil {
			return err
		}
		previous := dnfrepo.SkillProfile{Skills: record.Skills, Points: record.Points}
		record.Skills, record.Points = record.Trees.Other.Skills, record.Trees.Other.Points
		record.Trees.Other = previous
		record.Trees.Active = cmd.SkillTree ^ 1
		record.UpdatedAt = time.Now().UTC()
		if err := dnfrepo.SaveSkillFields(ctx, skills, record, dnfrepo.SkillFieldSkills, dnfrepo.SkillFieldPoints, dnfrepo.SkillFieldTrees); err != nil {
			return err
		}

		target := cmd.SkillTree ^ 1
		character = dnfrepo.CloneCharacter(character)
		if character.Stats == nil {
			character.Stats = make(map[string]int64)
		}
		character.Stats[currentEXESkillTreeIndexStat] = int64(target)
		character.UpdatedAt = time.Now().UTC()
		if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		result.Current = cmd.SkillTree
		result.Target = target
		return nil
	})
	if err != nil {
		return TreeSwitchMutationResult{}, err
	}
	return result, nil
}

// premiumActive reports whether the account currently holds an active premium
// contract of the given type. It is a read-only gate: the worst case of a
// stale read is one purchase against a just-expired contract, matching the
// 86JP lazy-expiry model. A missing account repository (tests) means inactive.
func (o *Owner) premiumActive(ctx context.Context, accountID string, premiumType int64) bool {
	if o == nil || o.accounts == nil || strings.TrimSpace(accountID) == "" {
		return false
	}
	account, ok, err := o.accounts.Load(ctx, accountID)
	if err != nil || !ok {
		return false
	}
	return premium.Active(account, premiumType, time.Now().UTC())
}

// sweepExpiredOverSkill resets skills that could only have been learned while
// the 达人契约 was active: with the contract inactive, every learned skill
// whose PVF required level exceeds the raw character level is returned to its
// PVF initial floor with a full SP/TP refund of the levels above the floor.
// Dependents whose prerequisites were broken by a removal are cascaded out
// the same way. It returns true when any skill changed so the handler can
// schedule a full skill refresh.
func (o *Owner) sweepExpiredOverSkill(ctx context.Context, cmd Command, character dnfrepo.CharacterRecord, job byte) (bool, error) {
	if o == nil || o.skillTx == nil || o.catalog == nil {
		return false, nil
	}
	if o.premiumActive(ctx, character.AccountID, premium.TypeOverSkill) {
		return false, nil
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	characterLevel := int(character.Level)
	swept := false
	err := o.skillTx.WithinCharacterSkill(ctx, characterID, func(repo dnfrepo.SkillRepository) error {
		record, exists, err := repo.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !exists || len(record.Skills) == 0 {
			return nil
		}
		if err := validateActiveSkillTree(record, cmd.SkillTree); err != nil {
			return err
		}
		record = dnfrepo.CloneSkill(record)
		changed := false
		// Removing one skill can break another's prerequisite, so rescan until
		// nothing is over-level and nothing is broken.
		for {
			offender := int64(-1)
			for rawID, state := range record.Skills {
				if state.Level <= 0 || rawID < 0 || rawID > 0xffff {
					continue
				}
				definition, ok := o.catalog.Find(job, uint16(rawID))
				if !ok {
					continue
				}
				if int(definition.RequiredLevel) > characterLevel || skillPrerequisitesBroken(record.Skills, definition) {
					offender = rawID
					break
				}
			}
			if offender < 0 {
				break
			}
			if err := o.unlearnSkillToFloor(&record, job, uint16(offender)); err != nil {
				return err
			}
			changed = true
		}
		if !changed {
			return nil
		}
		if record.Points.RemainingSP > record.Points.TotalSP {
			record.Points.RemainingSP = record.Points.TotalSP
		}
		if record.Points.RemainingTP > record.Points.TotalTP {
			record.Points.RemainingTP = record.Points.TotalTP
		}
		record.UpdatedAt = time.Now().UTC()
		if err := dnfrepo.SaveSkillFields(ctx, repo, record, dnfrepo.SkillFieldSkills, dnfrepo.SkillFieldPoints, dnfrepo.SkillFieldLayouts); err != nil {
			return err
		}
		swept = true
		return nil
	})
	return swept, err
}

func skillPrerequisitesBroken(states map[int64]dnfrepo.SkillState, definition dnfskill.Skill) bool {
	for _, prerequisite := range definition.Prerequisites {
		if states[int64(prerequisite.SkillID)].Level < prerequisite.Level {
			return true
		}
	}
	return false
}

// unlearnSkillToFloor refunds one skill down to its PVF initial level (or
// fully unlearns it when the floor is zero) and releases its quickbar slot.
func (o *Owner) unlearnSkillToFloor(record *dnfrepo.SkillRecord, job byte, skillID uint16) error {
	definition, ok := o.catalog.Find(job, skillID)
	if !ok {
		return nil
	}
	state := record.Skills[int64(skillID)]
	floor := 0
	if o.initial != nil && o.initial[skillID] > 0 {
		floor = o.initial[skillID]
	}
	if state.Level <= floor {
		return nil
	}
	pointDelta, err := mutationPointDelta(definition, state.Level, floor)
	if err != nil {
		return err
	}
	if definition.IsTPSkill() {
		record.Points.RemainingTP -= pointDelta
	} else {
		record.Points.RemainingSP -= pointDelta
	}
	if floor == 0 {
		delete(record.Skills, int64(skillID))
		for tree, layout := range record.Layouts {
			if tree != dnfrepo.ActiveSkillTree(*record) {
				continue
			}
			for slot, layoutID := range layout {
				if layoutID == skillID {
					delete(layout, slot)
				}
			}
			record.Layouts[tree] = layout
		}
		return nil
	}
	record.Skills[int64(skillID)] = dnfrepo.SkillState{Level: floor, Enabled: true}
	return nil
}

func (o *Owner) Plan(ctx context.Context, cmd Command) (PlanResult, error) {
	if o == nil || o.characters == nil || o.skills == nil {
		return PlanResult{}, ErrOwnerUnavailable
	}
	if cmd.SelectedCharacterID == 0 {
		return PlanResult{}, ErrCharacterRequired
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	character, ok, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return PlanResult{}, err
	}
	if !ok {
		return PlanResult{}, ErrCharacterNotFound
	}
	character = dnfrepo.CloneCharacter(character)
	if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
		return PlanResult{}, ErrCharacterOwner
	}
	record, ok, err := o.skills.Load(ctx, characterID)
	if err != nil {
		return PlanResult{}, err
	}
	record = dnfrepo.CloneSkill(record)
	return PlanResult{
		AccountID:         character.AccountID,
		CharacterID:       characterID,
		Operation:         cmd.Operation,
		Known:             ok,
		SkillCount:        len(record.Skills),
		CooldownCount:     len(record.Cooldowns),
		RequestedSkillIDs: append([]int64(nil), cmd.SkillIDs...),
		RefundCount:       cmd.RefundCount,
	}, nil
}

func (o *Owner) ApplyBuy(ctx context.Context, cmd Command) (MutationResult, error) {
	if o == nil {
		return MutationResult{}, ErrOwnerUnavailable
	}
	if err := validateBuySkillTree(cmd.SkillTree); err != nil {
		return MutationResult{}, err
	}
	if o.characters == nil || o.skills == nil || o.skillTx == nil {
		return MutationResult{}, ErrOwnerUnavailable
	}
	if o.catalog == nil {
		return MutationResult{}, ErrSkillRulesMissing
	}
	if o.initial == nil {
		return MutationResult{}, ErrSkillRulesMissing
	}
	if cmd.SelectedCharacterID == 0 {
		return MutationResult{}, ErrCharacterRequired
	}
	if len(cmd.BuyEntries) == 0 {
		return MutationResult{}, ErrSkillDelta
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	character, ok, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return MutationResult{}, err
	}
	if !ok {
		return MutationResult{}, ErrCharacterNotFound
	}
	if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
		return MutationResult{}, ErrCharacterOwner
	}
	currentRecord, found, loadErr := o.skills.Load(ctx, characterID)
	if loadErr != nil {
		return MutationResult{}, loadErr
	}
	if found {
		if err := validateActiveSkillTree(currentRecord, cmd.SkillTree); err != nil {
			return MutationResult{}, err
		}
	} else if cmd.SkillTree != 0 {
		return MutationResult{}, ErrSkillTreeUnavailable
	}
	jobValue, err := strconv.ParseUint(strings.TrimSpace(character.Job), 10, 8)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: job=%q", ErrSkillRulesMissing, character.Job)
	}
	job := byte(jobValue)
	// 分支门禁的唯一输入：`dnf_characters.grow_type` 列的投影（见
	// repository/character_grow_type.go）。`grow_present=false` 表示记录根本没有
	// 携带权威键（仓储没水合 / 部分记录），与"合法值 0 = 未转职"是两码事：
	// 前者是缺陷，后者是正常角色。旧代码用裸 map 读取，两者都变成 grow=0，
	// 于是现场无法区分"该角色确实没转职"和"权威值丢了"。
	growType, growTypePresent := dnfrepo.CharacterGrowType(character)
	// 达人契约 (premium type 27): skills can be learned five character levels
	// early. The PVF required-level rule applies to the effective level, and so
	// does the learnable cap below.
	effectiveLevel := int(character.Level)
	if o.premiumActive(ctx, character.AccountID, premium.TypeOverSkill) {
		effectiveLevel += OverSkillLevelBonus
	}
	// When the contract is inactive, any learned skill whose PVF required
	// level exceeds the raw character level could only have been learned via
	// the contract: reset it and refund SP/TP (official expiry rule).
	swept, err := o.sweepExpiredOverSkill(ctx, cmd, character, job)
	if err != nil {
		return MutationResult{}, err
	}
	tree := int(cmd.SkillTree)
	result := MutationResult{
		CharacterID:                characterID,
		SkillTree:                  cmd.SkillTree,
		FinalMode:                  cmd.FinalMode,
		ExpiredContractSkillsReset: swept,
	}
	// 遗忘河之水 (item 3, stackable/cash/river_lethe.stk): 改动前，任何带 refund
	// 条目的批次都必须在同一事务里消耗一个水（86JP
	// BuySkillService.ExecuteWithRefundConsumable），没有就整体拒绝。默认档不再
	// 收水（见 refund_water.go 的现场与厂商 9.16 证据），只有回退开关
	// DNFBRIDGE_SKILL_REFUND_REQUIRE_WATER=1 才恢复该门槛；遗忘河水契约
	// (premium type 33) 在任何档位下都让退款免费。
	if cmd.RefundCount > 0 && refundRequiresForgetRiverWater() && !o.premiumActive(ctx, character.AccountID, premium.TypeLethe) {
		if o.settlement == nil {
			return MutationResult{}, ErrSkillRefundUnavailable
		}
		err = o.settlement.WithinCharacterSettlement(ctx, characterID, func(group dnfrepo.Group) error {
			if group.Skill == nil || group.Inventory == nil {
				return ErrSkillRefundUnavailable
			}
			mutation, err := o.applyBuyMutation(ctx, cmd, character, job, growType, growTypePresent, effectiveLevel, tree, group.Skill, func() error {
				slot, err := consumeForgetRiverWater(ctx, group.Inventory, characterID)
				if err != nil {
					return err
				}
				result.ConsumedRefundItemSlot = slot
				return nil
			})
			if err != nil {
				return err
			}
			result.Points = mutation.Points
			result.Entries = mutation.Entries
			result.AppliedRefund = mutation.AppliedRefund
			result.ConsumedRefundItem = true
			return nil
		})
	} else {
		err = o.skillTx.WithinCharacterSkill(ctx, characterID, func(repo dnfrepo.SkillRepository) error {
			mutation, err := o.applyBuyMutation(ctx, cmd, character, job, growType, growTypePresent, effectiveLevel, tree, repo, nil)
			if err != nil {
				return err
			}
			result.Points = mutation.Points
			result.Entries = mutation.Entries
			result.AppliedRefund = mutation.AppliedRefund
			return nil
		})
	}
	if err != nil {
		return MutationResult{}, err
	}
	return result, nil
}

// applyBuyMutation runs one validated buy/refund batch against the scoped
// skill repository. consumeRefund, when non-nil, is invoked exactly once
// after every entry validates and before the skill fields are saved, so the
// refund consumable and the skill mutation commit or roll back together.
func (o *Owner) applyBuyMutation(
	ctx context.Context,
	cmd Command,
	character dnfrepo.CharacterRecord,
	job byte,
	growType int,
	growTypePresent bool,
	effectiveLevel int,
	tree int,
	repo dnfrepo.SkillRepository,
	consumeRefund func() error,
) (MutationResult, error) {
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	growTypeDetail := formatGrowTypeDetail(growType, growTypePresent)
	result := MutationResult{
		CharacterID: characterID,
		SkillTree:   cmd.SkillTree,
		FinalMode:   cmd.FinalMode,
	}
	record, exists, err := repo.Load(ctx, characterID)
	if err != nil {
		return MutationResult{}, err
	}
	if !exists {
		return MutationResult{}, ErrSkillPointLedger
	}
	if err := validateActiveSkillTree(record, cmd.SkillTree); err != nil {
		return MutationResult{}, err
	}
	record = dnfrepo.CloneSkill(record)
	points, err := synchronizePointLedger(record.Points, o.baseline, character.Level, o.catalog, job, o.initial, record.Skills)
	if err != nil {
		return MutationResult{}, err
	}
	record.Points = points
	if record.Skills == nil {
		record.Skills = make(map[int64]dnfrepo.SkillState)
	}
	if record.Layouts == nil {
		record.Layouts = make(map[int]dnfrepo.SkillLayout)
	}
	layout, err := ensureSkillLayout(o.catalog, job, tree, record.Skills, record.Layouts[tree])
	if err != nil {
		return MutationResult{}, err
	}
	record.Layouts[tree] = layout
	changed := make(map[uint16]dnfskill.Skill)
	changedSlots := make(map[uint16]int)
	hasEffectiveRefund := false
	for _, entry := range cmd.BuyEntries {
		if entry.LevelDelta == 0 {
			return MutationResult{}, fmt.Errorf("%w: skill=%d delta=0", ErrSkillDelta, entry.SkillID)
		}
		definition, ok := o.catalog.Find(job, entry.SkillID)
		if !ok {
			return MutationResult{}, fmt.Errorf("%w: job=%d skill=%d", ErrSkillNotFound, job, entry.SkillID)
		}
		if effectiveLevel < int(definition.RequiredLevel) {
			return MutationResult{}, fmt.Errorf("%w: skill=%d character=%d effective=%d required=%d", ErrSkillLevel, entry.SkillID, character.Level, effectiveLevel, definition.RequiredLevel)
		}
		if !definition.SupportsCharacterGrowth(growType) {
			return MutationResult{}, fmt.Errorf("%w: skill=%d grow=%d%s", ErrSkillGrowType, entry.SkillID, growType, growTypeDetail)
		}
		if definition.FixedLevelSkill {
			return MutationResult{}, fmt.Errorf("%w: automatic skill=%d", ErrSkillLevel, entry.SkillID)
		}
		state := record.Skills[int64(entry.SkillID)]
		currentLevel := state.Level
		targetLevel := currentLevel + entry.SignedDelta()
		floor := o.initial[entry.SkillID]
		if targetLevel < floor || targetLevel < 0 {
			return MutationResult{}, fmt.Errorf("%w: skill=%d current=%d target=%d floor=%d", ErrSkillLevel, entry.SkillID, currentLevel, targetLevel, floor)
		}
		// Raising a skill is additionally gated by the SAME cap the client
		// draws its "+"/"已精通" state from: min([maximum level],
		// [growtype maximum level][direction], one level per character level).
		// Capping at [maximum level] alone lets the server persist a level the
		// client renders as 已精通 with a dead "+" button (real PVF: 上挑
		// 20/10, 物理暴击 20/10, 基础精通 100/1).
		if targetLevel > currentLevel {
			maxLearnable := definition.MaxLearnableLevel(growType, effectiveLevel)
			if maxLearnable <= 0 {
				return MutationResult{}, fmt.Errorf("%w: skill=%d grow=%d", ErrSkillGrowType, entry.SkillID, growType)
			}
			if targetLevel > maxLearnable {
				return MutationResult{}, fmt.Errorf("%w: skill=%d current=%d target=%d max_learnable=%d max_level=%d", ErrSkillLevel, entry.SkillID, currentLevel, targetLevel, maxLearnable, definition.MaximumLevel)
			}
		}
		pointDelta, err := mutationPointDelta(definition, currentLevel, targetLevel)
		if err != nil {
			return MutationResult{}, err
		}
		if definition.IsTPSkill() {
			record.Points.RemainingTP -= pointDelta
			if record.Points.RemainingTP < 0 || record.Points.RemainingTP > record.Points.TotalTP {
				return MutationResult{}, fmt.Errorf("%w: skill=%d tp_delta=%d remaining=%d total=%d", ErrSkillPoints, entry.SkillID, pointDelta, record.Points.RemainingTP, record.Points.TotalTP)
			}
		} else {
			record.Points.RemainingSP -= pointDelta
			if record.Points.RemainingSP < 0 || record.Points.RemainingSP > record.Points.TotalSP {
				return MutationResult{}, fmt.Errorf("%w: skill=%d sp_delta=%d remaining=%d total=%d", ErrSkillPoints, entry.SkillID, pointDelta, record.Points.RemainingSP, record.Points.TotalSP)
			}
		}
		slot, hasSlot := findSkillSlot(layout, entry.SkillID)
		if currentLevel > 0 && !hasSlot {
			return MutationResult{}, fmt.Errorf("%w: learned skill=%d has no slot", ErrSkillSlot, entry.SkillID)
		}
		if currentLevel == 0 && targetLevel > 0 {
			slot = allocateSkillSlot(definition, tree, layout)
			if slot < 0 {
				return MutationResult{}, fmt.Errorf("%w: skill=%d group=%d", ErrSkillSlot, entry.SkillID, currentSlotGroup(definition))
			}
			layout[slot] = entry.SkillID
			hasSlot = true
		}
		if !hasSlot {
			return MutationResult{}, fmt.Errorf("%w: skill=%d target=%d", ErrSkillSlot, entry.SkillID, targetLevel)
		}
		changedSlots[entry.SkillID] = slot
		if targetLevel == 0 {
			delete(record.Skills, int64(entry.SkillID))
			delete(layout, slot)
		} else {
			record.Skills[int64(entry.SkillID)] = dnfrepo.SkillState{Level: targetLevel, Enabled: true}
		}
		if entry.RefundFlag != 0 && targetLevel < currentLevel {
			hasEffectiveRefund = true
		}
		changed[entry.SkillID] = definition
	}
	if err := validateLearnedPrerequisites(o.catalog, job, record.Skills); err != nil {
		return MutationResult{}, err
	}
	if hasEffectiveRefund && consumeRefund != nil {
		if err := consumeRefund(); err != nil {
			return MutationResult{}, err
		}
	}
	record.UpdatedAt = time.Now().UTC()
	if err := dnfrepo.SaveSkillFields(ctx, repo, record, dnfrepo.SkillFieldSkills, dnfrepo.SkillFieldPoints, dnfrepo.SkillFieldLayouts); err != nil {
		return MutationResult{}, err
	}
	result.Points = record.Points
	result.AppliedRefund = hasEffectiveRefund
	result.Entries = make([]MutationEntry, 0, len(changed))
	for _, entry := range cmd.BuyEntries {
		definition, first := changed[entry.SkillID]
		if !first {
			continue
		}
		delete(changed, entry.SkillID)
		result.Entries = append(result.Entries, MutationEntry{
			SkillID: entry.SkillID,
			Level:   record.Skills[int64(entry.SkillID)].Level,
			TP:      definition.IsTPSkill(),
			Slot:    changedSlots[entry.SkillID],
		})
	}
	return result, nil
}

// consumeForgetRiverWater decrements one 遗忘河之水 (item 3) stack from the
// character's main inventory list and returns the consumed slot. Missing
// water fails the enclosing transaction, so no skill refund is persisted.
func consumeForgetRiverWater(ctx context.Context, inventoryRepo dnfrepo.InventoryRepository, characterID string) (int16, error) {
	record, found, err := inventoryRepo.Load(ctx, characterID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrSkillRefundConsumableRequired
	}
	record = dnfrepo.CloneInventory(record)
	for key, stack := range record.Slots {
		if !strings.HasPrefix(key, "0:") || stack.ItemID != forgetRiverWaterItemID || stack.Count <= 0 {
			continue
		}
		slot, err := strconv.Atoi(key[2:])
		if err != nil {
			return 0, err
		}
		if stack.Count == 1 {
			delete(record.Slots, key)
		} else {
			stack.Count--
			record.Slots[key] = stack
		}
		record.UpdatedAt = time.Now().UTC()
		if err := dnfrepo.SaveInventoryFields(ctx, inventoryRepo, record, dnfrepo.InventoryFieldSlots); err != nil {
			return 0, err
		}
		return int16(slot), nil
	}
	return 0, ErrSkillRefundConsumableRequired
}

// ApplyReset atomically restores the current tree to the runtime PVF initial
// levels, refunds every spent SP/TP point, and rebuilds the initial quickbar.
// Cooldowns are deliberately preserved because reset does not own that field.
func (o *Owner) ApplyReset(ctx context.Context, cmd Command) (ResetMutationResult, error) {
	if o == nil || o.characters == nil || o.skills == nil || o.skillTx == nil {
		return ResetMutationResult{}, ErrOwnerUnavailable
	}
	if err := validateSkillInitCommand(cmd); err != nil {
		return ResetMutationResult{}, err
	}
	if o.catalog == nil || o.initial == nil || o.baseline == nil {
		return ResetMutationResult{}, ErrSkillRulesMissing
	}
	if cmd.SelectedCharacterID == 0 {
		return ResetMutationResult{}, ErrCharacterRequired
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	character, ok, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return ResetMutationResult{}, err
	}
	if !ok {
		return ResetMutationResult{}, ErrCharacterNotFound
	}
	if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
		return ResetMutationResult{}, ErrCharacterOwner
	}
	jobValue, err := strconv.ParseUint(strings.TrimSpace(character.Job), 10, 8)
	if err != nil {
		return ResetMutationResult{}, fmt.Errorf("%w: job=%q", ErrSkillRulesMissing, character.Job)
	}
	job := byte(jobValue)
	points := *o.baseline
	if err := validatePointLedger(points, character.Level); err != nil {
		return ResetMutationResult{}, err
	}
	states := make(map[int64]dnfrepo.SkillState, len(o.initial))
	for skillID, level := range o.initial {
		if level < 0 {
			return ResetMutationResult{}, fmt.Errorf("%w: initial skill=%d level=%d", ErrSkillLevel, skillID, level)
		}
		if level == 0 {
			continue
		}
		definition, ok := o.catalog.Find(job, skillID)
		if !ok {
			return ResetMutationResult{}, fmt.Errorf("%w: initial job=%d skill=%d", ErrSkillNotFound, job, skillID)
		}
		if fixedLevel := definition.FixedLevelForCharacter(character.Level); fixedLevel > level {
			level = fixedLevel
		}
		states[int64(skillID)] = dnfrepo.SkillState{Level: level, Enabled: true}
	}
	layout, err := BuildInitialSkillLayout(o.catalog, job, int(cmd.SkillTree), states)
	if err != nil {
		return ResetMutationResult{}, err
	}
	result := ResetMutationResult{
		CharacterID: characterID,
		SkillTree:   cmd.SkillTree,
		Mode:        cmd.Mode,
		SkillCount:  len(states),
		Points:      points,
	}
	err = o.skillTx.WithinCharacterSkill(ctx, characterID, func(repo dnfrepo.SkillRepository) error {
		record, exists, err := repo.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrSkillPointLedger
		}
		if err := validateActiveSkillTree(record, cmd.SkillTree); err != nil {
			return err
		}
		record = dnfrepo.CloneSkill(record)
		record.Skills = states
		record.Points = points
		if record.Layouts == nil {
			record.Layouts = make(map[int]dnfrepo.SkillLayout)
		}
		record.Layouts[int(cmd.SkillTree)] = layout
		record.UpdatedAt = time.Now().UTC()
		return dnfrepo.SaveSkillFields(ctx, repo, record, dnfrepo.SkillFieldSkills, dnfrepo.SkillFieldPoints, dnfrepo.SkillFieldLayouts)
	})
	if err != nil {
		return ResetMutationResult{}, err
	}
	return result, nil
}

// ApplySlot atomically swaps two entries in the persisted current-EXE skill
// vector. The op28 success reader performs the same swap locally, so the ACK is
// emitted only after this transaction commits and no proactive op19 refresh is
// needed.
func (o *Owner) ApplySlot(ctx context.Context, cmd Command) (SlotMutationResult, error) {
	if o == nil || o.characters == nil || o.skills == nil || o.skillTx == nil {
		return SlotMutationResult{}, ErrOwnerUnavailable
	}
	if o.catalog == nil {
		return SlotMutationResult{}, ErrSkillRulesMissing
	}
	if err := validateSkillSlotCommand(cmd); err != nil {
		return SlotMutationResult{}, err
	}
	if cmd.SelectedCharacterID == 0 {
		return SlotMutationResult{}, ErrCharacterRequired
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	character, ok, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return SlotMutationResult{}, err
	}
	if !ok {
		return SlotMutationResult{}, ErrCharacterNotFound
	}
	if accountID := strings.TrimSpace(cmd.AccountID); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
		return SlotMutationResult{}, ErrCharacterOwner
	}
	currentRecord, found, loadErr := o.skills.Load(ctx, characterID)
	if loadErr != nil {
		return SlotMutationResult{}, loadErr
	}
	if found {
		if err := validateActiveSkillTree(currentRecord, cmd.SkillTree); err != nil {
			return SlotMutationResult{}, err
		}
	} else if cmd.SkillTree != 0 {
		return SlotMutationResult{}, ErrSkillTreeUnavailable
	}
	jobValue, err := strconv.ParseUint(strings.TrimSpace(character.Job), 10, 8)
	if err != nil {
		return SlotMutationResult{}, fmt.Errorf("%w: job=%q", ErrSkillRulesMissing, character.Job)
	}
	job := byte(jobValue)
	result := SlotMutationResult{
		CharacterID: characterID,
		SkillTree:   cmd.SkillTree,
		From:        cmd.From,
		To:          cmd.To,
	}
	err = o.skillTx.WithinCharacterSkill(ctx, characterID, func(repo dnfrepo.SkillRepository) error {
		record, exists, err := repo.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !exists || len(record.Skills) == 0 {
			return ErrSkillSlot
		}
		if err := validateActiveSkillTree(record, cmd.SkillTree); err != nil {
			return err
		}
		record = dnfrepo.CloneSkill(record)
		tree := int(cmd.SkillTree)
		var existing dnfrepo.SkillLayout
		if record.Layouts != nil {
			existing = record.Layouts[tree]
		}
		layout, err := ensureSkillLayout(o.catalog, job, tree, record.Skills, existing)
		if err != nil {
			return err
		}
		from := int(cmd.From)
		to := int(cmd.To)
		fromSkillID, fromOccupied := layout[from]
		if !fromOccupied {
			return fmt.Errorf("%w: source slot=%d is empty", ErrSkillSlot, from)
		}
		toSkillID, toOccupied := layout[to]
		layout[to] = fromSkillID
		if toOccupied {
			layout[from] = toSkillID
		} else {
			delete(layout, from)
		}
		if err := validateSkillLayoutBounds(layout); err != nil {
			return err
		}
		if record.Layouts == nil {
			record.Layouts = make(map[int]dnfrepo.SkillLayout)
		}
		record.Layouts[tree] = layout
		record.UpdatedAt = time.Now().UTC()
		if err := dnfrepo.SaveSkillFields(ctx, repo, record, dnfrepo.SkillFieldLayouts); err != nil {
			return err
		}
		result.FromSkillID = fromSkillID
		result.ToSkillID = toSkillID
		result.ToOccupied = toOccupied
		return nil
	})
	if err != nil {
		return SlotMutationResult{}, err
	}
	return result, nil
}

func validateSkillSlotCommand(cmd Command) error {
	if cmd.SkillTree >= currentEXESkillTreeCount {
		return fmt.Errorf("%w: tree=%d; persisted op19 layout currently owns tree=%d", ErrSkillTree, cmd.SkillTree, currentEXEProvenBuySkillTree)
	}
	if int(cmd.From) >= currentEXESkillSlotCount || int(cmd.To) >= currentEXESkillSlotCount {
		return fmt.Errorf("%w: from=%d to=%d count=%d", ErrSkillSlot, cmd.From, cmd.To, currentEXESkillSlotCount)
	}
	if int(cmd.To) >= currentEXEReservedSlotStart && int(cmd.To) < currentEXEReservedSlotEnd {
		return fmt.Errorf("%w: destination slot=%d is reserved", ErrSkillSlot, cmd.To)
	}
	if cmd.ContextIndex < -1 || cmd.ContextIndex > 2 {
		return fmt.Errorf("%w: context=%d", ErrSkillSlotContext, cmd.ContextIndex)
	}
	if cmd.Mode != 0 && cmd.Mode != 2 {
		return fmt.Errorf("%w: mode=%d", ErrSkillSlotMode, cmd.Mode)
	}
	return nil
}

// validateSkillLayoutBounds keeps the persisted tree-zero vector serializable:
// every key must address one of the current EXE's 204 slots.
//
// 2026-09-26 — 这里原本还有一条「被动技能不得占用快捷栏槽位」的门禁，已删除。现场
// （HK packet_log.20260924-195607.txt:13697）唯一一次 op28 拒绝就是它：
//
//	type=28 operation=change_skill_slot response_allowed=false
//	reason=… skill UI slot allocation failed: passive skill=27 cannot occupy quick slot=3
//	       success ACK is blocked
//
// 角色 2 把快捷栏 3 号与技能树 54 号互换，而 54 号上是被动 27（真档
// `skill/Swordman/WeaponMasteryUp.skl` = `[type]` `[passive]`，即武器奥义）。删掉它的
// 三条理由都指向「这条限制是服务端自己发明的」：
//  1. 客户端**自己**发起了这次互换。op28 的 S2C sub_1D0A4B0 读完 tree/from/to 才执行
//     本地互换（见 ApplySlot 顶部注释），所以服务端不回包 = 玩家拖不动技能栏，正是操作
//     者报的「技能栏无法移动技能」。
//  2. 四份厂商 C# 参考实现（86JP / ServerS4A21 / 10675 / a21zigai）
//     `SkillHandler.Handle_CHANGE_SKILLSLOT` 一律**无条件**互换、**无条件**回
//     `{0x01,tree,from,to}`（与 BuildChangeSkillSlotSuccess 逐字节同形），既没有被动
//     门禁也没有任何失败体。
//  3. 服务端自己的登录投影从不做这项校验（scene_skill_mutation.go 经
//     BuildCurrentSkillLayout → ensureSkillLayout 只做槽位分配），历史库里本来就存在
//     快捷栏里的被动（dnf_local-20260806-112541.sql:7849 角色 1 slot2=190 快速起身、
//     slot5=27 武器奥义）——即「服务端会照原样下发自己拒绝写入的状态」。
func validateSkillLayoutBounds(layout dnfrepo.SkillLayout) error {
	for slot := range layout {
		if slot < 0 || slot >= currentEXESkillSlotCount {
			return fmt.Errorf("%w: persisted slot=%d count=%d", ErrSkillSlot, slot, currentEXESkillSlotCount)
		}
	}
	return nil
}

func validateBuySkillTree(tree byte) error {
	if tree >= currentEXESkillTreeCount {
		return fmt.Errorf("%w: tree=%d; only tree=%d has a current EXE reconstruction path", ErrSkillTree, tree, currentEXEProvenBuySkillTree)
	}
	return nil
}

func validateSkillInitCommand(cmd Command) error {
	if err := validateBuySkillTree(cmd.SkillTree); err != nil {
		return err
	}
	if cmd.Mode > 3 {
		return fmt.Errorf("%w: init mode=%d", ErrSkillSlotMode, cmd.Mode)
	}
	return nil
}

const (
	mathMaxUint8                 = int(^uint8(0))
	initialPrimaryQuickSlotCount = 3
)

func ensureSkillLayout(catalog *dnfskill.Table, job byte, tree int, states map[int64]dnfrepo.SkillState, existing dnfrepo.SkillLayout) (dnfrepo.SkillLayout, error) {
	layout := make(dnfrepo.SkillLayout)
	assigned := make(map[uint16]struct{})
	for slot, skillID := range existing {
		if slot < 0 || slot > mathMaxUint8 {
			return nil, fmt.Errorf("%w: slot=%d skill=%d", ErrSkillSlot, slot, skillID)
		}
		if states[int64(skillID)].Level <= 0 {
			continue
		}
		if _, duplicate := assigned[skillID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate skill=%d", ErrSkillSlot, skillID)
		}
		layout[slot] = skillID
		assigned[skillID] = struct{}{}
	}
	ids := make([]int, 0, len(states))
	for rawID, state := range states {
		if state.Level > 0 && rawID >= 0 && rawID <= 0xffff {
			ids = append(ids, int(rawID))
		}
	}
	sort.Ints(ids)
	for _, rawID := range ids {
		skillID := uint16(rawID)
		if _, ok := assigned[skillID]; ok {
			continue
		}
		definition, ok := catalog.Find(job, skillID)
		if !ok {
			slot := allocateMissingPVFGrantSlot(layout)
			if slot < 0 {
				return nil, fmt.Errorf("%w: layout job=%d missing_skill=%d", ErrSkillSlot, job, skillID)
			}
			layout[slot] = skillID
			assigned[skillID] = struct{}{}
			continue
		}
		slot := allocateSkillSlot(definition, tree, layout)
		if slot < 0 {
			return nil, fmt.Errorf("%w: layout skill=%d group=%d", ErrSkillSlot, skillID, currentSlotGroup(definition))
		}
		layout[slot] = skillID
		assigned[skillID] = struct{}{}
	}
	return layout, nil
}

// BuildCurrentSkillLayout reconstructs the current EXE's skill-tree slots from
// persisted learned skills and any existing assignments. Active tree-zero
// skills occupy the primary/extension quick slots; passive skills stay in the
// grouped skill-tree ranges.
func BuildCurrentSkillLayout(catalog *dnfskill.Table, job byte, tree int, states map[int64]dnfrepo.SkillState, existing dnfrepo.SkillLayout) (dnfrepo.SkillLayout, error) {
	return ensureSkillLayout(catalog, job, tree, states, existing)
}

// BuildInitialSkillLayout builds the first persisted layout for PVF starter
// skills. Only the first three active skills are placed on the primary
// quickbar; every other learned skill remains available in its tree group.
func BuildInitialSkillLayout(catalog *dnfskill.Table, job byte, tree int, states map[int64]dnfrepo.SkillState) (dnfrepo.SkillLayout, error) {
	if catalog == nil {
		return nil, fmt.Errorf("%w: initial layout job=%d", ErrSkillNotFound, job)
	}
	layout := make(dnfrepo.SkillLayout)
	ids := make([]int, 0, len(states))
	for rawID, state := range states {
		if state.Level > 0 && rawID >= 0 && rawID <= 0xffff {
			ids = append(ids, int(rawID))
		}
	}
	sort.Ints(ids)
	primaryActive := 0
	for _, rawID := range ids {
		skillID := uint16(rawID)
		definition, ok := catalog.Find(job, skillID)
		if !ok {
			slot := allocateMissingPVFGrantSlot(layout)
			if slot < 0 {
				return nil, fmt.Errorf("%w: initial layout job=%d missing_skill=%d", ErrSkillSlot, job, skillID)
			}
			layout[slot] = skillID
			continue
		}
		slot := -1
		// 与 allocateSkillSlot 同一判据，避免新建/重建路径把 TP 技能填回普通快捷栏。
		if tree >= 0 && tree < currentEXESkillTreeCount && definition.Active && !definition.IsTPSkill() && primaryActive < initialPrimaryQuickSlotCount {
			slot = firstFreeSkillSlot(layout, 0, initialPrimaryQuickSlotCount)
			if slot >= 0 {
				primaryActive++
			}
		}
		if slot < 0 {
			group := currentSlotGroup(definition)
			start := []int{6, 54, 102, 150}[group]
			slot = firstFreeSkillSlot(layout, start, start+48)
		}
		if slot < 0 {
			return nil, fmt.Errorf("%w: initial layout skill=%d group=%d", ErrSkillSlot, skillID, currentSlotGroup(definition))
		}
		layout[slot] = skillID
	}
	return layout, nil
}

func allocateSkillSlot(definition dnfskill.Skill, tree int, occupied dnfrepo.SkillLayout) int {
	// 保留既有领域/数据库的两段快捷栏编号。90客户端线上编号由dnfbridge
	// 在op19/op29/op28边界统一转换，不能把这里的198直接解释成当前线上快捷栏。
	//
	// TP（强化/EX）技能不参与普通快捷栏（0..5、198..203）的自动填空：真档唯一的 active TP
	// （固定QA档 job 11 id 220 强化 - 招架反击）会把这当成第一空位。普通主动技能照旧优先填空；
	// TP 技能只拿 currentSlotGroup 的技能页展示槽，手动绑定（op28）与已有布局不受影响。
	// 113段反馈的 强化 - 上挑（真档 job 0 id 215）是 passive，本来就不过这个分支。
	if tree >= 0 && tree < currentEXESkillTreeCount && definition.Active && !definition.IsTPSkill() {
		if slot := firstFreeSkillSlot(occupied, 0, 6); slot >= 0 {
			return slot
		}
		if slot := firstFreeSkillSlot(occupied, 198, 204); slot >= 0 {
			return slot
		}
	}
	group := currentSlotGroup(definition)
	start := []int{6, 54, 102, 150}[group]
	return firstFreeSkillSlot(occupied, start, start+48)
}

func currentSlotGroup(definition dnfskill.Skill) int {
	// sub_3059AC0 maps the current skill object's active marker, skill class,
	// and growtype-pair count to groups 3, 2, 1, and 0 respectively.
	if definition.Active {
		return 3
	}
	if definition.SkillClass == 4 {
		return 2
	}
	if len(definition.GrowTypes) <= 2 {
		return 1
	}
	return 0
}

func firstFreeSkillSlot(occupied dnfrepo.SkillLayout, start int, end int) int {
	for slot := start; slot < end; slot++ {
		if _, exists := occupied[slot]; !exists {
			return slot
		}
	}
	return -1
}

// Some real character/*.chr free grants intentionally have no job-scoped
// .skl row. Keep the real PVF grant visible in the first passive/group range,
// matching the reference client's missing-definition fallback, without
// inventing costs, prerequisites, or an active quick-slot classification.
func allocateMissingPVFGrantSlot(occupied dnfrepo.SkillLayout) int {
	if slot := firstFreeSkillSlot(occupied, 6, 54); slot >= 0 {
		return slot
	}
	return firstFreeSkillSlot(occupied, 0, currentEXESkillSlotCount)
}

func findSkillSlot(layout dnfrepo.SkillLayout, skillID uint16) (int, bool) {
	for slot, assigned := range layout {
		if assigned == skillID {
			return slot, true
		}
	}
	return 0, false
}

func validatePointLedger(points dnfrepo.SkillPointState, characterLevel int) error {
	if points.SyncedLevel != characterLevel || points.TotalSP < 0 || points.RemainingSP < 0 || points.RemainingSP > points.TotalSP || points.TotalTP < 0 || points.RemainingTP < 0 || points.RemainingTP > points.TotalTP {
		return fmt.Errorf("%w: character_level=%d ledger=%+v", ErrSkillPointLedger, characterLevel, points)
	}
	return nil
}

func synchronizePointLedger(current dnfrepo.SkillPointState, baseline *dnfrepo.SkillPointState, characterLevel int, catalog *dnfskill.Table, job byte, initial map[uint16]int, states map[int64]dnfrepo.SkillState) (dnfrepo.SkillPointState, error) {
	if baseline == nil {
		return current, validatePointLedger(current, characterLevel)
	}
	target := *baseline
	if err := validatePointLedger(target, characterLevel); err != nil {
		return dnfrepo.SkillPointState{}, err
	}
	if isEmptyPointLedger(current) {
		spentSP, spentTP, err := learnedPointCost(catalog, job, initial, states)
		if err != nil {
			return dnfrepo.SkillPointState{}, err
		}
		target.RemainingSP = target.TotalSP - spentSP
		target.RemainingTP = target.TotalTP - spentTP
		if err := validatePointLedger(target, characterLevel); err != nil {
			return dnfrepo.SkillPointState{}, err
		}
		return target, nil
	}
	if current.SyncedLevel <= 0 || current.SyncedLevel > characterLevel || current.TotalSP < 0 || current.RemainingSP < 0 || current.RemainingSP > current.TotalSP || current.TotalTP < 0 || current.RemainingTP < 0 || current.RemainingTP > current.TotalTP {
		return dnfrepo.SkillPointState{}, fmt.Errorf("%w: character_level=%d ledger=%+v", ErrSkillPointLedger, characterLevel, current)
	}
	current.RemainingSP += target.TotalSP - current.TotalSP
	current.RemainingTP += target.TotalTP - current.TotalTP
	current.TotalSP = target.TotalSP
	current.TotalTP = target.TotalTP
	current.SyncedLevel = target.SyncedLevel
	if err := validatePointLedger(current, characterLevel); err != nil {
		return dnfrepo.SkillPointState{}, err
	}
	return current, nil
}

func learnedPointCost(catalog *dnfskill.Table, job byte, initial map[uint16]int, states map[int64]dnfrepo.SkillState) (int, int, error) {
	spentSP := 0
	spentTP := 0
	for rawID, state := range states {
		if state.Level <= 0 {
			continue
		}
		if rawID < 0 || rawID > 0xffff {
			return 0, 0, fmt.Errorf("%w: job=%d skill=%d", ErrSkillNotFound, job, rawID)
		}
		skillID := uint16(rawID)
		definition, ok := catalog.Find(job, skillID)
		if !ok {
			return 0, 0, fmt.Errorf("%w: job=%d skill=%d", ErrSkillNotFound, job, skillID)
		}
		floor := initial[skillID]
		if state.Level < floor {
			return 0, 0, fmt.Errorf("%w: skill=%d level=%d floor=%d", ErrSkillLevel, skillID, state.Level, floor)
		}
		cost, err := mutationPointDelta(definition, floor, state.Level)
		if err != nil {
			return 0, 0, err
		}
		if definition.IsTPSkill() {
			spentTP += cost
		} else {
			spentSP += cost
		}
	}
	return spentSP, spentTP, nil
}

func isEmptyPointLedger(points dnfrepo.SkillPointState) bool {
	return points == (dnfrepo.SkillPointState{})
}

func clonePointBaseline(points *dnfrepo.SkillPointState) *dnfrepo.SkillPointState {
	if points == nil {
		return nil
	}
	copy := *points
	return &copy
}

func mutationPointDelta(definition dnfskill.Skill, currentLevel int, targetLevel int) (int, error) {
	total := 0
	if targetLevel > currentLevel {
		for level := currentLevel; level < targetLevel; level++ {
			cost := definition.LevelCost(level)
			if cost < 0 {
				return 0, fmt.Errorf("%w: skill=%d level=%d cost=%d", ErrSkillPointLedger, definition.ID, level, cost)
			}
			total += cost
		}
		return total, nil
	}
	for level := currentLevel - 1; level >= targetLevel; level-- {
		cost := definition.LevelCost(level)
		if cost < 0 {
			return 0, fmt.Errorf("%w: skill=%d level=%d cost=%d", ErrSkillPointLedger, definition.ID, level, cost)
		}
		total -= cost
	}
	return total, nil
}

func validateLearnedPrerequisites(catalog *dnfskill.Table, job byte, states map[int64]dnfrepo.SkillState) error {
	for rawID, state := range states {
		if state.Level <= 0 || rawID < 0 || rawID > 0xffff {
			continue
		}
		definition, ok := catalog.Find(job, uint16(rawID))
		if !ok {
			continue
		}
		for _, prerequisite := range definition.Prerequisites {
			actual := states[int64(prerequisite.SkillID)].Level
			if actual < prerequisite.Level {
				return fmt.Errorf("%w: skill=%d requires=%d level=%d actual=%d", ErrSkillPrerequisite, definition.ID, prerequisite.SkillID, prerequisite.Level, actual)
			}
		}
	}
	return nil
}

func cloneInitialLevels(values map[uint16]int) map[uint16]int {
	if values == nil {
		return nil
	}
	out := make(map[uint16]int, len(values))
	for skillID, level := range values {
		out[skillID] = level
	}
	return out
}

func planError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
