package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// currentTownPostTransitionStage is a durable-in-session wire cursor for the
// selected actor generation created by one typed town op24. A failed write may
// resume only at the unfinished suffix; replaying an already accepted mode0 or
// mode1 can leave the current client bound to a different native object.
type currentTownPostTransitionStage byte

const (
	currentTownPostTransitionIdle currentTownPostTransitionStage = iota
	currentTownPostTransitionPending
	currentTownPostTransitionMode0Sent
	currentTownPostTransitionMode1Sent
	currentTownPostTransitionCreatureTableSent
	currentTownPostTransitionCreatureGrowthSent
	currentTownPostTransitionFinishStateSent
	currentTownPostTransitionFinishCompletionSent
	currentTownPostTransitionSkillSent
	currentTownPostTransitionGuildBuffsSent
	currentTownPostTransitionComplete
)

// armCurrentTownPostTransitionPlayerState starts a new selected-actor
// generation after an accepted movement/return typed town op24. First
// selection deliberately does not use this cursor: its type1345 callback owns
// only the already-deferred tail and HUD gauge and cannot safely consume
// another actor/finish-loading generation.
func (s *Service) armCurrentTownPostTransitionPlayerState(session *gameSession, source string) {
	if session == nil || session.selectedCharacterID == 0 {
		return
	}
	session.townPostTransition.mu.Lock()
	defer session.townPostTransition.mu.Unlock()

	session.townPostTransition.generation++
	session.townPostTransition.characterID = session.selectedCharacterID
	session.townPostTransition.ownerChannel = currentTownActorOwnerContext(session)
	session.townPostTransition.stage = currentTownPostTransitionPending
	session.townPostTransition.source = source

	// These gates belong to the actor generation invalidated by op24. The
	// staged chain below owns their one replacement for the new generation.
	session.setFinishLoadingGates(finishLoadingGates{})
	session.initialTownSkillInfoSent = false
	session.initialTownGuardianShieldDeckSent = false
	session.initialTownGuardianShieldAppearanceSent = false

	s.logGameEvent(session, "game-town-post-transition-player-state-armed",
		"source", source,
		"char_id", session.townPostTransition.characterID,
		"owner_context", session.townPostTransition.ownerChannel,
		"generation", session.townPostTransition.generation,
		"stage", session.townPostTransition.stage,
		"sequence", "mode0_mode1_op105_optional_op102_op37_op30_op19_op120")
}

// resetCurrentTownPostTransitionPlayerState drops a stale pending generation
// when character selection/session state is reset before another op24.
func resetCurrentTownPostTransitionPlayerState(session *gameSession) {
	if session == nil {
		return
	}
	session.townPostTransition.mu.Lock()
	session.townPostTransition.characterID = 0
	session.townPostTransition.ownerChannel = 0
	session.townPostTransition.stage = currentTownPostTransitionIdle
	session.townPostTransition.source = ""
	session.townPostTransition.mu.Unlock()
}

// sendCurrentTownPostTransitionPlayerState 是城镇过场后状态链的对外入口：
// 先跑完 op37/op30/op19/op120 那一串（在 townPostTransition.mu 内），**锁释放之后**再补一次
// 同屏投影。锁必须已经释放才做补发（补发会走 onlinePlayers 与仓库读取，不能与
// townPostTransition.mu 形成锁序）。
//
// 为什么必须补：这一串是"城镇场景被 op24/op35 重建"的收尾点，等价于副本的 finish-loading，
// 而它走的是 `sendCurrentFinishLoadingCharacterStateSnapshot`（**直接调用快照版**），
// 绕过了 `scene_finish_loading.go:69` 那条唯一的城镇补发调用 ⇒ 回城之后一次投影都不补，
// 两个人要"来回离开城镇"才重新看见对方。取证与开关见
// town_copresence_post_transition_resend.go 的文件头。
func (s *Service) sendCurrentTownPostTransitionPlayerState(session *gameSession, source string) error {
	err := s.sendCurrentTownPostTransitionPlayerStateLocked(session, source)
	if err != nil {
		return err
	}
	s.resendTownCopresenceAfterTownPostTransition(session, source+"_town_copresence_resend")
	return nil
}

// sendCurrentTownPostTransitionPlayerStateLocked rebuilds the exact current-client
// town generation invalidated by op24. The transport-only DLL does not rewrite
// scene owners, so both actor packets use the CHANNELINFO/connection owner.
func (s *Service) sendCurrentTownPostTransitionPlayerStateLocked(session *gameSession, source string) error {
	if session == nil {
		return nil
	}
	session.townPostTransition.mu.Lock()
	defer session.townPostTransition.mu.Unlock()

	stage := session.townPostTransition.stage
	if stage == currentTownPostTransitionIdle || stage == currentTownPostTransitionComplete {
		return nil
	}
	characterID := session.townPostTransition.characterID
	ownerChannel := session.townPostTransition.ownerChannel
	currentOwner := currentTownActorOwnerContext(session)
	connectionOwner := currentConnectionTownActorOwnerContext(session)
	if characterID == 0 || characterID != session.selectedCharacterID {
		return fmt.Errorf(
			"town post-transition character changed before stage %d: armed=%d selected=%d",
			stage,
			characterID,
			session.selectedCharacterID,
		)
	}
	if ownerChannel != currentOwner || ownerChannel != connectionOwner {
		return fmt.Errorf(
			"town post-transition owner changed before stage %d: armed=%d current=%d connection=%d",
			stage,
			ownerChannel,
			currentOwner,
			connectionOwner,
		)
	}
	if source == "" {
		source = session.townPostTransition.source
	}
	generation := session.townPostTransition.generation

	if session.townPostTransition.stage < currentTownPostTransitionMode0Sent {
		if err := s.sendSelectedActorMode0AppearanceRefresh(
			session,
			"town_post_transition",
			source+"_mode0_native_owner",
		); err != nil {
			return err
		}
		session.townPostTransition.stage = currentTownPostTransitionMode0Sent
	}
	if session.townPostTransition.stage < currentTownPostTransitionMode1Sent {
		if err := s.sendSelectedActorMode1AppearanceRefresh(
			session,
			"town_post_transition",
			source+"_mode1_native_owner",
		); err != nil {
			return err
		}
		session.townPostTransition.stage = currentTownPostTransitionMode1Sent
	}
	if session.townPostTransition.stage < currentTownPostTransitionCreatureTableSent {
		// op24 created a new native actor generation, so op105 must be written
		// even when the previous generation already consumed the same table.
		session.selectedCreatureStateTableSent = false
		if err := s.sendSelectedCreatureStateTable(session, source+"_op105_after_actor_rebind"); err != nil {
			return err
		}
		session.townPostTransition.stage = currentTownPostTransitionCreatureTableSent
	}
	if session.townPostTransition.stage < currentTownPostTransitionCreatureGrowthSent {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		equippedCreature, err := s.currentEquippedCreatureForCharacterWithError(
			ctx,
			strconv.Itoa(int(characterID)),
		)
		cancel()
		if err != nil {
			return err
		}
		if equippedCreature.valid() {
			if err := s.sendSelectedEquippedCreatureGrowthState(
				session,
				equippedCreature,
				source+"_op102_after_op105",
			); err != nil {
				return err
			}
		}
		session.townPostTransition.stage = currentTownPostTransitionCreatureGrowthSent
	}
	if session.townPostTransition.stage < currentTownPostTransitionFinishStateSent {
		if err := s.sendCurrentFinishLoadingCharacterStateSnapshot(
			session,
			source+"_op37_after_actor_and_creature_rebind",
		); err != nil {
			return err
		}
		session.setFinishLoadingGateState(true)
		session.townPostTransition.stage = currentTownPostTransitionFinishStateSent
	}
	if session.townPostTransition.stage < currentTownPostTransitionFinishCompletionSent {
		if err := s.sendCurrentIncreaseStatusResult(
			session,
			source+"_op30_after_finish_state",
		); err != nil {
			return err
		}
		session.setFinishLoadingGateCompletion(true)
		session.townPostTransition.stage = currentTownPostTransitionFinishCompletionSent
	}

	if _, err := s.sendSelectedGuardianShieldDeckInfo(session, source+"_guardian_deck"); err != nil {
		return err
	}
	if _, err := s.sendSelectedGuardianShieldTownAppearanceRefreshOnce(session, source+"_guardian_appearance"); err != nil {
		return err
	}
	if session.townPostTransition.stage < currentTownPostTransitionSkillSent {
		prepared := session.initialTownSkillInfoPrepared &&
			session.initialTownSkillInfo.characterID == strconv.Itoa(int(characterID)) &&
			len(session.initialTownSkillInfo.body) != 0
		if prepared {
			if err := s.sendCurrentSceneSkillInfoProjection(
				session,
				session.initialTownSkillInfo,
				source+"_op19_prepared_after_op30",
			); err != nil {
				return err
			}
			// 这条 prepared 分支绕过了 sendSelectedActorCurrentSceneSkillInfo，
			// 所以「Buff 强化」换装栏的重投影必须在这里单独补一次
			// （档位与四条边界清单见 skill_switch_inventory.go）。
			if err := s.sendSkillSwitchInventoryProjection(
				session,
				source+"_op19_prepared_after_op30_skill_switch_inventory",
			); err != nil {
				return err
			}
			if err := s.sendBuffSwapReapply(session, source+"_after_prepared_op19"); err != nil {
				return err
			}
		} else {
			if err := s.sendSelectedActorCurrentSceneSkillInfo(
				session,
				source+"_op19_reloaded_after_op30",
			); err != nil {
				return err
			}
		}
		session.initialTownSkillInfoPrepared = false
		session.initialTownSkillInfoSent = true
		session.initialTownSkillInfo = currentSceneSkillInfoProjection{}
		session.townPostTransition.stage = currentTownPostTransitionSkillSent
	}
	if session.townPostTransition.stage < currentTownPostTransitionGuildBuffsSent {
		if err := s.restoreCurrentGuildBuffProjection(session); err != nil {
			return err
		}
		session.townPostTransition.stage = currentTownPostTransitionGuildBuffsSent
	}
	if session.townPostTransition.stage < currentTownPostTransitionComplete {
		placementBody := buildCurrentSceneActorPlacementBody()
		if err := s.sendGameUpperRawClass(
			session,
			uint16(dnfenum.CmdPacketRequestBlacklist),
			placementBody,
			0,
		); err != nil {
			return err
		}
		session.setFinishLoadingGatePostFinish(true)
		session.townPostTransition.stage = currentTownPostTransitionComplete
	}
	s.logGameEvent(session, "game-town-post-transition-player-state-sent",
		"source", source,
		"char_id", characterID,
		"owner_context", ownerChannel,
		"generation", generation,
		"stage", session.townPostTransition.stage,
		"sequence", "mode0_mode1_op105_optional_op102_op37_op30_op19_op120")
	return nil
}
