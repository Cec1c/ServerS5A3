package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

type currentSceneSkillInfoProjection struct {
	body            []byte
	characterID     string
	job             byte
	learnedCount    int
	learnedSkillIDs []int64
	// learnedLevels 是本次技能页真正写进包体的"技能 id → 等级"向量（只含 level>0 且在
	// layout 里有槽位的技能）。它是客户端此刻**唯一**能看到的技能状态，所以
	// `skill_equip_requirement_audit.go` 用它来对照穿戴装备在 PVF 里点名的掌握技能。
	learnedLevels   map[int64]int
	activeSlots     []int
	quickSkillIDs   []uint16
	commandSkillIDs []uint16
}

// loadSkillCommandProjection reads the op331-persisted custom command vector.
// A missing/unavailable store degrades to "no custom commands" (PVF defaults),
// never to a blocked skill panel.
func (s *Service) loadSkillCommandProjection(ctx context.Context, repos dnfrepo.Group, characterID string) map[uint16][]byte {
	settings, found, err := skillcmd.LoadSkillCommands(ctx, repos, characterID)
	if err != nil {
		if s != nil {
			s.logWarn("dnfbridge skill command projection failed",
				"character_id", characterID,
				"error", err)
		}
		return nil
	}
	if !found || len(settings.Commands) == 0 {
		return nil
	}
	return settings.Commands
}

func (s *Service) buildCurrentSceneSkillInfoProjection(
	session *gameSession,
	ctx context.Context,
	character dnfrepo.CharacterRecord,
	source string,
) (currentSceneSkillInfoProjection, bool, error) {
	if s == nil || session == nil {
		return currentSceneSkillInfoProjection{}, false, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Skill == nil {
		s.logGameEvent(session, "game-main-skill-info-deferred", "source", source, "reason", "skill_repository_missing")
		return currentSceneSkillInfoProjection{}, false, nil
	}
	job, ok := characterJobByte(character)
	if !ok {
		return currentSceneSkillInfoProjection{}, false, fmt.Errorf("current scene skill info has invalid job %q", character.Job)
	}
	record, backfilled, err := s.loadOrBackfillCurrentSceneSkillRecord(ctx, repos, character, job)
	if err != nil {
		return currentSceneSkillInfoProjection{}, false, fmt.Errorf("load current scene skill info: %w", err)
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	if catalog == nil {
		s.logGameEvent(session, "game-main-skill-info-deferred", "source", source, "reason", "skill_catalog_missing")
		return currentSceneSkillInfoProjection{}, false, nil
	}
	record, layout, layoutBackfilled, err := s.ensureCurrentSceneSkillLayout(ctx, repos, character, job, catalog, record)
	if err != nil {
		return currentSceneSkillInfoProjection{}, false, fmt.Errorf("build current scene skill layout: %w", err)
	}
	if backfilled {
		s.logGameEvent(session, "game-main-skill-info-backfilled",
			"source", source,
			"character_id", character.CharacterID,
			"job", job,
			"learned_count", len(record.Skills),
			"layout_count", len(layout),
			"record_source", "runtime_pvf_character_initial_value_skill")
	}
	if layoutBackfilled {
		s.logGameEvent(session, "game-main-skill-layout-backfilled",
			"source", source,
			"character_id", character.CharacterID,
			"job", job,
			"layout_count", len(layout),
			"layout_source", "job_scoped_pvf_initial_skill_layout")
	}
	commands := s.loadSkillCommandProjection(ctx, repos, character.CharacterID)
	body, activeSlots, err := buildCurrentSceneSkillInfoBody(record, layout, commands)
	if err != nil {
		return currentSceneSkillInfoProjection{}, false, err
	}
	learnedSkillIDs := make([]int64, 0, len(record.Skills))
	learnedLevels := make(map[int64]int, len(record.Skills))
	for skillID, state := range record.Skills {
		if state.Enabled && state.Level > 0 {
			learnedSkillIDs = append(learnedSkillIDs, skillID)
			learnedLevels[skillID] = state.Level
		}
	}
	sort.Slice(learnedSkillIDs, func(left, right int) bool {
		return learnedSkillIDs[left] < learnedSkillIDs[right]
	})
	quickSkillIDs := make([]uint16, 0, len(activeSlots))
	for _, slot := range activeSlots {
		quickSkillIDs = append(quickSkillIDs, layout[slot])
	}
	commandSkillIDs := make([]uint16, 0, len(commands))
	for skillID := range commands {
		if state, ok := record.Skills[int64(skillID)]; ok && state.Enabled && state.Level > 0 {
			commandSkillIDs = append(commandSkillIDs, skillID)
		}
	}
	sort.Slice(commandSkillIDs, func(left, right int) bool { return commandSkillIDs[left] < commandSkillIDs[right] })
	projection := currentSceneSkillInfoProjection{
		body:            append([]byte(nil), body...),
		characterID:     character.CharacterID,
		job:             job,
		learnedCount:    len(record.Skills),
		learnedSkillIDs: append([]int64(nil), learnedSkillIDs...),
		learnedLevels:   learnedLevels,
		activeSlots:     append([]int(nil), activeSlots...),
		quickSkillIDs:   append([]uint16(nil), quickSkillIDs...),
		commandSkillIDs: commandSkillIDs,
	}
	// 穿戴装备在 PVF 里点名的掌握技能（`[required job skill]`）与本页的对照判读。
	// 只观察、不改包体；开关与回退档见 skill_equip_requirement_audit.go。
	s.auditLoginEquipRequirements(session, ctx, character, projection, source)
	return projection, true, nil
}

func (s *Service) sendCurrentSceneSkillInfoProjection(
	session *gameSession,
	projection currentSceneSkillInfoProjection,
	source string,
) error {
	s.logGameEvent(session, "game-main-skill-info-send",
		"source", source,
		"character_id", projection.characterID,
		"job", projection.job,
		"msg_id", currentSkillInfoMsgID,
		"classification", 0,
		"body_len", len(projection.body),
		"learned_count", projection.learnedCount,
		"learned_skill_ids", projection.learnedSkillIDs,
		"active_quick_slots", projection.activeSlots,
		"active_quick_skill_ids", projection.quickSkillIDs,
		"custom_command_skill_ids", projection.commandSkillIDs,
		"tree_count", currentSkillInfoTreeCount,
		"wire_level_encoding", "persisted_level_direct",
		"body_source", "current_exe_sub_1D6E240_length_prefixed_DNFPB_skillinfo")
	return s.sendGameUpperRawClass(session, currentSkillInfoMsgID, projection.body, 0)
}

func (s *Service) sendCurrentSceneSkillInfo(session *gameSession, ctx context.Context, character dnfrepo.CharacterRecord, source string) error {
	projection, ok, err := s.buildCurrentSceneSkillInfoProjection(session, ctx, character, source)
	if err != nil || !ok {
		return err
	}
	if err := s.sendCurrentSceneSkillInfoProjection(session, projection, source); err != nil {
		return err
	}
	// 技能换装背包 / 「Buff 强化」换装栏：与 `character:<id>:skill_commands`
	// 的加载点是同一个投影函数族。这里覆盖两条重新同步边界 ——
	// 重新登录（scene_finish_loading.go 的 op37 finish-loading）与
	// 选择角色/进城（initial_town_player_state.go 的 mode1 之后、typed op24 之前）。
	// 档位、四条边界的完整清单与残余不确定见 skill_switch_inventory.go。
	return s.sendSkillSwitchInventoryProjection(session, source+"_skill_switch_inventory")
}

// sendSelectedActorCurrentSceneSkillInfo reloads the selected character before
// projecting op19. Existing-actor mode0/mode1 rebinds can replace the native
// skill manager, so callers must invoke this only after the final actor rebind,
// never from the DLL or before the repository-backed appearance packets.
func (s *Service) sendSelectedActorCurrentSceneSkillInfo(session *gameSession, source string) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return fmt.Errorf("selected actor skill projection could not load character repository")
	}
	characterID := fmt.Sprintf("%d", session.selectedCharacterID)
	character, found, err := repos.Character.Load(ctx, characterID)
	if err != nil {
		return fmt.Errorf("load selected actor %s for skill projection: %w", characterID, err)
	}
	if !found {
		return fmt.Errorf("selected actor %s for skill projection was not found", characterID)
	}
	if accountID := strings.TrimSpace(s.accountIDForSession(session)); accountID != "" &&
		accountID != strings.TrimSpace(character.AccountID) {
		return fmt.Errorf("selected actor %s skill projection owner mismatch", characterID)
	}
	projection, ready, err := s.buildCurrentSceneSkillInfoProjection(
		session,
		ctx,
		character,
		source,
	)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("selected actor %s skill projection is unavailable", characterID)
	}
	if err := s.sendCurrentSceneSkillInfoProjection(session, projection, source); err != nil {
		return err
	}
	// 技能换装背包 / 「Buff 强化」换装栏的另外两条边界：穿脱装备
	// （game_aligned.go 的 `aligned_<op>_after_ack`）与进城/副本返回的
	// op30 之后（town_post_transition_player_state.go）。两条都走本函数。
	if err := s.sendSkillSwitchInventoryProjection(session, source+"_skill_switch_inventory"); err != nil {
		return err
	}
	return s.sendBuffSwapReapply(session, source+"_after_selected_actor_skills")
}
