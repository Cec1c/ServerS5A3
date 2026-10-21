package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// partyDeathReturnRoomTransitionEnv 控制"组队副本里死亡回城倒计时是否跨过图保留"。
//
// 未设置/空/任意值 = `keep`（默认，修复档）；只有字面量 `legacy` 回到"过图即取消"。
const partyDeathReturnRoomTransitionEnv = "DNFBRIDGE_PARTY_DEATH_RETURN_ROOM_TRANSITION"

const partyDeathReturnRoomTransitionLegacyValue = "legacy"

// partyDeathReturnRoomTransitionMode 是死亡回城倒计时跨过图的档位。
type partyDeathReturnRoomTransitionMode int

const (
	// partyDeathReturnRoomTransitionKeep 是默认档，也是修复档（零值 = 修复）。
	partyDeathReturnRoomTransitionKeep partyDeathReturnRoomTransitionMode = iota
	// partyDeathReturnRoomTransitionLegacy 是改动前的"过图即取消"档。
	partyDeathReturnRoomTransitionLegacy
)

func readPartyDeathReturnRoomTransitionMode(value string) partyDeathReturnRoomTransitionMode {
	if strings.EqualFold(strings.TrimSpace(value), partyDeathReturnRoomTransitionLegacyValue) {
		return partyDeathReturnRoomTransitionLegacy
	}
	return partyDeathReturnRoomTransitionKeep
}

func (mode partyDeathReturnRoomTransitionMode) String() string {
	if mode == partyDeathReturnRoomTransitionLegacy {
		return partyDeathReturnRoomTransitionLegacyValue
	}
	return "keep"
}

// keeps 报告该档位是否在组队副本过图时保留已经成立的死亡回城倒计时。
func (mode partyDeathReturnRoomTransitionMode) keeps() bool {
	return mode == partyDeathReturnRoomTransitionKeep
}

func partyDeathReturnRoomTransitionModeFromEnv() partyDeathReturnRoomTransitionMode {
	return readPartyDeathReturnRoomTransitionMode(os.Getenv(partyDeathReturnRoomTransitionEnv))
}

const (
	currentDungeonDieCharacterBodySize = 4
	currentDungeonUseCoinBodySize      = 2
	currentDungeonDeathStateMsgID      = uint16(32)
	currentDungeonDeathStateBodySize   = 4
	// currentDungeonReviveStateType 是同一个 op32 布局里"角色已复活"的那一档
	// （厂商 `DungeonCombatHandler.cs:597` `noti.WriteByte(0x01); // state=1 revive`）。
	currentDungeonReviveStateType  = byte(1)
	currentDungeonDeathReturnDelay = 10 * time.Second
	// Current EXE class-1/op41 success body carries one additional u8 after the
	// success discriminator. 0 is the private-server accepted-revive flag.
	currentDungeonUseCoinSuccessFlag = byte(0)
	// Private-server playability: if the character is dead but has no coin
	// stack, still allow revive so friends are not soft-locked by the 10s timer.
	currentDungeonUseCoinAllowFreeRevive = true
)

var errGameplayTimeQueueUnavailable = errors.New("gameplay time queue is unavailable")

// handleDungeonDieCharacter accepts two u16 actor coordinates, optionally
// followed by a zero u16 emitted by the current EXE's non-15h sender branch.
// Nonzero extra fields are not interpreted here. Neither accepted shape supplies
// an actor identity; the selected runtime owner supplies the op32 scene key.
func (s *Service) handleDungeonDieCharacter(session *gameSession, body []byte) error {
	var partyDeathRuntime *runtimeDungeonState
	var raidLifeProjections []runtimeRaidState
	defer func() {
		for _, state := range raidLifeProjections {
			s.queueCurrentRaidDungeonProgress(state)
		}
		if partyDeathRuntime != nil {
			if err := s.broadcastPartyDungeonLife(session, partyDeathRuntime, false); err != nil {
				s.logGameEvent(session, "game-dungeon-party-life-sync-failed", "error", err)
			}
		}
	}()
	if session == nil {
		return nil
	}
	if len(body) != currentDungeonDieCharacterBodySize &&
		(len(body) != currentDungeonDieCharacterBodySize+2 ||
			binary.LittleEndian.Uint16(body[currentDungeonDieCharacterBodySize:]) != 0) {
		s.logGameEvent(session, "game-dungeon-character-death-blocked",
			"body_len", len(body),
			"expected_body_len", currentDungeonDieCharacterBodySize,
			"optional_zero_tail_body_len", currentDungeonDieCharacterBodySize+2,
			"reason", "current_exe_op40_coordinates_or_zero_tail_shape_mismatch")
		return nil
	}

	positionX := binary.LittleEndian.Uint16(body[0:2])
	positionY := binary.LittleEndian.Uint16(body[2:4])

	// Do not hold the runtime owner while loading the persisted town target.
	// Revalidate the exact run pointer after the repository read.
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	characterID, readyReason := currentDungeonDeathRuntimeOwner(runtime, session)
	session.dungeon.mu.Unlock()
	if readyReason != "" {
		s.logGameEvent(session, "game-dungeon-character-death-blocked",
			"body_len", len(body),
			"position_x_raw", positionX,
			"position_y_raw", positionY,
			"reason", readyReason)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	transition, err := s.prepareCurrentDungeonTownTransitionForSession(ctx, session, characterID)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-character-death-blocked",
			"char_id", characterID,
			"body_len", len(body),
			"reason", "persisted_town_transition_unavailable",
			"error", err)
		return nil
	}

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.dungeon.runtime != runtime {
		s.logGameEvent(session, "game-dungeon-character-death-blocked",
			"char_id", characterID,
			"body_len", len(body),
			"reason", "dungeon_runtime_changed_during_town_transition_load")
		return nil
	}
	if _, readyReason = currentDungeonDeathRuntimeOwner(runtime, session); readyReason != "" {
		s.logGameEvent(session, "game-dungeon-character-death-blocked",
			"char_id", characterID,
			"body_len", len(body),
			"reason", readyReason)
		return nil
	}

	if err := s.armCurrentDungeonDeathReturnLocked(session, runtime, transition); err != nil {
		s.logGameEvent(session, "game-dungeon-character-death-blocked",
			"char_id", characterID,
			"body_len", len(body),
			"reason", "death_return_timer_schedule_failed",
			"error", err)
		return nil
	}

	actorObjectKey := currentSceneActorObjectKey(characterID)
	runtime.deathStateSent = false
	deathBody := buildCurrentDungeonDeathStateBody(actorObjectKey, 0, 0)
	s.logGameEvent(session, "game-dungeon-character-death-op32-send",
		"char_id", characterID,
		"actor_object_key", actorObjectKey,
		"position_x_raw", positionX,
		"position_y_raw", positionY,
		"position_x_signed", int16(positionX),
		"position_y_signed", int16(positionY),
		"request_msg_id", uint16(dnfenum.CmdPacketDieCharacter),
		"response_msg_id", currentDungeonDeathStateMsgID,
		"classification", 0,
		"body_len", len(deathBody),
		"death_type", 0,
		"flag", 0,
		"timer_delay_ms", currentDungeonDeathReturnDelay.Milliseconds(),
		"body_source", "current_exe_sub_1D407C0_u16_u8_u8_reader")
	if err := s.sendGameUpperRawClass(session, currentDungeonDeathStateMsgID, deathBody, 0); err != nil {
		s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "op32_socket_write_failed")
		return err
	}
	markPartyDungeonLife(runtime, characterID, true)
	runtime.deathStateSent = true
	update, accountingErr := s.recordCurrentRaidCharacterLifeLocked(session, runtime, true, time.Now())
	raidLifeProjections = update.Projections
	if accountingErr != nil {
		s.logGameEvent(session, "game-raid-death-accounting-rejected", "char_id", characterID, "error", accountingErr)
	}
	partyDeathRuntime = runtime
	return nil
}

func currentDungeonDeathRuntimeOwner(runtime *runtimeDungeonState, session *gameSession) (uint16, string) {
	if runtime == nil || runtime.Session == nil || session == nil {
		return 0, "active_dungeon_runtime_missing"
	}
	characterID := session.selectedCharacterID
	if characterID == 0 || !dungeonRuntimeOwnsCharacter(runtime, characterID) {
		return 0, "active_dungeon_runtime_character_owner_mismatch"
	}
	snapshot := runtime.Session.Snapshot()
	if snapshot.Run.Status != worldmap.DungeonRunActive {
		return 0, "dungeon_run_not_active"
	}
	if runtime.townReturnPending {
		return 0, "town_transition_already_pending"
	}
	return characterID, ""
}

func buildCurrentDungeonDeathStateBody(actorObjectKey uint16, deathType, flag byte) []byte {
	body := make([]byte, currentDungeonDeathStateBodySize)
	binary.LittleEndian.PutUint16(body[0:2], actorObjectKey)
	body[2] = deathType
	body[3] = flag
	return body
}

func (s *Service) armCurrentDungeonDeathReturnLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	transition currentDungeonTownTransition,
) error {
	if s == nil || session == nil || runtime == nil || session.dungeon.runtime != runtime || s.gameplayTimers == nil {
		return errGameplayTimeQueueUnavailable
	}
	if runtime.lifecycleToken == 0 {
		return fmt.Errorf("%w: runtime lifecycle token is zero", errGameplayTimeQueueUnavailable)
	}

	s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "replace_or_initialize_death_timer")
	generation := runtime.deathReturnGeneration
	timerName := fmt.Sprintf("dnf-dungeon-death:%s:run:%d", session.connID, runtime.lifecycleToken)
	runtime.deathReturnWaiting = true
	runtime.deathReturnTimerName = timerName
	runtime.deathReturnDueAt = s.gameplayTimers.Now().Add(currentDungeonDeathReturnDelay)
	runtime.deathReturnTransition = cloneCurrentDungeonTownTransition(transition)
	characterID, characterGeneration, err := gameSessionCharacterEventIdentity(session, session.selectedCharacterID)
	if err != nil {
		s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "death_timer_character_identity_changed")
		return err
	}
	dungeonID := runtime.Dungeon.ID
	runToken := runtime.lifecycleToken

	err = s.gameplayTimers.ScheduleAfter(
		timerName,
		currentDungeonDeathReturnDelay,
		func(due time.Time) {
			postErr := s.postGameSessionCharacterEvent(
				session,
				"dungeon-death-return-timequeue",
				characterID,
				characterGeneration,
				func() error {
					s.fireCurrentDungeonDeathReturn(session, runtime, generation, due)
					return nil
				},
			)
			if postErr != nil && !isClosedGameSessionEventError(postErr) {
				s.logPacketEvent("game-session-event-submit-failed",
					"conn_id", session.connID,
					"source", "dungeon-death-return-timequeue",
					"char_id", characterID,
					"character_generation", characterGeneration,
					"dungeon_id", dungeonID,
					"run_token", runToken,
					"error", postErr)
			}
		},
	)
	if err != nil {
		s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "death_timer_schedule_failed")
		return err
	}
	s.logGameEvent(session, "game-dungeon-character-death-timer-scheduled",
		"char_id", runtime.Character.CharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"run_token", runtime.lifecycleToken,
		"timer_generation", generation,
		"timer_name", timerName,
		"due_at_utc", runtime.deathReturnDueAt.UTC().Format(time.RFC3339Nano),
		"delay_ms", currentDungeonDeathReturnDelay.Milliseconds(),
		"source", "timequeue_one_shot")
	return nil
}

func (s *Service) fireCurrentDungeonDeathReturn(
	session *gameSession,
	runtime *runtimeDungeonState,
	generation uint64,
	due time.Time,
) {
	if s == nil || session == nil || runtime == nil {
		return
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.dungeon.runtime != runtime || !runtime.deathReturnWaiting ||
		runtime.deathReturnGeneration != generation || runtime.lifecycleToken == 0 {
		s.logGameEvent(session, "game-dungeon-character-death-timer-stale",
			"timer_generation", generation,
			"current_generation", runtime.deathReturnGeneration,
			"run_token", runtime.lifecycleToken,
			"runtime_is_current", session.dungeon.runtime == runtime,
			"waiting", runtime.deathReturnWaiting,
			"reason", "run_identity_or_generation_changed")
		return
	}
	if _, reason := currentDungeonDeathRuntimeOwner(runtime, session); reason != "" {
		s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "timer_fired_after_runtime_state_changed")
		s.logGameEvent(session, "game-dungeon-character-death-timer-stale",
			"char_id", runtime.Character.CharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_token", runtime.lifecycleToken,
			"timer_generation", generation,
			"reason", reason)
		return
	}

	s.logGameEvent(session, "game-dungeon-character-death-timer-fired",
		"char_id", runtime.Character.CharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"run_token", runtime.lifecycleToken,
		"timer_generation", generation,
		"scheduled_due_utc", due.UTC().Format(time.RFC3339Nano),
		"source", "timequeue_one_shot")
	// 死亡倒计时到期 ⇒ 先问机制"该回城还是该进锦标赛失败终止"
	// （厂商 `DungeonCombatHandler.cs:737 ResolveDeathDeadline`）。
	//
	// **默认档逐字节不变**：非锦标赛的 runtime 恒返回 ReturnToTown，
	// 下面的控制流与改动前完全相同（一个分支都不进）。
	switch s.resolveCurrentTournamentDeathDeadline(session, runtime) {
	case tournamentDeathDeadlinePresentTournamentRewards:
		// 锦标赛：取消回城、留在本局看奖励（厂商 `:750-766`）。
		// 被淘汰者走 `tryEliminate`，**永不**提交通关 intent。
		s.presentCurrentTournamentRewardsOnDeathLocked(session, runtime, generation)
		return
	case tournamentDeathDeadlineIgnore:
		// 失效 run / 非 Active：厂商 `:741-749` 的 `Ignore`（取消复活倒计时，什么都不做）。
		s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "tournament_death_deadline_ignored")
		s.logGameEvent(session, "game-dungeon-character-death-timer-ignored",
			"char_id", runtime.Character.CharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_token", runtime.lifecycleToken,
			"timer_generation", generation,
			"source", "tournament_death_deadline_mechanism_decision")
		return
	}
	if partyDungeonHasPartner(runtime, session.selectedCharacterID) {
		// Keep the authoritative death state, including late coin eligibility.
		// Room movement and the shared settlement retain this participant.
		s.logGameEvent(session, "game-dungeon-party-death-spectator-retained",
			"char_id", session.selectedCharacterID, "instance_id", runtime.partyInstance.id,
			"timer_generation", generation, "waiting_for_shared_settlement", true)
		return
	}
	if err := s.sendCurrentDungeonReturnToTownLocked(
		session,
		runtime,
		cloneCurrentDungeonTownTransition(runtime.deathReturnTransition),
		uint16(dnfenum.CmdPacketDieCharacter),
		"current_exe_op40_death_state_10s_timequeue",
	); err != nil {
		s.logGameEvent(session, "game-dungeon-character-death-return-failed",
			"char_id", runtime.Character.CharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_token", runtime.lifecycleToken,
			"timer_generation", generation,
			"error", err)
	}
}

func (s *Service) cancelCurrentDungeonDeathReturn(session *gameSession, source string) {
	if session == nil {
		return
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	s.cancelCurrentDungeonDeathReturnLocked(session, session.dungeon.runtime, source)
}

// cancelCurrentDungeonDeathReturnLocked invalidates callbacks before removing
// the named queue item. This also defeats a callback that was already dequeued
// but is waiting for the dungeon owner lock.
func (s *Service) cancelCurrentDungeonDeathReturnLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	source string,
) {
	if runtime == nil {
		return
	}
	wasWaiting := runtime.deathReturnWaiting
	timerName := runtime.deathReturnTimerName
	previousGeneration := runtime.deathReturnGeneration
	runtime.deathReturnGeneration = nextCurrentDungeonDeathGeneration(previousGeneration)
	runtime.deathReturnWaiting = false
	runtime.deathReturnTimerName = ""
	runtime.deathReturnDueAt = time.Time{}
	runtime.deathReturnTransition = currentDungeonTownTransition{}
	cancelled := false
	if timerName != "" && s != nil && s.gameplayTimers != nil {
		cancelled = s.gameplayTimers.Cancel(timerName)
	}
	if wasWaiting || timerName != "" {
		s.logGameEvent(session, "game-dungeon-character-death-timer-cancelled",
			"char_id", runtime.Character.CharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"run_token", runtime.lifecycleToken,
			"previous_generation", previousGeneration,
			"current_generation", runtime.deathReturnGeneration,
			"timer_name", timerName,
			"queue_item_cancelled", cancelled,
			"source", source)
	}
}

func nextCurrentDungeonDeathGeneration(current uint64) uint64 {
	next := current + 1
	if next == 0 {
		next++
	}
	return next
}

// ── 操作者缺陷「组队死亡后队长无法复活 / 死亡后下一个房间模型再次出现」的开关 ────────
//
// `keepDungeonDeathReturnAcrossRoomTransition` 报告"这次过图要不要**保留**已经成立的
// 死亡回城倒计时"。现场与因果链见 `dungeon_navigation_runtime.go` 过图提交处那段注释
// （`source=dungeon_room_transition_committed` 把 `deathReturnWaiting` 抹掉 ⇒
// 复活币 op41 被 `reason=death_timer_not_waiting` 拒、回城也不再发生）。
//
// 判据刻意收得很窄，保证**只有"队伍副本里一名角色正处于权威死亡态"这一种情形**会改行为：
//
//	· 档位必须是保留档（`DNFBRIDGE_PARTY_DEATH_RETURN_ROOM_TRANSITION` 非 `legacy`）；
//	· `runtime.deathReturnWaiting` 必须为 true（真的有 op32 死亡在等倒计时）；
//	· `runtime.partyInstance != nil`（**只在组队副本实例内生效**）。
//
// 不满足任何一条 ⇒ 返回 false ⇒ 调用方照旧取消 ⇒ **逐字节等于改动前**
// （单人图/普通过图/没有死亡在等的过图全部不受影响）。
func keepDungeonDeathReturnAcrossRoomTransition(runtime *runtimeDungeonState) bool {
	if runtime == nil || !runtime.deathReturnWaiting || runtime.partyInstance == nil {
		return false
	}
	return partyDeathReturnRoomTransitionModeFromEnv().keeps()
}

// completeCurrentDungeonReviveLocked is the cancellation boundary for a real
// future revive transaction. Callers must first commit the authoritative coin
// consumption and prove the current op41 success flag; a request alone is not
// a revive and must not cancel the death timer.
func (s *Service) completeCurrentDungeonReviveLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	source string,
) []runtimeRaidState {
	if session == nil || runtime == nil || session.dungeon.runtime != runtime {
		return nil
	}
	s.cancelCurrentDungeonDeathReturnLocked(session, runtime, source)
	markPartyDungeonLife(runtime, session.selectedCharacterID, false)
	update, accountingErr := s.recordCurrentRaidCharacterLifeLocked(session, runtime, false, time.Now())
	if accountingErr != nil {
		s.logGameEvent(session, "game-raid-revive-accounting-rejected", "char_id", session.selectedCharacterID, "error", accountingErr)
	}
	runtime.deathStateSent = false
	return update.Projections
}

type currentDungeonReviveCoinConsumeResult struct {
	Consumed      bool
	FreeRevive    bool
	SlotKey       string
	Slot          int16
	ItemID        int64
	CountAfter    int64
	HasItemUpdate bool
	ItemUpdate    currentItemListEntry
}

// handleDungeonUseCoin accepts current-EXE op41 (body = target actor object
// key u16) while the death-return timer is armed. It consumes one revive coin
// stack (item id 1) when available, cancels the death timer, and ACKs with
// class1/op41 success plus the extra success flag byte.
func (s *Service) handleDungeonUseCoin(session *gameSession, body []byte) error {
	if session == nil {
		return nil
	}
	if len(body) != currentDungeonUseCoinBodySize {
		s.logGameEvent(session, "game-dungeon-use-coin-blocked",
			"body_len", len(body),
			"expected_body_len", currentDungeonUseCoinBodySize,
			"reason", "current_exe_normal_op41_single_u16_target_boundary_mismatch")
		return nil
	}
	targetObjectKey := binary.LittleEndian.Uint16(body)

	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	characterID := session.selectedCharacterID
	ownerID, readyReason := currentDungeonDeathRuntimeOwner(runtime, session)
	waiting := runtime != nil && runtime.deathReturnWaiting
	runToken := uint64(0)
	generation := uint64(0)
	dungeonID := int64(0)
	if runtime != nil {
		runToken = runtime.lifecycleToken
		generation = runtime.deathReturnGeneration
		dungeonID = int64(runtime.Dungeon.ID)
	}
	expectedObjectKey := currentSceneActorObjectKey(characterID)
	session.dungeon.mu.Unlock()

	if readyReason != "" || !waiting {
		reason := readyReason
		if reason == "" {
			reason = "death_timer_not_waiting"
		}
		s.logGameEvent(session, "game-dungeon-use-coin-blocked",
			"char_id", characterID,
			"target_object_key", targetObjectKey,
			"expected_object_key", expectedObjectKey,
			"run_token", runToken,
			"death_timer_waiting", waiting,
			"reason", reason)
		return nil
	}
	if targetObjectKey != expectedObjectKey || targetObjectKey != currentSceneActorObjectKey(ownerID) {
		s.logGameEvent(session, "game-dungeon-use-coin-blocked",
			"char_id", characterID,
			"target_object_key", targetObjectKey,
			"expected_object_key", expectedObjectKey,
			"run_token", runToken,
			"reason", "target_object_key_mismatch")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()

	session.dungeon.mu.Lock()
	if session.dungeon.runtime != runtime || runtime == nil || !runtime.deathReturnWaiting ||
		runtime.deathReturnGeneration != generation || runtime.lifecycleToken != runToken {
		session.dungeon.mu.Unlock()
		s.logGameEvent(session, "game-dungeon-use-coin-blocked",
			"char_id", characterID,
			"target_object_key", targetObjectKey,
			"run_token", runToken,
			"timer_generation", generation,
			"reason", "death_timer_state_changed_before_commit")
		return nil
	}
	if _, reason := currentDungeonDeathRuntimeOwner(runtime, session); reason != "" {
		session.dungeon.mu.Unlock()
		s.logGameEvent(session, "game-dungeon-use-coin-blocked",
			"char_id", characterID,
			"target_object_key", targetObjectKey,
			"run_token", runToken,
			"reason", reason)
		return nil
	}

	consume, consumeErr := s.consumeCurrentDungeonReviveCoinWithLukeCaps(ctx, session, runtime, characterID)
	if consumeErr != nil {
		session.dungeon.mu.Unlock()
		s.logGameEvent(session, "game-dungeon-use-coin-blocked",
			"char_id", characterID,
			"target_object_key", targetObjectKey,
			"run_token", runToken,
			"reason", "revive_coin_asset_transaction_failed",
			"error", consumeErr)
		_ = s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketUseCoin), 1)
		return nil
	}
	if !consume.Consumed && !consume.FreeRevive {
		session.dungeon.mu.Unlock()
		s.logGameEvent(session, "game-dungeon-use-coin-blocked",
			"char_id", characterID,
			"target_object_key", targetObjectKey,
			"run_token", runToken,
			"reason", "revive_coin_insufficient")
		_ = s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketUseCoin), 1)
		return nil
	}

	source := "current_exe_op41_revive_coin_consumed"
	if consume.FreeRevive {
		source = "current_exe_op41_private_server_free_revive"
	}
	raidLifeProjections := s.completeCurrentDungeonReviveLocked(session, runtime, source)
	session.dungeon.mu.Unlock()
	for _, state := range raidLifeProjections {
		s.queueCurrentRaidDungeonProgress(state)
	}

	// 厂商顺序：**先** NOTI 0x0020（state=1，"角色已复活"），**再** class-1/op41 ACK
	// （`DungeonCombatHandler.cs:593-608`）。改动前这里什么都不发 —— 币扣了、死亡计时器
	// 也取消了，客户端却永远停在死亡态，现场就是"复活币用不了"。
	// 见 dungeon_revive_notify.go（含 DNFBRIDGE_REVIVE_STATE_NOTIFY 开关）。
	if err := s.sendCurrentDungeonReviveState(session, targetObjectKey); err != nil {
		s.logGameEvent(session, "game-dungeon-revive-state-notify-failed",
			"char_id", characterID,
			"target_object_key", targetObjectKey,
			"run_token", runToken,
			"error", err)
		return err
	}
	if err := s.broadcastPartyDungeonLife(session, runtime, true); err != nil {
		// A peer connection failure must not suppress the committed coin ACK.
		s.logGameEvent(session, "game-dungeon-party-life-sync-failed", "error", err)
	}

	if err := s.sendGameUpperSuccess(
		session,
		uint16(dnfenum.CmdPacketUseCoin),
		[]byte{currentDungeonUseCoinSuccessFlag},
	); err != nil {
		s.logGameEvent(session, "game-dungeon-use-coin-ack-failed",
			"char_id", characterID,
			"target_object_key", targetObjectKey,
			"run_token", runToken,
			"error", err)
		return err
	}

	if consume.HasItemUpdate {
		updateBody := buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, []currentItemListEntry{consume.ItemUpdate})
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), updateBody, 0); err != nil {
			s.logGameEvent(session, "game-dungeon-use-coin-inventory-update-failed",
				"char_id", characterID,
				"slot_key", consume.SlotKey,
				"count_after", consume.CountAfter,
				"error", err)
			return err
		}
	}

	s.logGameEvent(session, "game-dungeon-use-coin-success",
		"char_id", characterID,
		"target_object_key", targetObjectKey,
		"dungeon_id", dungeonID,
		"run_token", runToken,
		"timer_generation", generation,
		"coin_consumed", consume.Consumed,
		"free_revive", consume.FreeRevive,
		"slot_key", consume.SlotKey,
		"item_id", consume.ItemID,
		"count_after", consume.CountAfter,
		"inventory_update_sent", consume.HasItemUpdate,
		"success_flag", currentDungeonUseCoinSuccessFlag,
		"response_sent", true,
		"timer_cancelled", true,
		"revive_state_notified", s.reviveStateNotifyMode().sends(),
		"revive_state_notify_switch", s.reviveStateNotifyMode().String(),
		"source", source)
	return nil
}

func (s *Service) consumeCurrentDungeonReviveCoin(
	ctx context.Context,
	characterID uint16,
) (currentDungeonReviveCoinConsumeResult, error) {
	result := currentDungeonReviveCoinConsumeResult{}
	if characterID == 0 {
		if currentDungeonUseCoinAllowFreeRevive {
			result.FreeRevive = true
			return result, nil
		}
		return result, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterItems == nil {
		if currentDungeonUseCoinAllowFreeRevive {
			result.FreeRevive = true
			return result, nil
		}
		return result, errors.New("character item transaction unavailable")
	}
	characterKey := strconv.FormatUint(uint64(characterID), 10)
	owner, err := dnfdungeon.NewOwner(repositories)
	if err != nil {
		return result, err
	}
	var update currentItemListEntry
	consume, err := owner.ConsumeReviveCoin(ctx, dnfdungeon.ReviveCoinCommand{
		CharacterID: characterKey,
		ItemID:      int64(csharpReviveCoinItemID),
		WalletSlot:  int16(csharpReviveCoinWalletSlot),
		AllowFree:   currentDungeonUseCoinAllowFreeRevive,
		UpdatedAt:   time.Now().UTC(),
		Project: func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			update = currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
			stack.RawEntry = append([]byte(nil), update.data[:]...)
			return stack, nil
		},
	})
	if err != nil {
		return result, err
	}
	result = currentDungeonReviveCoinConsumeResult{
		Consumed:      consume.Consumed,
		FreeRevive:    consume.FreeRevive,
		SlotKey:       consume.SlotKey,
		Slot:          consume.Slot,
		ItemID:        consume.ItemID,
		CountAfter:    consume.CountAfter,
		HasItemUpdate: consume.Consumed,
	}
	if consume.Removed {
		update.patchCore(consume.Slot, math.MaxUint32, 0)
	}
	result.ItemUpdate = update
	return result, nil
}
