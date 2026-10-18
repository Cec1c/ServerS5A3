package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Friendly PVF APCs can follow the player into later rooms while retaining
// the object key announced by their original room.
func dungeonRuntimeContainsDeathOwnerObjectKey(runtime *runtimeDungeonState, objectKey uint32) bool {
	if runtime == nil || runtime.Room == nil || objectKey == 0 {
		return false
	}
	if runtime.Room.ContainsAnnouncedActorObjectKey(objectKey) {
		return true
	}
	for _, visit := range runtime.Rooms {
		if visit == nil || visit.Room == nil || visit.Room == runtime.Room {
			continue
		}
		if currentDungeonRoomContainsFriendlyOwner(visit.Room, visit.Scene, objectKey, true) {
			return true
		}
	}
	return false
}

// currentDungeonRoomAnnouncedLiveHostile 的完整说明与开关见 dungeon_death_room_proof.go。
func (s *Service) handleDungeonMonsterDeath(session *gameSession, body []byte) error {
	return s.handleDungeonMonsterDeathRequest(session, body, nil)
}

func (s *Service) handleDungeonMonsterDeathRequest(
	session *gameSession,
	body []byte,
	sequence *uint64,
) (resultErr error) {
	var huntProgressErr error
	defer func() {
		if resultErr == nil {
			resultErr = huntProgressErr
		}
	}()
	request, err := s.decodeCurrentDungeonMonsterDeath(body)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-monster-death-blocked",
			"body_len", len(body),
			"reason", "current_exe_op39_request_malformed",
			"error", err)
		return nil
	}
	if len(request.OpaqueTail) != 0 {
		s.logGameEvent(session, "game-dungeon-monster-death-blocked",
			"object_key", request.RuntimeObjectKey,
			"body_len", len(body),
			"tail_len", len(request.OpaqueTail),
			"reason", "current_exe_op39_request_tail_unproven")
		return nil
	}
	if session == nil {
		return nil
	}
	var raidBarrierProjections []runtimeRaidState
	defer func() {
		for _, state := range raidBarrierProjections {
			s.queueCurrentRaidDungeonProgress(state)
		}
	}()
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil || runtime.Room == nil {
		s.logGameEvent(session, "game-dungeon-monster-death-blocked",
			"object_key", request.RuntimeObjectKey,
			"reason", "dungeon_runtime_missing")
		return nil
	}
	scene, hasScene := runtime.Session.Scene()
	// 类别3任务物品先按报告事件结算，再保留已注册map-owned对象的地面行释放。
	// 两条路径都不发送普通怪物死亡确认；其余报告继续既有怪物/APC处理。
	if hasScene {
		mapOwnedEnabled := dungeonMapOwnedPassiveObjectModeFromEnv().registers()
		var questDropHandled bool
		var questDropErr error
		if sequence != nil {
			questDropHandled, questDropErr = s.handleCurrentDungeonQuestPassiveDropDeathLocked(
				session,
				runtime,
				scene,
				request,
				body,
				*sequence,
			)
		}
		var mapOwnedHandled bool
		var mapOwnedErr error
		if mapOwnedEnabled {
			mapOwnedHandled, mapOwnedErr = s.handleCurrentDungeonMapOwnedPassiveObjectDeathLocked(
				session,
				runtime,
				scene,
				request,
			)
		}
		if questDropErr != nil || mapOwnedErr != nil {
			return errors.Join(questDropErr, mapOwnedErr)
		}
		if questDropHandled || mapOwnedHandled {
			return nil
		}
	}
	if hasScene && runtime.Tournament != nil && runtime.Tournament.Definition != nil {
		room := runtime.Room.Snapshot()
		if room.MapID == runtime.Tournament.Definition.MapID &&
			tournamentActorIsAnnounced(room, request.RuntimeObjectKey) &&
			!runtime.Tournament.canAcceptActorDeath(int(request.RuntimeObjectKey)) {
			s.logGameEvent(session, "game-dungeon-tournament-actor-death-rejected",
				"dungeon_id", runtime.Dungeon.ID,
				"map_id", room.MapID,
				"object_key", request.RuntimeObjectKey,
				"current_round", runtime.Tournament.currentRoundNumber(),
				"terminal", runtime.Tournament.snapshot().Terminal,
				"reason", "actor_is_not_in_the_current_tournament_round")
			return nil
		}
	}
	// `DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF=room_proven` 时追加的服务端自证判据：
	// 这条 op39 的目标是不是本房间在册、仍在场的敌对 actor。默认档（strict）下恒为
	// false；已验证的友好 APC 贡献兼容路径另作窄范围判断。见 dungeon_death_room_proof.go。
	roomProvenHostile := false
	if hasScene && s.dungeonDeathRoomProofActive() {
		roomProvenHostile = currentDungeonRoomAnnouncedLiveHostile(runtime, scene, request.RuntimeObjectKey)
	}
	ownerIsSentinel := request.OwnerObjectKey == ^uint16(0)
	friendlyAPCDeath := hasScene && s.currentDungeonFriendlyAPCDeath(runtime, scene, request)
	friendlyOwnerDeath := hasScene && s.currentDungeonFriendlyOwnerDeath(runtime, scene, request)
	if friendlyAPCDeath || friendlyOwnerDeath {
		if _, defeated := runtime.Room.DefeatedDeathTarget(request.RuntimeObjectKey); defeated {
			// Repair a duplicate or failed notification without settling the kill
			// again. The proof above still requires this room's ordinary target.
			return s.replayCurrentDungeonMonsterDeathLocked(session, runtime, request.RuntimeObjectKey)
		}
	}
	var scriptedTutorialMonster runtimeDungeonMonster
	var scriptedTutorialEvidence dungeonTutorialScriptEvidence
	scriptedTutorialDeath := false
	ownerSentinelOrdinaryTarget := false
	ownerSentinelBlockingClearTarget := false
	ownerSentinelBlockingClearSource := ""
	var storyProcEvidence dungeonStoryProcDeathEvidence
	storyProcDeath := false
	if ownerIsSentinel {
		if (request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat || request.CombatEntryCount != 0) && !friendlyAPCDeath {
			if !roomProvenHostile {
				s.logGameEvent(session, "game-dungeon-monster-death-blocked",
					"dungeon_id", runtime.Dungeon.ID,
					"maze_index", runtime.MazeIndex,
					"object_key", request.RuntimeObjectKey,
					"related_actor_object_key", request.OwnerObjectKey,
					"request_layout", request.Layout,
					"combat_entry_count", request.CombatEntryCount,
					"reason", "non_hostile_ai_request_shape_unproven")
				return nil
			}
			// 形状不可证，但目标本身是本房间在册的活怪：按普通怪死亡提交，
			// 不走非敌对 actor 退役路径。
			ownerSentinelOrdinaryTarget = true
			s.logGameEvent(session, "game-dungeon-monster-death-shape-relaxed",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"object_key", request.RuntimeObjectKey,
				"related_actor_object_key", request.OwnerObjectKey,
				"request_layout", request.Layout,
				"combat_entry_count", request.CombatEntryCount,
				"room_proven_hostile", true,
				"reason", "owner_sentinel_request_shape_unproven_but_target_is_announced_live_room_hostile",
				"source", "server_owned_announced_actor_table")
		}
		if hasScene {
			scriptedTutorialMonster, scriptedTutorialEvidence, scriptedTutorialDeath = s.isPVFTutorialScriptedMonsterDeath(
				runtime,
				scene,
				request.RuntimeObjectKey,
			)
			if !scriptedTutorialDeath {
				_, ownerSentinelBlockingClearSource, ownerSentinelBlockingClearTarget =
					currentDungeonOwnerSentinelBlockingClearTarget(runtime, scene, request.RuntimeObjectKey)
				if !ownerSentinelBlockingClearTarget {
					_, ownerSentinelOrdinaryTarget = currentDungeonOwnerSentinelAnnouncedOrdinaryMonster(
						runtime,
						scene,
						request.RuntimeObjectKey,
					)
				}
			}
		}
		if hasScene && !scriptedTutorialDeath && !ownerSentinelBlockingClearTarget && !ownerSentinelOrdinaryTarget {
			storyProcEvidence, storyProcDeath = s.currentDungeonStoryProcDeath(session, runtime, scene, request)
			if storyProcDeath {
				if _, defeated := runtime.Room.DefeatedDeathTarget(request.RuntimeObjectKey); defeated {
					return s.replayCurrentDungeonStoryProcDeathLocked(session, runtime, request.RuntimeObjectKey)
				}
			}
		}
	}
	if ownerIsSentinel && !scriptedTutorialDeath && !ownerSentinelBlockingClearTarget && !ownerSentinelOrdinaryTarget && !storyProcDeath && !friendlyAPCDeath {
		target, retirementErr := runtime.Room.RetireNonHostileAIActor(request.RuntimeObjectKey)
		if retirementErr != nil {
			if hasScene {
				handled, clearErr := s.handleCurrentDungeonDestroyObjectClearConditionDeathLocked(
					session,
					runtime,
					scene,
					request.RuntimeObjectKey,
				)
				if clearErr != nil {
					return clearErr
				}
				if handled {
					s.cancelCurrentDungeonReturnAfterDungeonEvidenceLocked(
						session,
						runtime,
						"accepted_destroy_object_clear_condition_op39_after_pending_op24",
					)
					return nil
				}
				handled, passiveErr := s.handleCurrentDungeonQuestPassiveObjectDeathLocked(
					session,
					runtime,
					scene,
					request.RuntimeObjectKey,
				)
				if passiveErr != nil {
					s.logGameEvent(session, "game-dungeon-quest-passive-object-death-deferred",
						"dungeon_id", runtime.Dungeon.ID,
						"maze_index", runtime.MazeIndex,
						"object_key", request.RuntimeObjectKey,
						"related_actor_object_key", request.OwnerObjectKey,
						"reason", "unique_active_pvf_passive_target_owner_failed",
						"error", passiveErr)
					return nil
				}
				if handled {
					s.cancelCurrentDungeonReturnAfterDungeonEvidenceLocked(
						session,
						runtime,
						"accepted_quest_passive_object_op39_after_pending_op24",
					)
					return nil
				}
			}
			if cachedBody, ok := runtime.Room.DeathResponseBody(request.RuntimeObjectKey); ok {
				// 队伍级共享实例：这只非敌对 actor 已经被权威退役过（队友先提交），
				// 请求方客户端那边还有它 ⇒ 幂等重放同一份退役通知，别把怪/actor 留在半死状态。
				return s.replayPartyDungeonAuthoritativeDeathWithBodyLocked(
					session,
					runtime,
					request.RuntimeObjectKey,
					cachedBody,
					"duplicate_op39_for_shared_instance_non_hostile_retirement",
				)
			}
			s.logGameEvent(session, "game-dungeon-monster-death-blocked",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"object_key", request.RuntimeObjectKey,
				"related_actor_object_key", request.OwnerObjectKey,
				"request_layout", request.Layout,
				"reason", "non_hostile_ai_retirement_rejected",
				"error", retirementErr)
			return nil
		}
		responseBody, err := buildCurrentDungeonDeathNotificationBody(
			target.ObjectKey,
			target.ResponseKind,
		)
		if err != nil {
			return err
		}
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyDieMonster), responseBody, 0); err != nil {
			return err
		}
		// 权威退役状态：记下来（供重复 op39 幂等重放）+ 广播给同实例队友。
		runtime.Room.RecordDeathResponseBody(target.ObjectKey, responseBody)
		s.broadcastPartyDungeonInstanceDeathNotification(
			session,
			runtime,
			target.ObjectKey,
			responseBody,
			"op39_authoritative_non_hostile_ai_retirement",
		)
		scene, _ = runtime.Session.Scene()
		s.logGameEvent(session, "game-dungeon-non-hostile-ai-retired",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_key", request.RuntimeObjectKey,
			"related_actor_object_key", request.OwnerObjectKey,
			"request_layout", request.Layout,
			"combat_entry_count", request.CombatEntryCount,
			"response_kind", target.ResponseKind,
			"response_opcode", uint16(dnfenum.CmdPacketNotifyDieMonster),
			"response_body_len", len(responseBody),
			"room_cleared", scene.Cleared,
			"boss_room", scene.Boss,
			"reward_assets", "unchanged",
			"response", "current_exe_op38_non_hostile_ai_retirement_ack")
		s.cancelCurrentDungeonReturnAfterDungeonEvidenceLocked(
			session,
			runtime,
			"accepted_non_hostile_ai_op39_after_pending_op24",
		)
		return nil
	}
	playerObjectKey := currentSceneActorObjectKey(session.selectedCharacterID)
	// 缺陷 B（操作者：「双方打同一个怪物有概率有一人的伤害为 1」）：这一条闸门是服务端唯一
	// 能把"两个攻击者里恰好一人的命中"判无效的地方，而 op39 在**两打一**形状下必然把
	// related actor 写成队友（现场 `related_actor_object_key=6 player_object_key=3`）。
	// 默认档（`party`）下同队同实例的队友攻击者按普通怪死亡结算，其余形状照旧拒收。
	// 开关、取证与"退化成最小伤害 1"的判定见 dungeon_party_hit_owner.go。
	hitOwnerMode := s.dungeonPartyHitOwnerGate()
	hitOwner := s.resolveCurrentDungeonPartyHitOwner(session, runtime, request.OwnerObjectKey, hitOwnerMode)
	if !scriptedTutorialDeath && !ownerSentinelBlockingClearTarget && !ownerSentinelOrdinaryTarget && !storyProcDeath &&
		request.OwnerObjectKey != playerObjectKey &&
		!roomProvenHostile &&
		!friendlyAPCDeath &&
		!friendlyOwnerDeath &&
		!dungeonRuntimeContainsDeathOwnerObjectKey(runtime, uint32(request.OwnerObjectKey)) &&
		!hitOwner.accepted {
		if hitOwnerMode == dungeonPartyHitOwnerParty {
			// 判定会退化成最小伤害 1：把"谁打谁、为什么被判无效"一次写清，下一次实测单看
			// 这一行就能判出缺陷 B 是否还在。
			verdict := dungeonPartyHitVerdictForAccepted(false)
			s.logGameEvent(session, "game-dungeon-party-hit-clamped-to-one",
				"attacker_char_id", hitOwner.partyPeerCharID,
				"attacker_is_party_member", hitOwner.partyMember,
				"attacker_in_same_dungeon_instance", hitOwner.sameInstance,
				"reporter_char_id", session.selectedCharacterID,
				"target_object_key", request.RuntimeObjectKey,
				"related_actor_object_key", request.OwnerObjectKey,
				"player_object_key", playerObjectKey,
				"party_id", hitOwner.partyID,
				"instance_id", hitOwner.instanceID,
				"request_layout", request.Layout,
				"damage_evidence_combat_entries", request.CombatEntryCount,
				"damage_floor", currentDungeonPartyHitDamageFloorLabel(verdict),
				"clamped_to_one", true,
				"switch", dungeonPartyHitOwnerEnv,
				"mode", hitOwnerMode.String(),
				"reason", hitOwner.reason)
		}
		s.logGameEvent(session, "game-dungeon-monster-death-blocked",
			"object_key", request.RuntimeObjectKey,
			"related_actor_object_key", request.OwnerObjectKey,
			"player_object_key", playerObjectKey,
			"request_layout", request.Layout,
			"reason", "room_related_actor_object_key_mismatch",
			"error", errDungeonMonsterOwnerMismatch)
		return nil
	}
	if hitOwner.accepted {
		// 命中被接受：客户端按自己的真实伤害数值结算（服务端不计算伤害，这里只把
		// "这次 op39 是两打一的正常形状"钉成一条可判读事件，并给出 damage_floor=real）。
		verdict := dungeonPartyHitVerdictForAccepted(true)
		s.logGameEvent(session, "game-dungeon-party-hit-accepted",
			"attacker_char_id", hitOwner.partyPeerCharID,
			"reporter_char_id", session.selectedCharacterID,
			"target_object_key", request.RuntimeObjectKey,
			"related_actor_object_key", request.OwnerObjectKey,
			"player_object_key", playerObjectKey,
			"party_id", hitOwner.partyID,
			"instance_id", hitOwner.instanceID,
			"same_party_dungeon_instance", true,
			"request_layout", request.Layout,
			"damage_evidence_combat_entries", request.CombatEntryCount,
			"damage_floor", currentDungeonPartyHitDamageFloorLabel(verdict),
			"clamped_to_one", false,
			"gate", "party_peer_attacker_same_instance",
			"switch", dungeonPartyHitOwnerEnv,
			"mode", hitOwnerMode.String(),
			"reason", hitOwner.reason)
	}
	if roomProvenHostile && !ownerSentinelBlockingClearTarget && !ownerSentinelOrdinaryTarget &&
		request.OwnerObjectKey != playerObjectKey {
		// 客户端声称的击杀者既不是本地角色、也不是本房间在册 actor，但**目标自己**
		// 是本房间在册的活怪。服务端在册表优先，避免客户端留下白色残影。
		s.logGameEvent(session, "game-dungeon-monster-death-owner-relaxed",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_key", request.RuntimeObjectKey,
			"related_actor_object_key", request.OwnerObjectKey,
			"player_object_key", playerObjectKey,
			"request_layout", request.Layout,
			"room_proven_hostile", true,
			"reason", "claimed_killer_owner_unproven_but_target_is_announced_live_room_hostile",
			"source", "server_owned_announced_actor_table")
	}
	if target, matchedSpecialMonster, retirementErr := runtime.Room.RetireSpecialMonsterActor(request.RuntimeObjectKey); matchedSpecialMonster {
		if retirementErr != nil {
			if errors.Is(retirementErr, errDungeonMonsterAlreadyDefeated) {
				return s.replayCurrentDungeonMonsterDeathLocked(session, runtime, request.RuntimeObjectKey)
			}
			s.logGameEvent(session, "game-dungeon-monster-death-blocked",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"object_key", request.RuntimeObjectKey,
				"related_actor_object_key", request.OwnerObjectKey,
				"request_layout", request.Layout,
				"combat_entry_count", request.CombatEntryCount,
				"reason", "special_monster_retirement_rejected",
				"error", retirementErr)
			return nil
		}
		if err := s.completeCurrentSuspiciousVillageElevatorLocked(session, runtime, target.ObjectKey); err != nil {
			return err
		}
		// 深渊波次怪的奖励投骰（操作者清单 batch item ②）：
		// 厂商 `DungeonKillApplicationService.cs:908-921` 对
		// `IsHellPartyActor && IsHellPartyRoom` 的死亡走
		// `GenerateAbyssPartyAndRegister`，票数来自 `etc/hellparty.etc` 的
		// `RewardRollCount`（A 档 8 次）。本仓波次怪走的正是这条
		// `RetireSpecialMonsterActor` 路径，所以奖励必须挂在这里 ——
		// 下面这条 op38 就是厂商 `SendMonsterDieAsync` 携带掉落的那一条。
		// 开关默认 vendor；`legacy` 档一个随机数都不抽、一条掉落都不注册。
		// 见 dungeon_hell_party_reward.go。
		var hellPartyRewardWaves []currentDungeonDeathDropWire
		hellPartyRewardMode := hellPartyRewardRollModeFromEnv()
		if hellPartyRewardMode.rolls() {
			if groupID, isRewardWave := dungeonHellPartyRewardWaves(runtime, scene, target.ObjectKey); isRewardWave {
				rewardRequest, requestErr := s.resolveHellPartyReward(runtime, scene, groupID,
					dungeonHellPartyRewardMonsterCode(runtime.Room, target.ObjectKey))
				if requestErr != nil {
					s.logHellPartyRewardSkipped(session, "game-dungeon-hell-party-reward-deferred",
						runtime, scene, target.ObjectKey,
						"group_id", groupID,
						"monster_code", 0,
						"reason", "reward_drop_rule_unavailable",
						"error", requestErr)
				} else {
					for _, recipient := range partyDungeonDropRecipients(runtime, session.selectedCharacterID, playerObjectKey) {
						granted := 0
						if visit, visitErr := runtime.currentDungeonRoomVisit(scene); visitErr == nil && visit != nil {
							rewardRequest.rng = newCurrentDungeonDropLCG(visit.DropRNG)
						}
						var rewardResult hellPartyRewardResult
						var rewardErr error
						if s.options.currentClientVersion == "official-24.9" {
							if currentNormalAbyssEpicClearReady(runtime, groupID) {
								rewardResult, rewardErr = currentNormalAbyssEpicClearReward(rewardRequest)
							}
						} else {
							rewardResult, rewardErr = hellPartyRewardRolls(rewardRequest)
						}
						switch {
						case rewardErr != nil:
							s.logHellPartyRewardSkipped(session, "game-dungeon-hell-party-reward-deferred",
								runtime, scene, target.ObjectKey,
								"group_id", groupID,
								"monster_code", rewardRequest.MonsterCode,
								"reward_roll_count", rewardRequest.RewardRollCount,
								"reason", "reward_roll_failed",
								"error", rewardErr)
						default:
							if visit, visitErr := runtime.currentDungeonRoomVisit(scene); visitErr == nil && visit != nil {
								visit.DropRNG = rewardRequest.rng.Seed()
							}
							if len(rewardResult.Selected) != 0 {
								wires, registerErr := s.registerHellPartyRewardDrops(
									runtime,
									scene,
									rewardRequest,
									rewardResult.Selected,
									recipient.ActorKey,
									recipient.CharacterID,
								)
								if registerErr != nil {
									s.logHellPartyRewardSkipped(session, "game-dungeon-hell-party-reward-deferred",
										runtime, scene, target.ObjectKey,
										"group_id", groupID,
										"monster_code", rewardRequest.MonsterCode,
										"reward_roll_count", rewardRequest.RewardRollCount,
										"reason", "reward_drop_registration_failed",
										"error", registerErr)
								} else {
									hellPartyRewardWaves = append(hellPartyRewardWaves, wires...)
									granted = len(wires)
								}
							}
							s.logHellPartyRewardRoll(session, runtime, scene, target.ObjectKey,
								rewardRequest, rewardResult, granted,
								"vendor_DungeonKillApplicationService_cs_908_921_GenerateAbyssPartyAndRegister")
							s.logGameEvent(session, "game-dungeon-party-member-hell-reward-rolled", "recipient_char_id", recipient.CharacterID, "selected_items", hellPartyRewardItemList(rewardResult.Selected), "monster_object_key", target.ObjectKey)
						}
					}
				}
			}
		}
		// 深渊**必掉**史诗碎片（操作者要求，见 dungeon_hell_party_epic_fragment.go）：
		// 触发点 = 击杀**最后一波**的组内最后一只（与厂商 `DropService.cs:179-199`
		// 同一个门）。碎片身份取自既有的 `etc/epicpieceinfo.etc [piece list]` 目录，
		// **不消费概率那一支的任何输入**（不读 [drop prob]、不读装备池、
		// 不看 Probability、不依赖投骰成功与否）⇒ 两支互不干扰、可叠加。
		// 掉落行**追加在概率掉落之后**（与厂商同序），走同一个
		// `registerHellPartyRewardDrops` 账本与同一条 op38。
		// 开关默认 guaranteed；`off`/`legacy` 档完全静默、一个随机数都不抽。
		// ⚠ 它从属于 `DNFBRIDGE_HELL_PARTY_REWARD_ROLL`：那一支为 legacy 时记一条
		//    `-skipped reason=reward_roll_switch_legacy` 后什么都不做。
		abyssEpicFragmentWaves := s.grantAbyssEpicFragmentGuarantee(
			session, runtime, scene, target.ObjectKey, hellPartyRewardMode,
		)
		hellPartyRewardWaves = append(hellPartyRewardWaves, abyssEpicFragmentWaves...)
		// 独立掉落档（`DNFBRIDGE_ABYSS_PARTY_DROP_OWNER=independent`，默认）：这次的掉落
		// 是**按成员各注册一份**的，击杀方自己那一帧只带**属于自己的行**
		// （与厂商 `DungeonKillApplicationService.cs:1399-1406` "每人只发自己那一份"一致）；
		// 队友那一份由下面的投影按各自所有者取。`shared`/`legacy` 档逐字节保持今天的行为。
		ownWaves := hellPartyRewardWaves
		authoritativeBody, err := buildCurrentDungeonDeathNotificationBodyWithDrops(target.ObjectKey, target.ResponseKind, hellPartyRewardWaves)
		if err != nil {
			return err
		}
		runtime.Room.RecordDeathResponseBody(target.ObjectKey, authoritativeBody)
		if abyssPartyDropOwnerModeFromEnv().perMember() {
			ownWaves = dropWiresOwnedBy(hellPartyRewardWaves, currentSceneActorObjectKey(session.selectedCharacterID))
		}
		responseBody, err := buildCurrentDungeonDeathNotificationBodyWithDrops(
			target.ObjectKey,
			target.ResponseKind,
			ownWaves,
		)
		if err != nil {
			return err
		}
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyDieMonster), responseBody, 0); err != nil {
			return err
		}
		// 队伍级共享实例：房图怪的权威死亡有投影（`broadcastPartyDungeonInstanceDeathNotification`），
		// 波次怪（特殊怪退役）这一条过去只发请求方 ⇒ 队友屏幕上留一只服务端已经判死的白怪
		// （缺陷「组队挑战深渊：打死柱子 boss 后卡白怪，没有出现 NPC」，见
		// `dungeon_hell_party_wave_peer_notify.go` 文件头的现场行）。单人/无实例为 0 字节副作用。
		//
		// 投影体默认与击杀方**逐字节同一份**（含概率掉落 + 保底碎片的地面掉落行），
		// 两名成员看到同一批地面对象；`DNFBRIDGE_HELL_PARTY_WAVE_PEER_DROPS=stripped`
		// 逐字节回退到上一轮那版（7 字节、无掉落行）。
		s.notifyCurrentHellPartyWaveRetirementToPartyPeersLocked(
			session,
			runtime,
			target.ObjectKey,
			target.ResponseKind,
			hellPartyRewardWaves,
		)
		// 深渊柱子（map-owned `[special passive object]`）的破坏客户端从不上报，
		// 队长那一侧是本地打碎的；这里在**本房间第一只波次怪退役**时把同一状态投影给队友，
		// 否则队员那一侧只看到"怪物出现了、柱子没碎"（见同一文件头）。
		s.projectCurrentHellPartyPillarDestructionToPartyPeersLocked(
			session,
			runtime,
			scene,
			target.ResponseKind,
		)
		if err := runtime.cacheCurrentDungeonRoom(); err != nil {
			return fmt.Errorf("cache dungeon room after special monster retirement: %w", err)
		}
		if err := s.persistCurrentDungeonRoomClearQuestLocked(session, runtime); err != nil {
			return err
		}
		scene, _ = runtime.Session.Scene()
		s.awardCurrentPetRoomClearLocked(session, runtime, scene, "authoritative_special_monster_retirement")
		s.logGameEvent(session, "game-dungeon-special-monster-retired",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"object_key", request.RuntimeObjectKey,
			"related_actor_object_key", request.OwnerObjectKey,
			"request_layout", request.Layout,
			"combat_entry_count", request.CombatEntryCount,
			"response_kind", target.ResponseKind,
			"response_opcode", uint16(dnfenum.CmdPacketNotifyDieMonster),
			"response_body_len", len(responseBody),
			"hell_party_reward_drops", len(hellPartyRewardWaves),
			"abyss_epic_fragment_drops", len(abyssEpicFragmentWaves),
			"room_cleared", scene.Cleared,
			"boss_room", scene.Boss,
			"reward_assets", "unchanged",
			"response", "current_exe_op38_special_monster_retirement_ack")
		s.cancelCurrentDungeonReturnAfterDungeonEvidenceLocked(
			session,
			runtime,
			"accepted_special_monster_op39_after_pending_op24",
		)
		if err := s.persistCurrentHellRegionQuestCompletionLocked(session, runtime, scene); err != nil {
			return err
		}
		// 深渊局通关判定（默认档 = **只在迷宫 Boss 房**成立，与普通最终房间同一个判据
		// `currentDungeonOrdinaryFinalRoomReady`）。放在**奖励之后**：掉落的任何成败都只记
		// 日志，绝不能挡住这一条；`DNFBRIDGE_HELL_PARTY_CLEAR_SETTLEMENT=legacy` 逐字节
		// 回退到上一轮"深渊房间清空即结算"（提前结算）行为，`off` 回到更早的不结算行为。
		// 见 dungeon_hell_party_clear_settlement.go。
		if err := s.completeCurrentDungeonHellPartyFinalRoomAfterDeathLocked(
			session,
			runtime,
			scene,
			target.ObjectKey,
			"hell_party_wave_monster_op39_retirement",
		); err != nil {
			return err
		}
		return nil
	}
	dropMonster, ordinaryDropCandidate := runtime.Room.AnnouncedMonster(request.RuntimeObjectKey)
	bossActorDeath, bossActorType, bossActorSource := currentDungeonRuntimeObjectBossActor(
		runtime,
		request.RuntimeObjectKey,
		runtimeDungeonMonsterAnnounced,
	)
	clearConditionActorDeath := false
	clearConditionSource := ""
	if ordinaryDropCandidate {
		clearConditionSource = currentDungeonClearConditionSource(runtime, dropMonster)
		clearConditionActorDeath = clearConditionSource != ""
		if clearConditionActorDeath && !bossActorDeath {
			bossActorDeath = true
			bossActorType = 3
			bossActorSource = clearConditionSource
		}
	}
	target, cleared, reportErr := runtime.Room.CommitActorDeathReport(request.RuntimeObjectKey, runtime.Session)
	if errors.Is(reportErr, errDungeonMonsterAlreadyDefeated) {
		if err := s.persistCurrentDungeonHuntMonsterDeathLocked(session, runtime, request.RuntimeObjectKey); err != nil {
			return err
		}
	}
	if reportErr != nil {
		// 阶段二（队伍级共享实例）：同一只怪被两个成员各自判定死亡时，后到的那次请求
		// 落在**共享房间**里必然是 already_defeated。此时不能只记 blocked：
		// 请求方的客户端以为怪还活着（它会把它打成白色残影），必须把既有的权威死亡状态
		// 幂等重放给他（同一份 op38 形状），并把他自己的副本场景补上清怪状态。
		// 单人/无队伍不进实例，行为与原来逐字节一致（重复 op39 仍然静默）。
		if errors.Is(reportErr, errDungeonMonsterAlreadyDefeated) && runtime.partyInstance != nil {
			return s.replayCurrentDungeonMonsterDeathLocked(session, runtime, request.RuntimeObjectKey)
		}
		if errors.Is(reportErr, errDungeonMonsterAlreadyDefeated) {
			// A single-session retry can still be the recovery request for a
			// transaction or packet failure after the death commit. Only replay
			// while no authoritative death body has been recorded; ordinary
			// repeated op39 requests keep the historical silent behavior.
			if _, responseRecorded := runtime.Room.DeathResponseBody(request.RuntimeObjectKey); !responseRecorded {
				return s.replayCurrentDungeonMonsterDeathLocked(session, runtime, request.RuntimeObjectKey)
			}
			return s.persistCurrentDungeonRoomClearQuestLocked(session, runtime)
		}
		s.logGameEvent(session, "game-dungeon-monster-death-blocked",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"object_key", request.RuntimeObjectKey,
			"reason", "runtime_actor_owner_rejected",
			"error", reportErr)
		return nil
	}
	if storyProcDeath {
		if runtime.storyProcDeaths == nil {
			runtime.storyProcDeaths = make(map[uint32]dungeonStoryProcDeathReceipt)
		}
		runtime.storyProcDeaths[request.RuntimeObjectKey] = dungeonStoryProcDeathReceipt{Evidence: storyProcEvidence, MapID: scene.Map.Map.ID, Coordinate: scene.Coordinate, Reference: target.Reference}
	}
	scene, _ = runtime.Session.Scene()
	storyAIBossGate, storyAIBossGateActive := currentDungeonStoryAIBossDeathGate(runtime, scene)
	if err := s.settleCurrentDungeonRewardEnemyQuestDropLocked(
		session,
		runtime,
		request.RuntimeObjectKey,
	); err != nil {
		return err
	}
	rewardMonster := dropMonster
	rewardMonsterCandidate := ordinaryDropCandidate
	if !rewardMonsterCandidate {
		if monster, ok := runtime.Room.MonsterByReference(
			target.Reference,
			runtimeDungeonMonsterAnnounced,
			runtimeDungeonMonsterDefeated,
		); ok {
			rewardMonster = monster
			rewardMonsterCandidate = true
		}
	}
	if rewardMonsterCandidate && !clearConditionActorDeath {
		clearConditionSource = currentDungeonClearConditionSource(runtime, rewardMonster)
		clearConditionActorDeath = clearConditionSource != ""
		if clearConditionActorDeath && !bossActorDeath {
			bossActorDeath = true
			bossActorType = 3
			bossActorSource = clearConditionSource
		}
	}
	var deathDrops []currentDungeonDeathDropWire
	dropSource := "none"
	if rewardMonsterCandidate {
		if (s.currentRiftEntryEnabled(runtime.Dungeon.ID) || (s.currentTimeRiftLootEnabled(runtime.Dungeon.ID) && cleared)) && bossActorDeath && scene.Boss {
			deathDrops, err = s.planCurrentRiftBossDrops(session, runtime, scene)
		} else {
			deathDrops, err = s.planPartyDungeonMonsterDrops(session, runtime, rewardMonster, playerObjectKey)
		}
		if err != nil {
			reason := "runtime_pvf_csharp_formula_drop_plan_failed_zero_drop_fallback"
			dropSource = "zero_drop_fallback"
			s.logGameEvent(session, "game-dungeon-monster-drop-deferred",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"monster_id", rewardMonster.Spawn.MonsterID,
				"monster_object_key", request.RuntimeObjectKey,
				"logical_monster_object_key", rewardMonster.ObjectKey,
				"map_random_drop_count", rewardMonster.Spawn.RandomDropCount,
				"map_offset7_specified_pool_count", rewardMonster.Spawn.FixedDropCount,
				"reason", reason,
				"error", err)
			deathDrops = nil
		} else if len(deathDrops) != 0 {
			if (s.currentRiftEntryEnabled(runtime.Dungeon.ID) || (s.currentTimeRiftLootEnabled(runtime.Dungeon.ID) && cleared)) && bossActorDeath && scene.Boss {
				dropSource = "particularhellparty_boss_guaranteed_fragments_and_epic_probability"
			} else {
				dropSource = "runtime_pvf_csharp_drop_formula_standard_item_branch"
			}
		} else {
			dropSource = "runtime_pvf_csharp_drop_formula_no_standard_item"
		}
	}
	var monsterExperience currentDungeonMonsterExperienceCommitResult
	var monsterExperienceBody []byte
	if rewardMonsterCandidate {
		experienceContext, cancelExperience := context.WithTimeout(context.Background(), createWriteTimeout)
		monsterExperience, err = s.awardCurrentDungeonMonsterExperience(
			experienceContext,
			session,
			runtime,
			rewardMonster,
		)
		cancelExperience()
		if err != nil {
			s.logGameEvent(session, "game-dungeon-monster-experience-deferred",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"monster_id", rewardMonster.Spawn.MonsterID,
				"monster_object_key", request.RuntimeObjectKey,
				"logical_monster_object_key", rewardMonster.ObjectKey,
				"reason", "runtime_pvf_csharp_domain_formula_or_atomic_progression_unavailable",
				"error", err)
		} else {
			recordCurrentDungeonMonsterExperience(runtime, rewardMonster, monsterExperience)
			if monsterExperience.Award.Gain > 0 {
				runtime.Character = dnfrepo.CloneCharacter(monsterExperience.Character)
				monsterExperienceBody = buildCurrentFinishLoadingCharacterStateBodyWithPresentation(
					monsterExperience.Character,
					monsterExperience.Skill.Points,
					&currentFinishLoadingExperiencePresentation{
						GrowthContractBonus: monsterExperience.GrowthContractBonus,
					},
				)
				if len(monsterExperienceBody) != currentFinishLoadingCharacterStateBodySize {
					s.logGameEvent(session, "game-dungeon-monster-experience-deferred",
						"dungeon_id", runtime.Dungeon.ID,
						"monster_id", dropMonster.Spawn.MonsterID,
						"monster_object_key", request.RuntimeObjectKey,
						"reason", "current_exe_op37_body_shape_invalid_after_atomic_commit",
						"body_len", len(monsterExperienceBody))
					monsterExperienceBody = nil
				}
			}
		}
	}
	forcedBossClearCount := 0
	forcedBossVisualDeathCount := 0
	var stagedStoryForcedCombatEnd *currentDungeonForcedCombatEnd
	nextStoryCoordinate, nextStoryStageIndex, pendingStoryAdvance := currentDungeonPendingStoryAdvance(runtime, scene)
	if pendingStoryAdvance && bossActorDeath && !cleared && !storyAIBossGateActive &&
		!isPVFTutorialDungeonScene(runtime, scene) {
		prepared, prepareErr := s.prepareCurrentDungeonForcedCombatEndLocked(runtime, request.RuntimeObjectKey)
		if prepareErr != nil {
			s.logGameEvent(session, "game-dungeon-story-stage-force-clear-deferred",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"story_stage_index", runtime.StoryStageIndex,
				"next_story_stage_index", nextStoryStageIndex,
				"next_story_room", nextStoryCoordinate.String(),
				"boss_object_key", request.RuntimeObjectKey,
				"reason", "story_stage_boss_forced_clear_prepare_failed",
				"error", prepareErr)
		} else {
			stagedStoryForcedCombatEnd = &prepared
			forcedBossClearCount = len(prepared.Targets)
			cleared = cleared || prepared.Cleared
			if prepared.SceneOK {
				scene = prepared.Scene
			}
		}
	}
	var optionalStoryRoom []worldmap.RoomCoordinate
	if pendingStoryAdvance && cleared && (!storyAIBossGateActive || storyAIBossGate.Ready) {
		optionalStoryRoom = append(optionalStoryRoom, nextStoryCoordinate)
	}
	responseBody, err := buildCurrentDungeonDeathNotificationBodyWithDrops(
		target.ObjectKey,
		target.ResponseKind,
		deathDrops,
		optionalStoryRoom...,
	)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyDieMonster), partyDungeonDeathBodyForRecipient(runtime, responseBody, playerObjectKey), 0); err != nil {
		return err
	}
	// 权威死亡状态：① 记下来（供重复 op39 幂等重放）；② 广播给同一队伍副本实例内的队友，
	// 否则队友的客户端永远不知道这只怪已经死了（复测症状：一边打完，另一边还剩白色残影）。
	runtime.Room.RecordDeathResponseBody(request.RuntimeObjectKey, responseBody)
	barrierUpdates, barrierErr := s.recordCurrentAntonConstructorDeathLocked(session, runtime, request.RuntimeObjectKey, time.Now())
	raidBarrierProjections = append(raidBarrierProjections, barrierUpdates...)
	if barrierErr != nil {
		s.logGameEvent(session, "game-raid-constructor-barrier-rejected", "object_key", request.RuntimeObjectKey, "error", barrierErr)
	}
	s.broadcastPartyDungeonInstanceDeathNotification(
		session,
		runtime,
		request.RuntimeObjectKey,
		responseBody,
		"op39_authoritative_death_after_killer_notification",
	)
	if stagedStoryForcedCombatEnd != nil {
		forcedVisual, forcedCleared, flushErr := s.flushCurrentDungeonForcedCombatEndLocked(
			session,
			runtime,
			request.RuntimeObjectKey,
			"op39_story_stage_boss_actor_death_after_source_coordinate_response",
			*stagedStoryForcedCombatEnd,
		)
		if flushErr != nil {
			return flushErr
		}
		forcedBossVisualDeathCount = forcedVisual
		cleared = cleared || forcedCleared
	}
	if len(monsterExperienceBody) != 0 {
		if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, monsterExperienceBody, 0); err != nil {
			return err
		}
		s.logGameEvent(session, "game-dungeon-monster-experience-awarded",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"monster_id", monsterExperience.Award.MonsterID,
			"monster_object_key", request.RuntimeObjectKey,
			"logical_monster_object_key", rewardMonster.ObjectKey,
			"monster_level", monsterExperience.Award.MonsterLevel,
			"monster_type", monsterExperience.Award.MonsterType,
			"named_monster", monsterExperience.Award.NamedMonster,
			"monster_table_exp", monsterExperience.Award.MonsterTableEXP,
			"dungeon_weight", monsterExperience.Award.DungeonWeight,
			"story_experience_quest_id", runtime.storyExperience.QuestID,
			"story_experience_rate_percent", runtime.storyExperience.RatePercent,
			"difficulty_rate", monsterExperience.Award.DifficultyRate,
			"monster_type_rate", monsterExperience.Award.MonsterTypeRate,
			"level_penalty", monsterExperience.Award.LevelPenalty,
			"pre_penalty_gain", monsterExperience.Award.PrePenaltyGain,
			"experience_gain", monsterExperience.Award.Gain,
			"growth_contract_bonus", monsterExperience.GrowthContractBonus,
			"guild_bonus", monsterExperience.GuildBonus,
			"committed_experience_gain", uint64(monsterExperience.Award.Gain)+uint64(monsterExperience.GrowthContractBonus)+uint64(monsterExperience.GuildBonus),
			"honor_expert_gain", monsterExperience.HonorExpertGain,
			"honor_expert_level", numericCharacterStatValue(monsterExperience.Character, currentHonorExpertLevelStatKey),
			"honor_expert_progress", numericCharacterStatValue(monsterExperience.Character, currentHonorExpertProgressExperienceStatKey),
			"sp_gain", monsterExperience.SPGain,
			"tp_gain", monsterExperience.TPGain,
			"remaining_sp", monsterExperience.Skill.Points.RemainingSP,
			"remaining_tp", monsterExperience.Skill.Points.RemainingTP,
			"new_level", monsterExperience.Character.Level,
			"new_total_exp", statU32(monsterExperience.Character, "exp", 0),
			"op38_then_op37", true,
			"op37_body_len", len(monsterExperienceBody),
			"formula_source", "86jp_domain_formula_with_runtime_pvf_monsterexp_table",
			"packet_source", "current_exe_sub_1D78240_full_character_state")
	}
	if rewardMonsterCandidate {
		huntCtx, cancelHunt := context.WithTimeout(context.Background(), createWriteTimeout)
		huntErr := s.persistCurrentDungeonHuntEnemyKillPhaseA(
			huntCtx,
			session,
			runtime,
			scene,
			rewardMonster,
			request.RuntimeObjectKey,
		)
		cancelHunt()
		if huntErr != nil && !errors.Is(huntErr, errDungeonHuntEnemyCompletionUnavailable) {
			s.logGameEvent(session, "game-dungeon-hunt-enemy-progress-deferred",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"monster_id", rewardMonster.Spawn.MonsterID,
				"monster_object_key", request.RuntimeObjectKey,
				"logical_monster_object_key", rewardMonster.ObjectKey,
				"reason", "86jp_hunt_enemy_progress_owner_failed",
				"error", huntErr)
		}
		s.checkCurrentDungeonQuestMonsterDrops(session, runtime, rewardMonster.Spawn.MonsterID)
		// Keep ordinary drops, EXP and boss/story completion in their existing
		// order even if quest persistence or its snapshot needs a retry.
		huntProgressErr = s.persistCurrentDungeonHuntMonsterDeathLocked(session, runtime, request.RuntimeObjectKey)
	}
	if bossActorDeath && !storyAIBossGateActive && !currentLukeNormalBossesPending(runtime, scene) && !isPVFTutorialDungeonScene(runtime, scene) &&
		stagedStoryForcedCombatEnd == nil {
		forcedDefeated, forcedVisual, forcedCleared, forceErr := s.forceCurrentDungeonRemainingHostilesForCombatEndLocked(
			session,
			runtime,
			request.RuntimeObjectKey,
			"op39_boss_actor_death",
		)
		if forceErr != nil {
			s.logGameEvent(session, "game-dungeon-boss-forced-clear-deferred",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"boss_object_key", request.RuntimeObjectKey,
				"monster_id", rewardMonster.Spawn.MonsterID,
				"boss_rank", rewardMonster.Spawn.Rank,
				"boss_actor_type", bossActorType,
				"boss_actor_source", bossActorSource,
				"forced_defeated_count", forcedDefeated,
				"reason", "86jp_boss_death_forced_clear_commit_failed",
				"error", forceErr)
		} else if forcedDefeated > 0 || forcedCleared {
			forcedBossClearCount = forcedDefeated
			forcedBossVisualDeathCount = forcedVisual
			cleared = cleared || forcedCleared
			scene, _ = runtime.Session.Scene()
			s.logGameEvent(session, "game-dungeon-boss-forced-room-clear",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"boss_object_key", request.RuntimeObjectKey,
				"monster_id", rewardMonster.Spawn.MonsterID,
				"boss_rank", rewardMonster.Spawn.Rank,
				"boss_actor_type", bossActorType,
				"boss_actor_source", bossActorSource,
				"forced_defeated_count", forcedDefeated,
				"forced_visual_death_count", forcedBossVisualDeathCount,
				"defeated_actor_count", len(scene.DefeatedObjects),
				"tracked_hostile_count", len(scene.ExpectedHostiles),
				"blocking_hostile_count", len(scene.BlockingHostiles),
				"room_cleared", scene.Cleared,
				"flow_source", "86jp_boss_death_means_combat_end")
		}
	}
	if err := runtime.cacheCurrentDungeonRoom(); err != nil {
		return fmt.Errorf("cache dungeon room after actor death: %w", err)
	}
	if err := s.persistCurrentDungeonRoomClearQuestLocked(session, runtime); err != nil {
		return err
	}
	if cleared || (storyAIBossGateActive && storyAIBossGate.Ready && scene.Cleared) {
		if clearedScene, ok := runtime.Session.Scene(); ok {
			s.awardCurrentPetRoomClearLocked(session, runtime, clearedScene, "authoritative_monster_room_clear")
		}
	}
	// 锦标赛赛制推进（默认档 `runtime.Tournament == nil` ⇒ 这一整段不可达）。
	//
	// 放在这里而不是 op39 入口：本函数上面的 Boss 强制清怪
	// （`forceCurrentDungeonRemainingHostilesForCombatEndLocked`，:581-626）可能**一次性**
	// 把剩余阻挡怪全部标成权威死亡，只在入口喂单个 key 会漏掉它们 ⇒ 第 4 轮永远凑不齐。
	// `syncCurrentTournamentActorDeathsLocked` 每次幂等全量同步，自愈且不重复计。
	// 厂商对拍：`DungeonKillApplicationService.cs:444` → `TournamentDungeonCoordinator.cs:126`
	// → `TournamentDungeonRuntime.cs:257-324`。
	tournamentAdvance := s.syncCurrentTournamentActorDeathsLocked(session, runtime)
	if tournamentAdvance.TournamentCompleted {
		s.beginCurrentTournamentRewardLocked(session, runtime)
	}
	s.logGameEvent(session, "game-dungeon-monster-death-accepted",
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"object_key", request.RuntimeObjectKey,
		"related_actor_object_key", request.OwnerObjectKey,
		"request_layout", request.Layout,
		"combat_entry_count", request.CombatEntryCount,
		"actor_kind", target.Reference.Kind,
		"scripted_tutorial_death", scriptedTutorialDeath,
		"story_proc_death", storyProcDeath,
		"story_proc_quest_id", storyProcEvidence.QuestID,
		"story_proc_ai_id", storyProcEvidence.AICharacterID,
		"story_proc_action_path", storyProcEvidence.ActionPath,
		"story_proc_passive_id", storyProcEvidence.PassiveObjectID,
		"story_proc_behavior_index", storyProcEvidence.BehaviorIndex,
		"scripted_tutorial_monster_id", scriptedTutorialMonster.Spawn.MonsterID,
		"scripted_tutorial_rank", scriptedTutorialMonster.Spawn.Rank,
		"scripted_tutorial_monster_index", scriptedTutorialEvidence.MonsterIndex,
		"scripted_tutorial_cinematic_id", scriptedTutorialEvidence.CinematicID,
		"scripted_tutorial_cinematic_path", scriptedTutorialEvidence.CinematicPath,
		"owner_sentinel_blocking_clear_target", ownerSentinelBlockingClearTarget,
		"owner_sentinel_blocking_clear_source", ownerSentinelBlockingClearSource,
		"owner_sentinel_ordinary_target", ownerSentinelOrdinaryTarget,
		"friendly_apc_contribution_proven", friendlyAPCDeath,
		"friendly_owner_proven", friendlyOwnerDeath,
		"response_kind", target.ResponseKind,
		"response_opcode", uint16(dnfenum.CmdPacketNotifyDieMonster),
		"response_body_len", len(responseBody),
		"story_stage_index", runtime.StoryStageIndex,
		"next_story_stage_index", nextStoryStageIndex,
		"story_stage_advance", len(optionalStoryRoom) == 1,
		"story_stage_next_room", nextStoryCoordinate.String(),
		"defeated_actor_count", len(scene.DefeatedObjects),
		"tracked_hostile_count", len(scene.ExpectedHostiles),
		"blocking_hostile_count", len(scene.BlockingHostiles),
		"room_cleared", cleared,
		"boss_actor_death", bossActorDeath,
		"boss_actor_type", bossActorType,
		"boss_actor_source", bossActorSource,
		"clear_condition_actor_death", clearConditionActorDeath,
		"clear_condition_source", clearConditionSource,
		"logical_monster_resolved", rewardMonsterCandidate,
		"logical_monster_id", rewardMonster.Spawn.MonsterID,
		"logical_monster_object_key", rewardMonster.ObjectKey,
		"boss_forced_defeated_count", forcedBossClearCount,
		"boss_forced_visual_death_count", forcedBossVisualDeathCount,
		"story_ai_boss_dual_death_gate", storyAIBossGateActive,
		"story_ai_boss_dual_death_ready", storyAIBossGate.Ready,
		"story_dummy_boss_object_keys", storyAIBossGate.DummyBossObjectKeys,
		"story_ai_boss_object_keys", storyAIBossGate.AIBossObjectKeys,
		"boss_room", scene.Boss,
		"drop_count", len(deathDrops),
		"drop_source", dropSource,
		"monster_experience_gain", monsterExperience.Award.Gain,
		"monster_experience_state_sent", len(monsterExperienceBody) != 0,
		"response", "current_exe_op38_actor_death_notification")
	if runtime.bossDieCheckPending &&
		uint32(runtime.bossDieCheckPendingRequest.TargetObjectKey) == request.RuntimeObjectKey {
		pendingRequest := runtime.bossDieCheckPendingRequest
		s.logGameEvent(session, "game-dungeon-boss-die-check-resume-after-death",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"related_actor_object_key", pendingRequest.RelatedActorObjectKey,
			"target_object_key", pendingRequest.TargetObjectKey,
			"reason", "same_target_authoritative_op39_committed")
		return s.handleDungeonBossDieCheckLocked(session, runtime, pendingRequest, true)
	}
	if cleared || (storyAIBossGateActive && storyAIBossGate.Ready && scene.Cleared) {
		if err := s.completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked(session, runtime, scene, request.RuntimeObjectKey); err != nil {
			return err
		}
		// 同一判定的第二条入口：玩家先清波次、后清房图阻挡怪时，清房那一刻落在**普通怪**
		// 死亡上。深渊房间的清空判据在这里再判一次（非深渊房间恒不介入）。
		// 见 dungeon_hell_party_clear_settlement.go。
		if err := s.completeCurrentDungeonHellPartyFinalRoomAfterDeathLocked(
			session,
			runtime,
			scene,
			request.RuntimeObjectKey,
			"room_cleared_by_ordinary_monster_op39",
		); err != nil {
			return err
		}
		if err := s.completePVFTutorialBosslessFinalRoomAfterDeathLocked(session, runtime, scene); err != nil {
			return err
		}
	}
	s.cancelCurrentDungeonReturnAfterDungeonEvidenceLocked(
		session,
		runtime,
		"accepted_hostile_op39_after_pending_op24",
	)
	return nil
}

// replayCurrentDungeonMonsterDeathLocked 把"这只怪已经是权威死亡状态"幂等地回给请求方。
//
// 队伍级共享实例下，两个成员的客户端会各自判定同一只怪死亡并各发一次 op39：先到者被接受
// （权威死亡 + 掉落 + 广播），后到者落在共享房间的 already_defeated 上。只回错误会让后到
// 那一侧的客户端把尸体留在白色半死状态，所以这里重发一份**同一形状**的 op38 死亡通知
// （不带掉落行：掉落已经由权威那一份下发/广播过，重复下发会在客户端复制地面物品），
// 并把权威清怪状态补到请求方自己的副本场景里（房间清怪/阻挡怪不落后）。
//
// 调用方持有 session.dungeon.mu。
func (s *Service) replayCurrentDungeonMonsterDeathLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	objectKey uint32,
) error {
	if err := s.persistCurrentDungeonHuntMonsterDeathLocked(session, runtime, objectKey); err != nil {
		return err
	}
	if err := s.settleCurrentDungeonRewardEnemyQuestDropLocked(session, runtime, objectKey); err != nil {
		s.logGameEvent(session, "game-dungeon-monster-death-reward-replay-deferred",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"object_key", objectKey,
			"error", err)
		return err
	}
	if body, ok := runtime.Room.DeathResponseBody(objectKey); ok {
		return s.replayPartyDungeonAuthoritativeDeathWithBodyLocked(
			session,
			runtime,
			objectKey,
			body,
			"duplicate_op39_for_shared_instance_authoritative_defeat",
		)
	}
	target, ok := runtime.Room.DefeatedDeathTarget(objectKey)
	if !ok {
		s.logGameEvent(session, "game-dungeon-monster-death-blocked",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"object_key", objectKey,
			"reason", "runtime_actor_owner_rejected",
			"error", errDungeonMonsterAlreadyDefeated)
		return nil
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(target.ObjectKey, target.ResponseKind, nil)
	if err != nil {
		return err
	}
	return s.replayPartyDungeonAuthoritativeDeathWithBodyLocked(session, runtime, objectKey, body, "duplicate_op39_rebuilt_authoritative_defeat")
}

// replayPartyDungeonAuthoritativeDeathWithBodyLocked 把一份既有的权威死亡/退役通知重放给请求方，
// 并把权威清怪状态补到请求方自己的副本场景；房间因此被清空时，**在他自己的会话里**跑一遍
// 最终房间完成（结算链）—— 否则只有击杀方的会话会结算，队员永远结算不了。
//
// 调用方持有 session.dungeon.mu。
func (s *Service) replayPartyDungeonAuthoritativeDeathWithBodyLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	objectKey uint32,
	body []byte,
	reason string,
) error {
	if len(body) == 0 {
		return nil
	}
	sessionSceneMarked := false
	sessionRoomCleared := false
	if runtime.Session != nil {
		cleared, markErr := runtime.Session.MarkHostileDefeated(objectKey)
		switch {
		case markErr == nil:
			sessionSceneMarked = true
			sessionRoomCleared = cleared
		case errors.Is(markErr, worldmap.ErrHostileAlreadyDefeated):
			sessionSceneMarked = true
			// APC quest-drop handling can fail after committing the actor death.
			// Resume its final-room handling from the authoritative scene while
			// preserving the ordinary-monster notification replay path.
			if target, found := runtime.Room.DefeatedDeathTarget(objectKey); found && target.Reference.Kind == worldmap.HostileAICharacter {
				if scene, ok := runtime.Session.Scene(); ok {
					sessionRoomCleared = scene.Cleared
				}
			}
		case errors.Is(markErr, worldmap.ErrHostileNotBound):
			// 请求方这一侧没有这个 actor（非敌对 actor 不参与阻挡怪扫描）：只重放通知。
		default:
			s.logGameEvent(session, "game-dungeon-monster-death-replay-scene-deferred",
				"dungeon_id", runtime.Dungeon.ID,
				"object_key", objectKey,
				"error", markErr)
		}
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketNotifyDieMonster), partyDungeonDeathBodyForRecipient(runtime, body, currentSceneActorObjectKey(session.selectedCharacterID)), 0); err != nil {
		return err
	}
	if err := s.persistCurrentDungeonRoomClearQuestLocked(session, runtime); err != nil {
		return err
	}
	s.logGameEvent(session, "game-dungeon-monster-death-replayed",
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"object_key", objectKey,
		"response_opcode", uint16(dnfenum.CmdPacketNotifyDieMonster),
		"response_body_len", len(body),
		"session_scene_marked", sessionSceneMarked,
		"session_room_cleared", sessionRoomCleared,
		"reason", reason,
		"source", "current_exe_op38_actor_death_notification")
	if sessionRoomCleared && runtime.Session != nil {
		// 队友自己的房间清空了：在他自己的会话里跑最终房间完成（与"被接受的 op39"同一条链）。
		if scene, ok := runtime.Session.Scene(); ok {
			s.awardCurrentPetRoomClearLocked(session, runtime, scene, "authoritative_party_death_replay_room_clear")
			if err := s.completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked(session, runtime, scene, objectKey); err != nil {
				return err
			}
			if err := s.completePVFTutorialBosslessFinalRoomAfterDeathLocked(session, runtime, scene); err != nil {
				return err
			}
			s.logGameEvent(session, "game-dungeon-party-instance-final-room-completed",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"object_key", objectKey,
				"source", reason)
		}
	}
	return nil
}

func (runtime *runtimeDungeonState) accumulateCurrentDungeonSettlementMonsterExperience(
	monsterType byte,
	gain uint32,
) {
	if runtime == nil || gain == 0 {
		return
	}
	runtime.settlementMonsterExperienceTotal = saturatingCurrentDungeonUint32Add(
		runtime.settlementMonsterExperienceTotal,
		gain,
	)
	switch monsterType {
	case 1:
		runtime.settlementChampionExperience = saturatingCurrentDungeonUint32Add(
			runtime.settlementChampionExperience,
			gain,
		)
	case 2:
		runtime.settlementSuperChampionExperience = saturatingCurrentDungeonUint32Add(
			runtime.settlementSuperChampionExperience,
			gain,
		)
	case 3:
		runtime.settlementBossExperience = saturatingCurrentDungeonUint32Add(
			runtime.settlementBossExperience,
			gain,
		)
	}
}

func saturatingCurrentDungeonUint32Add(current uint32, delta uint32) uint32 {
	if ^uint32(0)-current < delta {
		return ^uint32(0)
	}
	return current + delta
}
