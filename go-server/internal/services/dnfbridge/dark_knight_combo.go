package dnfbridge

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

const (
	currentDarkKnightComboFailureCode byte = 4
	currentDarkKnightComboAutoWindow       = 10 * time.Second
	// A current-client scene rebuild can emit an all-empty op508 roughly nine
	// seconds after the authoritative op447 projection. Keep enough margin to
	// reject that echo without blocking a later explicit editor operation.
	currentDarkKnightComboProjectionEchoWindow = 15 * time.Second

	// These are class-0 S2C notifications in the current NoPack client. They
	// intentionally do not use dnfenum: the similarly numbered C2S command
	// entries have unrelated meanings in the public packet enum.
	currentDarkKnightComboInitializeMsgID uint16 = 447
	currentDarkKnightComboPreloadMsgID    uint16 = 809
)

type darkKnightComboSessionState struct {
	finalTownSceneMu             sync.Mutex
	mu                           sync.Mutex
	pendingCharacterID           uint16
	pendingPage                  byte
	pendingDeadline              time.Time
	projectionCharacterID        uint16
	projectionGeneration         uint64
	projectionAt                 time.Time
	projectionPages              map[byte]darkKnightComboProjectionPageState
	finalTownSceneCharacterID    uint16
	finalTownSceneGeneration     uint64
	finalTownSceneInitialization bool
}

type darkKnightComboProjectionPageState struct {
	canonicalHash     [sha256.Size]byte
	canonicalNonEmpty bool
	acceptedNonEmpty  bool
}

type darkKnightComboSaveGuard struct {
	reject        bool
	canonicalEcho bool
	generation    uint64
	age           time.Duration
	hash          [sha256.Size]byte
}

// reset drops every character- and actor-generation-scoped combo gate while
// preserving the mutex values themselves. A game connection can return to the
// selector and bind another character, so carrying an op510 token, an op447
// projection hash, or a completed-town once key across that boundary would let
// one character suppress or classify another character's native combo flow.
func (state *darkKnightComboSessionState) reset() {
	if state == nil {
		return
	}
	state.finalTownSceneMu.Lock()
	defer state.finalTownSceneMu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.pendingCharacterID = 0
	state.pendingPage = 0
	state.pendingDeadline = time.Time{}
	state.projectionCharacterID = 0
	state.projectionGeneration = 0
	state.projectionAt = time.Time{}
	state.projectionPages = nil
	state.finalTownSceneCharacterID = 0
	state.finalTownSceneGeneration = 0
	state.finalTownSceneInitialization = false
}

func (state *darkKnightComboSessionState) arm(characterID uint16, page byte, deadline time.Time) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.pendingCharacterID = characterID
	state.pendingPage = page
	state.pendingDeadline = deadline
}

func (state *darkKnightComboSessionState) consume(characterID uint16, page byte, now time.Time) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingDeadline.IsZero() {
		return false
	}
	if !now.Before(state.pendingDeadline) {
		state.pendingCharacterID = 0
		state.pendingPage = 0
		state.pendingDeadline = time.Time{}
		return false
	}
	if state.pendingCharacterID != characterID || state.pendingPage != page {
		return false
	}
	state.pendingCharacterID = 0
	state.pendingPage = 0
	state.pendingDeadline = time.Time{}
	return true
}

func (state *darkKnightComboSessionState) recordProjection(
	characterID uint16,
	pages []darkcombo.Page,
	now time.Time,
) uint64 {
	projectionPages := make(map[byte]darkKnightComboProjectionPageState, len(pages))
	for _, page := range pages {
		block, err := darkcombo.EncodePageBlock(page)
		if err != nil {
			continue
		}
		projectionPages[page.Index] = darkKnightComboProjectionPageState{
			canonicalHash:     sha256.Sum256(block),
			canonicalNonEmpty: darkKnightComboPageNonEmpty(page),
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.projectionGeneration++
	state.projectionCharacterID = characterID
	state.projectionAt = now
	state.projectionPages = projectionPages
	return state.projectionGeneration
}

func (state *darkKnightComboSessionState) inspectSave(
	characterID uint16,
	page darkcombo.Page,
	now time.Time,
	matchedReset bool,
) darkKnightComboSaveGuard {
	block, _ := darkcombo.EncodePageBlock(page)
	incomingHash := sha256.Sum256(block)
	state.mu.Lock()
	defer state.mu.Unlock()
	guard := darkKnightComboSaveGuard{generation: state.projectionGeneration}
	if state.projectionCharacterID != characterID || state.projectionAt.IsZero() {
		return guard
	}
	projected, found := state.projectionPages[page.Index]
	if !found {
		return guard
	}
	guard.hash = projected.canonicalHash
	guard.canonicalEcho = incomingHash == projected.canonicalHash
	if matchedReset || darkKnightComboPageNonEmpty(page) || !projected.canonicalNonEmpty || projected.acceptedNonEmpty {
		return guard
	}
	guard.age = now.Sub(state.projectionAt)
	guard.reject = guard.age >= 0 && guard.age < currentDarkKnightComboProjectionEchoWindow
	return guard
}

func (state *darkKnightComboSessionState) markAcceptedNonEmpty(
	characterID uint16,
	page byte,
	generation uint64,
) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.projectionCharacterID != characterID || state.projectionGeneration != generation {
		return
	}
	projected, found := state.projectionPages[page]
	if !found {
		return
	}
	projected.acceptedNonEmpty = true
	state.projectionPages[page] = projected
}

func darkKnightComboPageNonEmpty(page darkcombo.Page) bool {
	for _, root := range page.Roots {
		if len(root.Children) != 0 {
			return true
		}
	}
	return false
}

func (s *Service) currentDarkKnightComboNow() time.Time {
	now := s.gameplayNow().UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now
}

// loadDarkKnightComboPages lazily creates the missing independent combo
// pages for preexisting job-9 characters. It never changes learned skills,
// ordinary quick slots, or commands.
func (s *Service) loadDarkKnightComboPages(
	ctx context.Context,
	repos dnfrepo.Group,
	character dnfrepo.CharacterRecord,
	catalog *dnfskill.Table,
	record dnfrepo.SkillRecord,
) ([]darkcombo.Page, error) {
	job, ok := characterJobByte(character)
	if !ok {
		return nil, fmt.Errorf("dark knight combo character has invalid job %q", character.Job)
	}
	if job != darkcombo.Job {
		return nil, nil
	}
	if repos.Settings == nil {
		return nil, fmt.Errorf("dark knight combo settings repository is unavailable")
	}

	pages, found, err := darkcombo.LoadPages(ctx, repos.Settings, character.CharacterID)
	if err != nil {
		return nil, err
	}
	needsSeed := !found
	if found && record.Trees.Expanded {
		indexes := make(map[byte]bool)
		for _, page := range pages {
			indexes[page.Index] = true
		}
		needsSeed = !indexes[0] || !indexes[1]
	}
	if !needsSeed {
		return darkcombo.FilterLearnedPages(record, pages), nil
	}
	s.initialEquipmentMu.Lock()
	if s.initialEquipmentArchive == nil && strings.TrimSpace(s.options.pvfPath) == "" {
		s.initialEquipmentMu.Unlock()
		return darkcombo.FilterLearnedPages(record, pages), nil
	}
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	if repos.CharacterSettlement == nil {
		return nil, fmt.Errorf("连招初始化缺少角色事务")
	}
	err = repos.CharacterSettlement.WithinCharacterSettlement(ctx, character.CharacterID, func(tx dnfrepo.Group) error {
		if tx.Settings == nil || tx.Skill == nil {
			return fmt.Errorf("连招初始化事务仓储缺失")
		}
		current, found, e := tx.Skill.Load(ctx, character.CharacterID)
		if e != nil {
			return e
		}
		if !found {
			return fmt.Errorf("连招初始化技能记录缺失")
		}
		saved, _, e := darkcombo.LoadPages(ctx, tx.Settings, character.CharacterID)
		if e != nil {
			return e
		}
		defaults, e := darkcombo.BuildDefaultPages(archive, catalog, current)
		if e != nil {
			return e
		}
		indexes := make(map[byte]bool)
		for _, page := range saved {
			indexes[page.Index] = true
		}
		changed := false
		for _, page := range defaults {
			if !indexes[page.Index] {
				saved = append(saved, page)
				changed = true
			}
		}
		if len(saved) == 0 {
			return fmt.Errorf("PVF没有已学习的黑武连招根技能")
		}
		if changed {
			if e := darkcombo.SavePages(ctx, tx.Settings, character.CharacterID, saved, s.currentDarkKnightComboNow()); e != nil {
				return e
			}
		}
		pages = darkcombo.FilterLearnedPages(current, saved)
		return nil
	})
	return pages, err
}

// sendCurrentDarkKnightComboInitialization sends the native class-0/op447
// body while a selected Dark Knight is being built. It is deliberately not
// part of ordinary op19 reloads or op454 refreshes. The current client does,
// however, clear the native combo manager after a final scene rebuild. The
// completed-dungeon, completed-return, and typed-town-transition boundaries
// therefore restore this exact packet only after the actor skill manager has
// been rebuilt.
func (s *Service) sendCurrentDarkKnightComboInitialization(
	session *gameSession,
	ctx context.Context,
	character dnfrepo.CharacterRecord,
	source string,
) error {
	if s == nil || session == nil {
		return nil
	}
	job, ok := characterJobByte(character)
	if !ok || job != darkcombo.Job {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Skill == nil || repos.Settings == nil {
		return fmt.Errorf("dark knight combo initialization repositories are unavailable")
	}
	record, found, err := repos.Skill.Load(ctx, character.CharacterID)
	if err != nil {
		return fmt.Errorf("load dark knight combo skill record: %w", err)
	}
	if !found || len(record.Skills) == 0 {
		return fmt.Errorf("dark knight combo skill record is unavailable after op19 initialization")
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	if catalog == nil {
		return fmt.Errorf("dark knight combo skill catalog is unavailable")
	}
	pages, err := s.loadDarkKnightComboPages(ctx, repos, character, catalog, record)
	if err != nil {
		return err
	}
	body, err := darkcombo.EncodeNotificationBody(pages)
	if err != nil {
		return fmt.Errorf("encode dark knight combo initialization: %w", err)
	}
	if err := s.sendGameUpperRawClass(session, currentDarkKnightComboInitializeMsgID, body, 0); err != nil {
		return err
	}
	projectionGeneration := session.darkKnightCombo.recordProjection(
		session.selectedCharacterID,
		pages,
		s.currentDarkKnightComboNow(),
	)
	s.logGameEvent(session, "game-dark-knight-combo-initialized",
		"source", source,
		"character_id", character.CharacterID,
		"msg_id", currentDarkKnightComboInitializeMsgID,
		"classification", 0,
		"apply_and_echo", 0,
		"page_count", len(pages),
		"projection_generation", projectionGeneration,
		"body_source", "current_exe_sub_1FFC1A0_class0_op447")
	return nil
}

// sendSelectedDarkKnightComboInitialization loads the committed selected actor
// at a final scene boundary and restores only its native job-9 combo pages.
// It is intentionally safe for every other job: those paths emit no packet.
func (s *Service) sendSelectedDarkKnightComboInitialization(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID, _, character, found := s.selectedCharacterForEnter(ctx, session)
	if characterID == 0 || !found {
		return nil
	}
	return s.sendCurrentDarkKnightComboInitialization(session, ctx, character, source)
}

// sendSelectedDarkKnightComboAfterFinalTownSceneOnce restores the job-9
// combo manager only after the selected town actor's deferred tail has
// committed its final native mode1. The key includes the native scene/actor
// generation, because a completed dungeon return rebuilds the same selected
// character without changing the connection's character generation.
//
// A dedicated serial mutex spans the single op447 attempt while the state
// mutex remains available to record the successful projection. Concurrent
// ready callbacks wait and re-check the exact character/generation key; a
// failed write therefore lets the next callback retry only op447 without
// replaying the already committed deferred tail.
func (s *Service) sendSelectedDarkKnightComboAfterFinalTownSceneOnce(
	session *gameSession,
	source string,
) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}

	session.townMu.Lock()
	characterID := session.selectedCharacterID
	generation := session.sceneBootstrapActorGeneration
	ready := session.initialTownRouteCharacterID == characterID &&
		session.initialTownRouteStage >= currentInitialTownRoutePlayerStateSent &&
		session.sceneBootstrapTailSent &&
		!session.sceneBootstrapTailDeferred
	session.townMu.Unlock()
	if !ready {
		return nil
	}

	state := &session.darkKnightCombo
	state.finalTownSceneMu.Lock()
	defer state.finalTownSceneMu.Unlock()

	// This callback may have waited behind a preceding attempt. Re-read the
	// actor key so a scene or character change cannot be acknowledged by the
	// previous generation's write.
	session.townMu.Lock()
	characterID = session.selectedCharacterID
	generation = session.sceneBootstrapActorGeneration
	ready = session.initialTownRouteCharacterID == characterID &&
		session.initialTownRouteStage >= currentInitialTownRoutePlayerStateSent &&
		session.sceneBootstrapTailSent &&
		!session.sceneBootstrapTailDeferred
	session.townMu.Unlock()
	if !ready || characterID == 0 {
		return nil
	}

	state.mu.Lock()
	if state.finalTownSceneInitialization &&
		state.finalTownSceneCharacterID == characterID &&
		state.finalTownSceneGeneration == generation {
		state.mu.Unlock()
		return nil
	}
	state.mu.Unlock()

	// A missing repository/record is retryable and must not close the gate. A
	// resolved non-Dark-Knight is the only packet-silent successful outcome.
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	resolvedID, _, character, found := s.selectedCharacterForEnter(ctx, session)
	if !found || resolvedID != characterID {
		cancel()
		return nil
	}
	job, validJob := characterJobByte(character)
	if !validJob {
		cancel()
		return nil
	}
	if job == darkcombo.Job {
		err := s.sendCurrentDarkKnightComboInitialization(session, ctx, character, source)
		cancel()
		if err != nil {
			return err
		}
	} else {
		cancel()
	}

	state.mu.Lock()
	state.finalTownSceneCharacterID = characterID
	state.finalTownSceneGeneration = generation
	state.finalTownSceneInitialization = true
	state.mu.Unlock()
	return nil
}

// sendCurrentDarkKnightComboPreload sends the native character-select cache
// for existing durable combo pages. New/legacy characters without a saved page
// are left for the selected-character op447 initializer, where the PVF-backed
// lazy seed is safe and complete.
func (s *Service) sendCurrentDarkKnightComboPreload(
	session *gameSession,
	ctx context.Context,
	characters []dnfrepo.CharacterRecord,
	source string,
) error {
	if s == nil || session == nil || len(characters) == 0 {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return nil
	}
	entries := make([]darkcombo.PreloadEntry, 0, len(characters))
	for _, character := range characters {
		job, valid := characterJobByte(character)
		if !valid || job != darkcombo.Job || character.Slot < 0 || character.Slot > 0xff {
			continue
		}
		pages, found, err := darkcombo.LoadPages(ctx, repos.Settings, character.CharacterID)
		if err != nil {
			return fmt.Errorf("load dark knight combo preload %s: %w", character.CharacterID, err)
		}
		if !found {
			continue
		}
		entries = append(entries, darkcombo.PreloadEntry{Slot: byte(character.Slot), Pages: pages})
	}
	if len(entries) == 0 {
		return nil
	}
	body, err := darkcombo.EncodePreloadNotificationBody(entries)
	if err != nil {
		return fmt.Errorf("encode dark knight combo preload: %w", err)
	}
	if err := s.sendGameUpperRawClass(session, currentDarkKnightComboPreloadMsgID, body, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-dark-knight-combo-preloaded",
		"source", source,
		"msg_id", currentDarkKnightComboPreloadMsgID,
		"classification", 0,
		"character_count", len(entries),
		"body_source", "current_exe_sub_1FFC580_class0_op809")
	return nil
}

func normalizeLegacyDarkKnightComboSkillInfoBody(body []byte) []byte {
	if _, err := darkcombo.ParsePageBlock(body); err == nil {
		return append([]byte(nil), body...)
	}
	// Captured current-client op508 bodies have one trailing transport byte.
	// Retain the complete page only when the shortened body validates exactly.
	for _, trailerSize := range []int{1, 4} {
		if len(body) <= trailerSize {
			continue
		}
		semantic := body[:len(body)-trailerSize]
		if _, err := darkcombo.ParsePageBlock(semantic); err == nil {
			return append([]byte(nil), semantic...)
		}
	}
	return append([]byte(nil), body...)
}

func (s *Service) handleCurrentDarkKnightComboSkillInfo(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketComboSkillInfo)
	page, err := darkcombo.ParsePageBlock(body)
	if err != nil {
		s.logGameEvent(session, "game-dark-knight-combo-save-rejected", "reason", err, "body_len", len(body))
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "parse_page")
	}
	page, err = darkcombo.NormalizePage(page)
	if err != nil {
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "normalize_page")
	}
	character, repos, err := s.currentDarkKnightComboCharacter(session)
	if err != nil {
		s.logGameEvent(session, "game-dark-knight-combo-save-rejected", "reason", err, "body_len", len(body))
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "character")
	}
	now := s.currentDarkKnightComboNow()
	autoCommit := session.darkKnightCombo.consume(session.selectedCharacterID, page.Index, now)
	guard := session.darkKnightCombo.inspectSave(session.selectedCharacterID, page, now, autoCommit)
	if guard.reject {
		s.logGameEvent(session, "game-dark-knight-combo-empty-save-rejected",
			"character_id", character.CharacterID,
			"page", page.Index,
			"projection_generation", guard.generation,
			"projection_age_ms", guard.age.Milliseconds(),
			"canonical_hash", fmt.Sprintf("%x", guard.hash[:]),
			"reason", "unmatched_scene_projection_echo")
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "empty_projection_echo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	cleaned, layoutChanged, err := persistCurrentDarkKnightComboPage(
		ctx,
		repos,
		character.CharacterID,
		page,
		autoCommit,
		now,
	)
	if err != nil {
		s.logGameEvent(session, "game-dark-knight-combo-save-rejected", "reason", err, "body_len", len(body))
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "persist_page_and_layout")
	}
	if darkKnightComboPageNonEmpty(page) && !guard.canonicalEcho {
		session.darkKnightCombo.markAcceptedNonEmpty(session.selectedCharacterID, page.Index, guard.generation)
	}
	if layoutChanged {
		s.logGameEvent(session, "game-dark-knight-combo-auto-save-layout-cleaned",
			"character_id", character.CharacterID, "page", page.Index, "quick_slots_cleaned", cleaned)
	}
	s.logGameEvent(session, "game-dark-knight-combo-saved",
		"character_id", character.CharacterID,
		"page", page.Index,
		"root_count", len(page.Roots),
		"auto_commit", autoCommit,
		"quick_slots_cleaned", cleaned,
		"body_len", len(body))
	return s.sendGameUpperSuccess(session, opcode, nil)
}

// persistCurrentDarkKnightComboPage keeps the op510 -> op508 auto-commit's
// independent combo page and ordinary skill layout in one character-scoped
// repository transaction. A failed layout write therefore cannot leave a new
// durable page paired with the old shortcut ownership (or vice versa).
func persistCurrentDarkKnightComboPage(
	ctx context.Context,
	repos dnfrepo.Group,
	characterID string,
	page darkcombo.Page,
	autoCommit bool,
	now time.Time,
) (cleaned int, layoutChanged bool, err error) {
	if repos.CharacterSettlement == nil {
		return 0, false, fmt.Errorf("dark knight combo settlement transaction is unavailable")
	}
	err = repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		if tx.Settings == nil || tx.Skill == nil {
			return fmt.Errorf("dark knight combo settlement repositories are unavailable")
		}
		pages, found, err := darkcombo.LoadPages(ctx, tx.Settings, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("persisted combo page template is unavailable")
		}
		current, found, err := tx.Skill.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || len(current.Skills) == 0 {
			return fmt.Errorf("dark knight combo skill record is unavailable inside save transaction")
		}
		if err := validateCurrentDarkKnightComboPage(page, pages, current); err != nil {
			return err
		}
		if err := darkcombo.SavePage(ctx, tx.Settings, characterID, page, now); err != nil {
			return err
		}
		if !autoCommit {
			return nil
		}
		updated, moved, err := darkcombo.CleanSavedChildrenQuickSlots(
			current.Layouts[int(page.Index)],
			darkcombo.RootSkillIDs(pages),
			darkcombo.ChildSkillIDs(page),
		)
		if err != nil {
			return err
		}
		cleaned = moved
		layoutChanged = moved > 0
		if !layoutChanged {
			return nil
		}
		if current.Layouts == nil {
			current.Layouts = make(map[int]dnfrepo.SkillLayout)
		}
		current.Layouts[int(page.Index)] = updated
		current.UpdatedAt = now.UTC()
		return dnfrepo.SaveSkillFields(ctx, tx.Skill, current, dnfrepo.SkillFieldLayouts)
	})
	return cleaned, layoutChanged, err
}

func validateCurrentDarkKnightComboPage(
	page darkcombo.Page,
	currentPages []darkcombo.Page,
	record dnfrepo.SkillRecord,
) error {
	var template *darkcombo.Page
	allRoots := darkcombo.RootSkillIDs(currentPages)
	for index := range currentPages {
		if currentPages[index].Index == page.Index {
			template = &currentPages[index]
			break
		}
	}
	if template == nil || len(page.Roots) != len(template.Roots) {
		return fmt.Errorf("dark knight combo page %d root template mismatch", page.Index)
	}
	seenRoots := make(map[uint16]struct{}, len(page.Roots))
	for index, root := range page.Roots {
		if root.SkillID == 0 || root.SkillID != template.Roots[index].SkillID {
			return fmt.Errorf("dark knight combo page %d root %d mismatch", page.Index, index)
		}
		if _, duplicate := seenRoots[root.SkillID]; duplicate {
			return fmt.Errorf("dark knight combo page %d has duplicate root %d", page.Index, root.SkillID)
		}
		seenRoots[root.SkillID] = struct{}{}
		if len(root.Children) > darkcombo.MaximumStageCount {
			return fmt.Errorf("dark knight combo root %d has %d stages", root.SkillID, len(root.Children))
		}
	}
	state, found := darkcombo.LearnedProfile(record, int(page.Index))
	if !found {
		return fmt.Errorf("dark knight combo skill tree %d is unavailable", page.Index)
	}
	for _, root := range page.Roots {
		for _, child := range root.Children {
			if _, isRoot := allRoots[child]; isRoot {
				return fmt.Errorf("dark knight combo child %d is a combo root", child)
			}
			learned, exists := state.Skills[int64(child)]
			if !exists || !learned.Enabled || learned.Level <= 0 {
				return fmt.Errorf("dark knight combo child %d is not learned on page %d", child, page.Index)
			}
		}
	}
	return nil
}

func (s *Service) handleCurrentDarkKnightComboQuickSlotReset(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketComboSkillExtensionQuickSlotReset)
	if len(body) != 2 || body[0] >= darkcombo.MaximumPageCount || (body[1] != 0 && body[1] != 2) {
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "invalid_request")
	}
	pageIndex, mode := body[0], body[1]
	character, repos, err := s.currentDarkKnightComboCharacter(session)
	if err != nil {
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "character")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	if catalog == nil {
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "catalog")
	}
	record, found, err := repos.Skill.Load(ctx, character.CharacterID)
	if err != nil || !found {
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "skill_record")
	}
	pages, err := s.loadDarkKnightComboPages(ctx, repos, character, catalog, record)
	if err != nil {
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "load_pages")
	}
	rootIDs := darkcombo.RootSkillIDs(pages)
	var savedChildren map[uint16]struct{}
	for _, candidate := range pages {
		if candidate.Index == pageIndex {
			savedChildren = darkcombo.ChildSkillIDs(candidate)
			break
		}
	}
	if savedChildren == nil {
		savedChildren = make(map[uint16]struct{})
	}
	now := s.currentDarkKnightComboNow()
	cleaned := 0
	changed := false
	if repos.CharacterSettlement == nil {
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "缺少连招事务")
	}
	err = repos.CharacterSettlement.WithinCharacterSettlement(ctx, character.CharacterID, func(tx dnfrepo.Group) error {
		current, found, e := tx.Skill.Load(ctx, character.CharacterID)
		if e != nil {
			return e
		}
		if !found {
			return fmt.Errorf("连招技能记录不存在")
		}
		state, exists := darkcombo.LearnedProfile(current, int(pageIndex))
		if !exists {
			return fmt.Errorf("连招技能页未解锁")
		}
		updated, moved, e := darkcombo.CleanAutoComboQuickSlots(catalog, state.Skills, current.Layouts[int(pageIndex)], rootIDs, savedChildren)
		if e != nil {
			return e
		}
		cleaned = moved
		changed = moved > 0
		if !changed {
			return nil
		}
		current = dnfrepo.CloneSkill(current)
		if current.Layouts == nil {
			current.Layouts = make(map[int]dnfrepo.SkillLayout)
		}
		current.Layouts[int(pageIndex)] = updated
		current.UpdatedAt = now
		return dnfrepo.SaveSkillFields(ctx, tx.Skill, current, dnfrepo.SkillFieldLayouts)
	})
	if err != nil {
		return s.sendCurrentDarkKnightComboFailureAndRestore(session, opcode, "重置连招布局失败")
	}
	if changed {
		// This is the one 86JP-prescribed ordinary skill refresh: it occurs
		// before the reset ACK because shortcuts were changed by the server.
		if err := s.sendSelectedActorCurrentSceneSkillInfoOnly(session, "dark_knight_auto_combo_pre_ack_skill_refresh"); err != nil {
			return err
		}
	}
	session.darkKnightCombo.arm(session.selectedCharacterID, pageIndex, now.Add(currentDarkKnightComboAutoWindow))
	s.logGameEvent(session, "game-dark-knight-combo-auto-reset-ready",
		"character_id", character.CharacterID,
		"page", pageIndex,
		"mode", mode,
		"quick_slots_cleaned", cleaned,
		"skill_info_refreshed", changed,
		"pending_window_ms", currentDarkKnightComboAutoWindow.Milliseconds())
	return s.sendGameUpperSuccess(session, opcode, nil)
}

func (s *Service) sendCurrentDarkKnightComboFailureAndRestore(
	session *gameSession,
	opcode uint16,
	source string,
) error {
	if err := s.sendGameUpperFailure(session, opcode, currentDarkKnightComboFailureCode); err != nil {
		return err
	}
	return s.sendSelectedDarkKnightComboInitialization(session, "failure_restore_"+source)
}

func (s *Service) currentDarkKnightComboCharacter(session *gameSession) (dnfrepo.CharacterRecord, dnfrepo.Group, error) {
	if session == nil || session.selectedCharacterID == 0 {
		return dnfrepo.CharacterRecord{}, dnfrepo.Group{}, fmt.Errorf("selected character is unavailable")
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil || repos.Skill == nil || repos.Settings == nil {
		return dnfrepo.CharacterRecord{}, dnfrepo.Group{}, fmt.Errorf("dark knight combo repositories are unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := fmt.Sprintf("%d", session.selectedCharacterID)
	character, found, err := repos.Character.Load(ctx, characterID)
	if err != nil {
		return dnfrepo.CharacterRecord{}, dnfrepo.Group{}, err
	}
	if !found {
		return dnfrepo.CharacterRecord{}, dnfrepo.Group{}, fmt.Errorf("character %s was not found", characterID)
	}
	if accountID := strings.TrimSpace(s.accountIDForSession(session)); accountID != "" && accountID != strings.TrimSpace(character.AccountID) {
		return dnfrepo.CharacterRecord{}, dnfrepo.Group{}, fmt.Errorf("character %s owner mismatch", characterID)
	}
	job, ok := characterJobByte(character)
	if !ok || job != darkcombo.Job {
		return dnfrepo.CharacterRecord{}, dnfrepo.Group{}, fmt.Errorf("character %s is not dark knight", characterID)
	}
	return character, repos, nil
}

func (s *Service) buildComboAwareCommittedSkillBody(ctx context.Context, character dnfrepo.CharacterRecord, record dnfrepo.SkillRecord, commands map[uint16][]byte) ([]byte, []int, error) {
	job, valid := characterJobByte(character)
	if !valid || job != darkcombo.Job {
		return buildCurrentSceneSkillInfoBody(record, record.Layouts[dnfrepo.ActiveSkillTree(record)], commands)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, nil, fmt.Errorf("连招仓储未就绪")
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	pages, err := s.loadDarkKnightComboPages(ctx, repos, character, catalog, record)
	if err != nil {
		return nil, nil, err
	}
	return buildCurrentSceneSkillInfoBodyWithCombo(record, record.Layouts[dnfrepo.ActiveSkillTree(record)], commands, pages)
}
func (s *Service) sendCommittedDarkKnightSkillRefresh(session *gameSession, character dnfrepo.CharacterRecord, record dnfrepo.SkillRecord, source string) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, _ := s.repositoryGroup()
	commands := s.loadSkillCommandProjection(ctx, repos, character.CharacterID)
	body, _, err := s.buildComboAwareCommittedSkillBody(ctx, character, record, commands)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, currentSkillInfoMsgID, body, 0); err != nil {
		return err
	}
	return s.sendCurrentDarkKnightComboInitialization(session, ctx, character, source)
}
