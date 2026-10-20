package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

// 登录/技能页投影时的"可学习上限修复"是**破坏性**的：它把高于可学习上限的
// 持久化等级夹回上限（只退 SP），所以现场必须先能"只看不动"。开关语义：
//
//	DNFBRIDGE_SKILL_CAP_REPAIR
//	  ""/"clamp"（默认）：修好后的行为，夹回并落库。
//	  "observe"/"report"/"dry-run"/"off"/"0"/"false"/"no"：只写
//	    `game-skill-learnable-cap-state` 报告（clamps 照常列出），一个字段都不改。
//
// 用途：若客户端真正的可加点上限是 `[maximum level]`（20）而不是
// `[growtype maximum level]`（10），这个修复就会在每次登录时删掉玩家合法买到的
// 等级。observe 档可以在不动玩家数据的前提下把受影响角色点出来。
const (
	currentSkillCapRepairEnv     = "DNFBRIDGE_SKILL_CAP_REPAIR"
	currentSkillCapRepairClamp   = "clamp"
	currentSkillCapRepairObserve = "observe"
)

func currentSkillCapRepairMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentSkillCapRepairEnv))) {
	case currentSkillCapRepairObserve, "report", "dry-run", "dryrun", "off", "0", "false", "no":
		return currentSkillCapRepairObserve
	default:
		return currentSkillCapRepairClamp
	}
}

func currentSceneSkillOwner(repositories dnfrepo.Group) (*dnfskill.SceneOwner, error) {
	return dnfskill.NewSceneOwner(repositories)
}

func syncCurrentSkillPointState(
	current dnfrepo.SkillPointState,
	target dnfrepo.SkillPointState,
) (dnfrepo.SkillPointState, bool, error) {
	return dnfskill.SyncPointState(current, target)
}

func (s *Service) loadOrBackfillCurrentSceneSkillRecord(ctx context.Context, repos dnfrepo.Group, character dnfrepo.CharacterRecord, job byte) (dnfrepo.SkillRecord, bool, error) {
	record, found, err := repos.Skill.Load(ctx, character.CharacterID)
	if err != nil {
		return dnfrepo.SkillRecord{}, false, err
	}
	if found && len(record.Skills) > 0 {
		record, _, err = s.syncCurrentSceneSkillPointLedger(ctx, repos, character, record)
		if err != nil {
			return dnfrepo.SkillRecord{}, false, err
		}
		record, err = s.repairCurrentSceneSkillLevelCaps(ctx, repos, character, record)
		if err != nil {
			return dnfrepo.SkillRecord{}, false, err
		}
		return record, false, nil
	}
	initial, err := s.initialCharacterSkills(ctx, job)
	if err != nil {
		return dnfrepo.SkillRecord{}, false, fmt.Errorf("load job %d initial skills from PVF: %w", job, err)
	}
	if len(initial) == 0 {
		return dnfrepo.SkillRecord{}, false, fmt.Errorf("job %d has no PVF initial skills", job)
	}
	points, err := s.initialSkillPoints(ctx, character.Level)
	if err != nil {
		return dnfrepo.SkillRecord{}, false, fmt.Errorf("load level %d initial skill points from PVF: %w", character.Level, err)
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	if catalog == nil {
		return dnfrepo.SkillRecord{}, false, fmt.Errorf("job %d initial skill catalog is unavailable", job)
	}

	seed := initialSkillRecord(character, characterPVFInitialization{
		Job:         job,
		Level:       character.Level,
		Skills:      initial,
		SkillPoints: points,
	}, time.Now().UTC())
	layout, err := skillcmd.BuildInitialSkillLayout(catalog, job, currentSkillInfoTreeIndex, seed.Skills)
	if err != nil {
		return dnfrepo.SkillRecord{}, false, fmt.Errorf("build job %d initial skill layout: %w", job, err)
	}
	seed.Layouts = map[int]dnfrepo.SkillLayout{currentSkillInfoTreeIndex: layout}

	owner, err := currentSceneSkillOwner(repos)
	if err != nil {
		return dnfrepo.SkillRecord{}, false, err
	}
	return owner.Backfill(ctx, dnfskill.BackfillCommand{
		CharacterID: character.CharacterID,
		Seed:        seed,
		UpdatedAt:   time.Now().UTC(),
	})
}

// repairCurrentSceneSkillLevelCaps clamps persisted skill levels that sit above
// the current client's learnable cap (`[growtype maximum level]` table) and
// refunds the difference. A level above that cap is exactly the state the
// client paints as 已精通 with a dead "+" button, so this is the login repair
// behind "回退技能加点显示已精通该技能". It also emits the at-cap list, which
// names the skills an operator report can be about.
//
// It is deliberately fail-open: the projection must still render when the PVF
// catalog or the initial-skill table is unavailable.
func (s *Service) repairCurrentSceneSkillLevelCaps(
	ctx context.Context,
	repos dnfrepo.Group,
	character dnfrepo.CharacterRecord,
	record dnfrepo.SkillRecord,
) (dnfrepo.SkillRecord, error) {
	job, ok := characterJobByte(character)
	if !ok {
		return record, nil
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	if catalog == nil {
		return record, nil
	}
	initial, err := s.initialCharacterSkills(ctx, job)
	if err != nil {
		return record, nil
	}
	initialLevels := make(map[uint16]int, len(initial))
	for _, entry := range initial {
		if entry.SkillID < 0 || entry.SkillID > 0xffff || entry.Level <= 0 {
			continue
		}
		initialLevels[uint16(entry.SkillID)] = entry.Level
	}
	growType := characterGrowTypeInt(character)
	// The learnable cap is a function of the *effective* character level: the
	// buy path already adds the 达人契约 (premium type 27) five-level bonus
	// (skillcmd.Owner.applyBuyMutation). Using the raw level here would clamp
	// — and refund — levels the contract legitimately bought, so the repair and
	// the buy gate must read the same level.
	effectiveLevel := s.currentSceneSkillEffectiveLevel(ctx, repos, character)
	repairMode := currentSkillCapRepairMode()
	owner, err := currentSceneSkillOwner(repos)
	if err != nil {
		return record, nil
	}
	repaired, result, err := owner.RepairLevelCaps(ctx, catalog, dnfskill.LevelCapRepairCommand{
		CharacterID:    character.CharacterID,
		Job:            job,
		GrowType:       growType,
		CharacterLevel: effectiveLevel,
		InitialLevels:  initialLevels,
		UpdatedAt:      time.Now().UTC(),
		ObserveOnly:    repairMode == currentSkillCapRepairObserve,
	})
	if err != nil {
		return record, nil
	}
	if result.Changed() {
		record = repaired
	}
	atCap := dnfskill.SkillsAtLearnableCap(catalog, job, growType, effectiveLevel, record.Skills)
	if len(atCap) > 0 || len(result.Clamps) > 0 || len(result.Unavailable) > 0 {
		s.logPacketEvent("game-skill-learnable-cap-state",
			append([]any{
				"character_id", character.CharacterID,
				"job", job,
				"grow_type", growType,
				"character_level", character.Level,
				"effective_level", effectiveLevel,
				"repair_mode", repairMode,
				"clamped", result.Clamps,
				"clamped_count", len(result.Clamps),
				"at_learnable_cap", atCap,
				"direction_unavailable", result.Unavailable,
			}, characterGrowTypeAuditState(character)...)...)
	}
	s.auditCurrentSceneSkillGrowType(character, job, record)
	return record, nil
}

// currentSceneSkillEffectiveLevel is the character level every learnable-cap
// decision must use: the persisted level plus the 达人契约 (premium type 27)
// five-level bonus while that contract is active. The buy gate in skillcmd
// already computes it that way; the login/projection cap repair used the raw
// level and therefore clamped contract-bought rows (refunding their SP) at
// every projection. A missing account repository or account record means "no
// contract", which is also what the buy gate assumes. Read-only, fail-open.
func (s *Service) currentSceneSkillEffectiveLevel(ctx context.Context, repos dnfrepo.Group, character dnfrepo.CharacterRecord) int {
	level := int(character.Level)
	if repos.Account == nil || character.AccountID == "" {
		return level
	}
	account, found, err := repos.Account.Load(ctx, character.AccountID)
	if err != nil || !found {
		return level
	}
	if !currentPremiumActive(account, currentPremiumTypeOverSkill, time.Now().UTC()) {
		return level
	}
	return level + skillcmd.OverSkillLevelBonus
}

// auditCurrentSceneSkillGrowType 把"持久化方向"与"已持久化技能"的矛盾直接点名。
//
// 判读串（登录技能同步边界，每个角色每次登录最多一条）：
//
//	kind=game-skill-grow-type-state character_id=2 job=0 grow_type=0
//	  grow_type_present=true grow_type_in_domain=true grow_branch=0 grow_awakening=0
//	  grow_type_source=dnf_characters.grow_type learned_count=32
//	  direction_unavailable=[27 33 110] direction_unavailable_count=3 suspect=true
//
// suspect=true 且 direction_unavailable 非空 = 该角色**已经学过**当前方向学不了的
// 技能，即"权威 grow_type 与已持久化技能自相矛盾"：门禁会拒绝这些技能的每一次加点
// （`skill grow type rule rejected mutation: skill=27 grow=0`），而客户端按同一份
// 已学技能画出可加点界面。线上 2026-09-25 的现场（角色 2 菲八番茄：从剑魂学了
// 27/33/110，权威列却是 0）就是这一条。若 grow_type_present=false，则是另一个
// 性质：权威值没水合（仓储/记录问题），不是数据矛盾。
//
// 本审计是**只读**的：它不修数据、不改技能、不放宽门禁。fail-open。
func (s *Service) auditCurrentSceneSkillGrowType(character dnfrepo.CharacterRecord, job byte, record dnfrepo.SkillRecord) {
	if !skillGrowTypeAuditEnabled() {
		return
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	if catalog == nil {
		return
	}
	growth := characterGrowType(character)
	unavailable := dnfskill.SkillsWithUnavailableDirection(catalog, job, growth.Packed, record.Skills)
	if len(unavailable) == 0 && growth.Present {
		return
	}
	s.logPacketEvent("game-skill-grow-type-state",
		append([]any{
			"character_id", character.CharacterID,
			"job", job,
			"character_level", character.Level,
			"learned_count", len(record.Skills),
			"direction_unavailable", formatSkillIDs(unavailable),
			"direction_unavailable_count", len(unavailable),
			"suspect", len(unavailable) > 0,
		}, characterGrowTypeAuditState(character)...)...)
}

// syncCurrentSceneSkillPointLedger is the login/reselect repair for characters
// whose level changed outside the ordinary EXP owner or whose historical
// ledger predates TP advancement. The target totals come from the runtime PVF
// SP/TP tables plus persisted bonus points. Remaining points preserve the
// already-spent amount and clamp exactly as the 86JP LoadAndSync domain rule.
func (s *Service) syncCurrentSceneSkillPointLedger(
	ctx context.Context,
	repos dnfrepo.Group,
	character dnfrepo.CharacterRecord,
	record dnfrepo.SkillRecord,
) (dnfrepo.SkillRecord, bool, error) {
	target, err := s.currentPVFSkillPointTarget(ctx, character)
	if err != nil {
		return dnfrepo.SkillRecord{}, false, err
	}
	_, changed, err := syncCurrentSkillPointState(record.Points, target)
	if err != nil {
		return record, false, err
	}
	if record.Trees.Expanded {
		_, otherChanged, syncErr := syncCurrentSkillPointState(record.Trees.Other.Points, target)
		if syncErr != nil {
			return record, false, syncErr
		}
		changed = changed || otherChanged
	}
	if !changed {
		return record, false, nil
	}
	owner, err := currentSceneSkillOwner(repos)
	if err != nil {
		return dnfrepo.SkillRecord{}, false, err
	}
	return owner.SyncPoints(ctx, dnfskill.SyncPointsCommand{
		CharacterID: character.CharacterID,
		Target:      target,
		UpdatedAt:   time.Now().UTC(),
	})
}

func (s *Service) ensureCurrentSceneSkillLayout(ctx context.Context, repos dnfrepo.Group, character dnfrepo.CharacterRecord, job byte, catalog *dnfskill.Table, record dnfrepo.SkillRecord) (dnfrepo.SkillRecord, dnfrepo.SkillLayout, bool, error) {
	normalized := false
	if job == darkcombo.Job && repos.CharacterSkills != nil {
		var err error
		record, normalized, err = normalizeDarkKnightLayouts(ctx, repos, catalog, character.CharacterID)
		if err != nil {
			return dnfrepo.SkillRecord{}, nil, false, err
		}
	}
	if existing := record.Layouts[dnfrepo.ActiveSkillTree(record)]; len(existing) > 0 {
		layout, err := skillcmd.BuildCurrentSkillLayout(catalog, job, dnfrepo.ActiveSkillTree(record), record.Skills, existing)
		return record, layout, normalized, err
	}
	initial, err := s.initialCharacterSkills(ctx, job)
	if err != nil {
		return dnfrepo.SkillRecord{}, nil, false, fmt.Errorf("load job %d initial skills for layout: %w", job, err)
	}
	build := func(current dnfrepo.SkillRecord) (dnfrepo.SkillLayout, error) {
		if existing := current.Layouts[dnfrepo.ActiveSkillTree(record)]; len(existing) > 0 {
			return skillcmd.BuildCurrentSkillLayout(catalog, job, dnfrepo.ActiveSkillTree(record), current.Skills, existing)
		}
		if skillRecordMatchesPVFInitialSkills(current.Skills, initial) {
			return skillcmd.BuildInitialSkillLayout(catalog, job, dnfrepo.ActiveSkillTree(record), current.Skills)
		}
		return skillcmd.BuildCurrentSkillLayout(catalog, job, dnfrepo.ActiveSkillTree(record), current.Skills, nil)
	}
	candidate, err := build(record)
	if err != nil {
		return dnfrepo.SkillRecord{}, nil, false, err
	}
	if repos.CharacterSkills == nil {
		return record, candidate, false, nil
	}
	owner, err := currentSceneSkillOwner(repos)
	if err != nil {
		return dnfrepo.SkillRecord{}, nil, false, err
	}
	return owner.EnsureLayout(ctx, dnfskill.EnsureLayoutCommand{
		CharacterID: character.CharacterID,
		TreeIndex:   dnfrepo.ActiveSkillTree(record),
		UpdatedAt:   time.Now().UTC(),
		Build:       build,
	})
}

func skillRecordMatchesPVFInitialSkills(states map[int64]dnfrepo.SkillState, initial []initialSkillEntry) bool {
	expected := make(map[int64]int, len(initial))
	for _, entry := range initial {
		if entry.SkillID > 0 && entry.Level > 0 {
			expected[entry.SkillID] = entry.Level
		}
	}
	if len(states) != len(expected) || len(expected) == 0 {
		return false
	}
	for skillID, level := range expected {
		state, ok := states[skillID]
		if !ok || !state.Enabled || state.Level != level {
			return false
		}
	}
	return true
}
