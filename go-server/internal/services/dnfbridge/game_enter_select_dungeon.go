package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// backToVillageEnterSelectSceneFinalizerWindow 限定"回城后那条场景收尾 op15"抑制的
// 有效窗口。当前客户端的场景收尾 op15 紧跟 op24 到来；一旦布防后窗口内没有等到它，
// 说明这条 op15 不会补发，此时继续吞掉 op15 会把玩家真正的开门请求吃掉，选择器要等
// 客户端自己重发才开。实测（2026-09-25 05:39 那双开会话，conn game-000006）：
// 05:39:00.6 布防 → 05:39:45.7 玩家 op15 被吞（response=none）→ 05:39:57.8 客户端重发
// 才回 state-send，玩家在副本门口白等 12.0 秒（操作者口径"要站 10 秒才出选择地图界面"）。
// 取 3s：远大于"紧跟 op24"的毫秒级间隔，又远小于客户端的重发周期。
const backToVillageEnterSelectSceneFinalizerWindow = 3 * time.Second

const (
	// townTransportEnterSelectWindowEnv 是"城镇 transport 后那条场景收尾 op15"抑制窗口的开关。
	// 未设置 / 空 / 非法 / 非正数 = 修复档（townTransportEnterSelectWindowDefault）；
	// 正整数 = 该毫秒数；legacy / off / false / no = 逐字节回到改动前的无时限恒吞（回退档）。
	townTransportEnterSelectWindowEnv = "DNFBRIDGE_TOWN_TRANSPORT_ENTER_SELECT_WINDOW_MS"
	// townTransportEnterSelectWindowDefault 是修复档的默认窗口。**为什么取 500 ms**：
	// 带毫秒时间戳的实测里，"op36 提交 -> op15"的全部 5 个间隔最小 **2054.7 ms**，唯一带
	// 时间戳的那次吞包是 **2361.3 ms**（`_dist/enter-select-latch-arming-investigation.md` §3：
	// L75245->L75247 2054.7 ms / L75336->L75338 2060.8 ms / L72824->L72826 2361.3 ms）。
	// 500 ms 距最近的实测样本仍有 **4 倍以上**余量，同时又能抑制"真正紧跟过图的那条场景收尾
	// op15"（它的存在至今未被证实）。原先的 1500 ms 虽然也能覆盖全部 20/20 历史样本，但离
	// 最近的 2054.7 ms 只有 1.37 倍、安全垫仅 550 ms，所以本次收窄到 500 ms。
	//
	// 更干净的终局是**整段退役**这个抑制（删掉下面的判定分支与配套的挂账字段/布防点，回到厂商
	// 口径 —— 参考实现没有任何等价守卫，`DungeonEntryHandler.cs:106-191` 对每条 op15 都回包，
	// 拒绝也回拒绝包）：但"客户端会补发一条场景收尾 op15"这个前提目前仍未证死，500 ms 以可忽略
	// 的代价把它保留下来。现场量准"收尾包 vs 玩家 intent"之后，用这个开关调档或直接退役即可。
	townTransportEnterSelectWindowDefault = 500 * time.Millisecond
	// townTransportEnterSelectWindowLegacyWord 与下面的别名（off/false/no）= 回退档字面量。
	townTransportEnterSelectWindowLegacyWord = "legacy"
)

// currentTownTransportEnterSelectWindow 限定"城镇 transport（世界地图/传送站、传送阵）之后那条
// 场景收尾 op15"抑制的有效窗口 —— 口径与兄弟 backToVillageEnterSelectSceneFinalizerWindow
// 逐条同源（那次是 2026-09-25 修的同形缺陷，见 dungeon_return_town.go 的
// markBackToVillageEnterSelectPending）：窗口内 = 客户端自己的场景收尾 op15，照旧不回包
// （逐字节等于改动前）；窗口外 = 玩家自己的开门意图，必须落回正常应答路径。
//
// 线上依据（2026-09-28 全库只读统计，见 `_dist/dungeon-door-trigger-diagnosis.md` 与
// `_dist/enter-select-latch-arming-investigation.md`）：
// 99 次 transport 布防 / 20 次吞包（`response=none`）/ 39 次选择页应答；
// 20 次吞包的布防落点 20/20 是"副本门口图"（14 次 Town/CentralPark/CentralPark_Waiting.map
// 的 `[dungeon gate] 39`）；4 次吞包发生在布防之后 132/299/942/1564 行日志 —— 这就是"挂账
// 会一直挂着"的直接证据。
//
// 注意别再复述"那两次吞掉之后玩家卡住了"：game-000004 / game-000005 那两条 op15 的 body 非零
// （`88130000` = 5000），紧随的 op16 直接进了那一张图、没有损失；它们只证明"挂账一直挂着"，
// 不能当作"吞包害人"的证据（同上前一份取证的 §6）。
//
// 返回 0 = 回退档（无时限恒吞，逐字节等于改动前）。
func currentTownTransportEnterSelectWindow() time.Duration {
	switch strings.ToLower(envString(townTransportEnterSelectWindowEnv, "")) {
	case townTransportEnterSelectWindowLegacyWord, "off", "false", "no":
		return 0
	}
	if millis := envInt(townTransportEnterSelectWindowEnv, 0); millis > 0 {
		return time.Duration(millis) * time.Millisecond
	}
	return townTransportEnterSelectWindowDefault
}

func (s *Service) sendEnterSelectDungeonState(
	session *gameSession,
	source string,
	transport bool,
	requestDriven bool,
) error {
	if !transport && requestDriven && session != nil {
		session.townMu.Lock()
		townTransportFinalizer := session.townTransportEnterSelectPending
		townTransportArmedAt := session.townTransportEnterSelectArmedAt
		session.townTransportEnterSelectPending = false
		session.townMu.Unlock()
		if townTransportFinalizer {
			window := currentTownTransportEnterSelectWindow()
			armedAge := time.Since(townTransportArmedAt)
			if window <= 0 {
				// 回退档（DNFBRIDGE_TOWN_TRANSPORT_ENTER_SELECT_WINDOW_MS=legacy/off/false/no）：
				// 逐字节回到改动前 —— 无时限恒吞，连这条日志的字段都刻意保持逐字不变。
				s.logGameEvent(session, "game-upper-enter-select-dungeon-after-town-transport-suppressed",
					"source", source,
					"char_id", session.selectedCharacterID,
					"reason", "current_exe_town_transport_scene_finalizer_op15_is_not_dungeon_selector_intent",
					"response", "none")
				return nil
			}
			if armedAge <= window {
				s.logGameEvent(session, "game-upper-enter-select-dungeon-after-town-transport-suppressed",
					"source", source,
					"char_id", session.selectedCharacterID,
					"armed_age_ms", armedAge.Milliseconds(),
					"window_ms", window.Milliseconds(),
					"reason", "current_exe_town_transport_scene_finalizer_op15_is_not_dungeon_selector_intent",
					"response", "none")
				return nil
			}
			// 窗口已过期：客户端没有按设计补发那条场景收尾 op15（线上实测 4 次在布防之后
			// 132/299/942/1564 行日志才来，2 次之后再没有第二条 op15）。这次 op15 就是玩家
			// 自己在副本门口撞出来的开门意图，必须照常应答，否则选图界面要等"回头再进"
			// （第二次触发）才弹。与兄弟的 `…-after-back-to-village-window-expired` 同形。
			s.logGameEvent(session, "game-upper-enter-select-dungeon-after-town-transport-window-expired",
				"source", source,
				"char_id", session.selectedCharacterID,
				"armed_age_ms", armedAge.Milliseconds(),
				"window_ms", window.Milliseconds(),
				"reason", "scene_finalizer_op15_never_arrived_answer_player_selector_intent",
				"response", "enter_select_state")
		}
	}
	if !transport && requestDriven && session != nil && session.backToVillageEnterSelectPending {
		armedAge := time.Since(session.backToVillageEnterSelectArmedAt)
		// A typed op24 from system-menu op132 is followed by a client-owned
		// scene-finalizer op15. It is not a fresh selector request.
		if armedAge <= backToVillageEnterSelectSceneFinalizerWindow {
			if err := s.commitPendingDungeonReturnBeforeTownEnterSelect(session, source+"_after_back_to_village_scene_finalizer"); err != nil {
				return err
			}
			session.backToVillageEnterSelectPending = false
			if err := s.rebuildRuntimePartyTownCoPresenceAfterReturn(
				session,
				source+"_after_back_to_village_scene_finalizer",
			); err != nil {
				return err
			}
			s.logGameEvent(session, "game-upper-enter-select-dungeon-after-back-to-village-suppressed",
				"source", source,
				"char_id", session.selectedCharacterID,
				"armed_age_ms", armedAge.Milliseconds(),
				"reason", "current_exe_op132_scene_finalizer_op15_is_not_selector_intent",
				"response", "none")
			return nil
		}
		// 布防已过期：客户端没有按设计补发那条场景收尾 op15（现场实测布防 45 秒后才来
		// 下一条 op15）。这次 op15 就是玩家自己的选择器请求，必须照常应答，否则界面
		// 要等客户端重发才开。收尾动作仍然照做（提交待决回城 + 重建同队同屏），随后
		// 落回下面的正常应答路径（重复提交由 townReturnPending 判据幂等挡掉）。
		s.logGameEvent(session, "game-upper-enter-select-dungeon-after-back-to-village-window-expired",
			"source", source,
			"char_id", session.selectedCharacterID,
			"armed_age_ms", armedAge.Milliseconds(),
			"window_ms", backToVillageEnterSelectSceneFinalizerWindow.Milliseconds(),
			"reason", "scene_finalizer_op15_never_arrived_answer_player_selector_intent",
			"response", "enter_select_state")
		if err := s.commitPendingDungeonReturnBeforeTownEnterSelect(session, source+"_after_back_to_village_window_expired"); err != nil {
			return err
		}
		session.backToVillageEnterSelectPending = false
		if err := s.rebuildRuntimePartyTownCoPresenceAfterReturn(
			session,
			source+"_after_back_to_village_window_expired",
		); err != nil {
			return err
		}
	}
	// Fixed v157's upper op15 failure consumes success and reason bytes and
	// releases its pending request flag. Legacy scene framing is separate.
	if !transport {
		if allowed, reason := s.currentRaidSelectorOpenAdmission(session); !allowed {
			s.logGameEvent(session, "game-raid-selector-open-rejected", "source", source, "reason", reason)
			return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketEnterSelectDungeon), []byte{0, 0}, 1)
		}
	}
	if !transport && requestDriven {
		if err := s.commitPendingDungeonReturnBeforeTownEnterSelect(session, source); err != nil {
			return err
		}
		retired, err := s.retireCompletedDungeonForTownSelect(session)
		if err != nil {
			s.logGameEvent(session, "game-upper-enter-select-dungeon-completed-runtime-retire-blocked",
				"source", source,
				"char_id", session.selectedCharacterID,
				"reason", "verified_town_origin_unavailable",
				"error", err)
		} else if retired {
			s.logGameEvent(session, "game-upper-enter-select-dungeon-completed-runtime-retired",
				"source", source,
				"char_id", session.selectedCharacterID,
				"reason", "settlement_page_select_other_dungeon_uses_current_op15_op27_route")
		}
	}
	if requestDriven && session.enterSelectDungeonAckSent {
		s.logGameEvent(session, "game-upper-enter-select-dungeon-duplicate-ack-replay",
			"source", source,
			"request_driven", true,
			"packet_count", 2,
			"reason", "current_client_retries_op15_after_post_start_map_state")
	}
	if !requestDriven && session.enterSelectDungeonSent {
		s.logGameEvent(session, "game-upper-enter-select-dungeon-duplicate-skipped", "source", source, "request_driven", false)
		return nil
	}
	if !transport && requestDriven {
		if err := s.restoreCurrentRaidSelectorProjection(session, source); err != nil {
			s.logGameEvent(session, "game-raid-selector-self-projection-restore-failed",
				"source", source,
				"char_id", selectedCharacterID(session),
				"error", err)
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	charID, _, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	// Guild hall selectors need the current cycle even after another member
	// unlocked a branch while this character stayed online.
	if hasCharacter && character.Stats["town_id"] == 8 && !transport {
		if err := s.writeCurrentGuildDungeonSnapshot(session); err != nil {
			return err
		}
	}

	contextReady := false
	contextReadyReason := "legacy_or_proactive_path"
	if !transport && requestDriven {
		contextReady, contextReadyReason = s.currentTownEnterSelectReady(session)
	}
	packetCount := 2
	if contextReady {
		packetCount++
	}
	s.logPacketEvent("game-enter-select-dungeon-state-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", charID,
		"scene_object_key", currentSceneActorObjectKey(charID),
		"transport", transport,
		"request_driven", requestDriven,
		"enter_msg_id", uint16(dnfenum.CmdPacketEnterSelectDungeon),
		"fatigue_msg_id", currentFatigueMsgID,
		"packet_count", packetCount,
		"town_context_ready", contextReady,
		"town_context_ready_reason", contextReadyReason,
		"reason", "current_exe_op15_ack_op36_fatigue_and_guarded_op27_enter_select_page")

	if transport {
		enterBody := buildEnterSelectDungeonBody(charID)
		if err := s.sendGame(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEnterSelectDungeon), enterBody); err != nil {
			return err
		}
	} else if err := s.sendGameUpperSuccess(session, uint16(dnfenum.CmdPacketEnterSelectDungeon), buildEnterSelectDungeonAckBody()); err != nil {
		return err
	}
	fatigueBody := buildCurrentFatigueBody(character, hasCharacter)
	s.logGameEvent(session, "game-enter-select-dungeon-fatigue-send",
		"char_id", charID,
		"msg_id", currentFatigueMsgID,
		"body_len", len(fatigueBody),
		"fatigue_used", binary.LittleEndian.Uint16(fatigueBody[0:2]),
		"fatigue_limit", binary.LittleEndian.Uint16(fatigueBody[2:4]),
		"actor_aux", binary.LittleEndian.Uint16(fatigueBody[4:6]),
		"display_used", binary.LittleEndian.Uint16(fatigueBody[6:8]),
		"actor_extra", binary.LittleEndian.Uint16(fatigueBody[8:10]),
		"body_source", "current_exe_sub_1D7ABE0_fatigue_five_u16")
	if transport {
		if err := s.sendGame(session, byte(dnfenum.GameCmdCommand), currentFatigueMsgID, fatigueBody); err != nil {
			return err
		}
	} else if err := s.sendGameUpperRawClass(session, currentFatigueMsgID, fatigueBody, 0); err != nil {
		return err
	}
	if hasCharacter {
		if err := s.sendCurrentDungeonCycleInfo(session, character); err != nil {
			return err
		}
		if err := s.sendCurrentDimensionCount(session, character); err != nil {
			return err
		}
	}
	session.enterSelectDungeonSent = true
	if requestDriven {
		session.enterSelectDungeonAckSent = true
	}
	if !transport && requestDriven {
		if err := s.sendCurrentTownEnterSelectContext(session, source+"_after_op15_ack_fatigue"); err != nil {
			return err
		}
		if contextReady {
			if err := s.synchronizeRuntimePartyMembersEnterSelect(session, source+"_party_members"); err != nil {
				return err
			}
		}
	}
	return nil
}

// synchronizeRuntimePartyMembersEnterSelect moves every other same-area
// member into the selector after any member opens it. Selecting a dungeon map
// is a party scene transition, not a leader-only action: the canonical party
// leader remains state.UserID, while initiator identifies the client that
// triggered the transition. This prevents the former partial transition where
// a non-leader opened the selector and the rest of the party stayed in town.
func (s *Service) synchronizeRuntimePartyMembersEnterSelect(initiator *gameSession, source string) error {
	if initiator == nil || initiator.selectedCharacterID == 0 || s.onlinePlayers == nil {
		return nil
	}
	state := runtimePartyStateSnapshot(initiator)
	if state.PartyID <= 0 || state.UserID == 0 {
		return nil
	}
	// 只有队长（或单人）打开选择器时才把队友一起带进选择器。
	// 队员打开城镇地图窗口时不能把队长推进"跟随者"状态——否则队长会拿到
	// 队员的选择器页面，而队员反而成了实际发起者（进图崩溃的根因之一）。
	if initiator.selectedCharacterID != state.UserID {
		s.logGameEvent(initiator, "game-party-enter-select-non-leader-initiated",
			"source", source,
			"char_id", initiator.selectedCharacterID,
			"party_id", state.PartyID,
			"leader_char_id", state.UserID,
			"reason", "only_party_leader_drives_party_selector")
		// 默认（开关 off）：到此为止，零发包，逐字节保持改动前行为。
		//
		// 开关打开时：给**该发起者本人**补一条完整跟随者转换（同 `prepareRuntimePartyFollowerEnterSelect`
		// 原语，与队长驱动时队员拿到的那条一字不差），因为当前客户端要用 op15 成功体安装
		// "可选副本页"、且必须在该 actor 从城镇脱离之后，否则只有一个背景（操作者报障原文）。
		// 归属不变：state.UserID（队长）不动、其他队员零发包、队长会话零改动。
		// 证据与回退见 party_selector_member_self_prepare.go。
		if partySelectorMemberSelfPrepareEnabled() {
			prepared, err := s.prepareRuntimePartyFollowerEnterSelect(
				initiator,
				state.UserID,
				state.PartyID,
				source+"_non_leader_self_prepare",
			)
			if err != nil {
				return err
			}
			s.logGameEvent(initiator, "game-party-enter-select-member-self-prepared",
				"source", source,
				"char_id", initiator.selectedCharacterID,
				"party_id", state.PartyID,
				"leader_char_id", state.UserID,
				"prepared", prepared,
				"switch", partySelectorMemberSelfPrepareEnv)
		}
		return nil
	}
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == 0 || member.UserID == initiator.selectedCharacterID {
			continue
		}
		// 频道隔离（彻底隔离口径）：跨频道队友不跟随进选择器（他们既不在本地场景里，
		// 也进不了同一支队伍流程）。拒绝原因记明，便于复测对日志。
		//
		// 2026-10-02 (V22) 修正日志口径：这里的真实判据是 `PeerInSameArea` ——
		// 它比的是城镇在线表的**四元桶键（town/area/channel/instance）**，不是频道号。
		// 以前无论真实原因是什么都写 `party_member_channel_mismatch` 且只打两个频道号，
		// 于是线上出现过 `source_channel=11 member_channel=11` 却写着"频道不符"的行
		// （真实原因是面积/实例桶不同，例如一方在每角色一份的私有落点房里）。
		// 现在按**真实差异**给 reason，并把两边的桶键并排打出来
		// （`townPresenceAreaText` 读的就是同一张表同一个键）。
		if !s.onlinePlayers.PeerInSameArea(initiator.selectedCharacterID, member.UserID) {
			if memberSession, online := s.onlineGameSession(member.UserID); online {
				sourceChannel := currentTownPresenceChannelID(initiator)
				memberChannel := currentTownPresenceChannelID(memberSession)
				reason := partyMemberAreaMismatchReason
				if sourceChannel != memberChannel {
					reason = partyMemberChannelMismatchReason
				}
				sourceArea, _ := s.townPresenceAreaText(initiator.selectedCharacterID)
				memberArea, _ := s.townPresenceAreaText(member.UserID)
				s.logGameEvent(initiator, "game-party-enter-select-member-skipped",
					"char_id", initiator.selectedCharacterID,
					"member_char_id", member.UserID,
					"party_id", state.PartyID,
					"source_channel", sourceChannel,
					"member_channel", memberChannel,
					"source_area", sourceArea,
					"member_area", memberArea,
					"reason", reason)
			}
			continue
		}
		follower, ok := s.onlineGameSession(member.UserID)
		if !ok || follower == initiator {
			continue
		}
		followerState := runtimePartyStateSnapshot(follower)
		if followerState.PartyID != state.PartyID || followerState.UserID != state.UserID {
			continue
		}
		fanoutCtx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		err := s.callGameSession(fanoutCtx, follower, "runtime-party-enter-select", func() error {
			_, prepareErr := s.prepareRuntimePartyFollowerEnterSelect(
				follower,
				state.UserID,
				state.PartyID,
				source,
			)
			return prepareErr
		})
		cancel()
		if err != nil {
			return fmt.Errorf("synchronize party member %d dungeon selector: %w", member.UserID, err)
		}
	}
	return nil
}

// prepareRuntimePartyFollowerEnterSelect installs the complete native selector
// transition for a same-area ordinary-party member. The old 86JP client could
// enter this state without an op15 response, but current NoPack uses the op15
// success body to install the selectable dungeon page. The passive member must
// therefore receive that same successful selector context after its town actor
// is detached; fatigue/op27 alone leaves a blank, non-interactive world map.
func (s *Service) prepareRuntimePartyFollowerEnterSelect(
	follower *gameSession,
	leaderCharacterID uint16,
	partyID int,
	source string,
) (bool, error) {
	if follower == nil || follower.selectedCharacterID == 0 {
		return false, nil
	}
	if allowed, reason := s.currentRaidSelectorOpenAdmission(follower); !allowed {
		s.logGameEvent(follower, "game-raid-selector-follower-rejected", "source", source, "reason", reason)
		return false, nil
	}
	if follower.enterSelectDungeonContextSent && follower.enterSelectDungeonSent {
		return true, nil
	}
	ready, reason := s.currentTownEnterSelectReady(follower)
	if !ready {
		s.logGameEvent(follower, "game-party-enter-select-follower-deferred",
			"source", source,
			"leader_char_id", leaderCharacterID,
			"party_id", partyID,
			"reason", reason)
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if partySelectorPreserveActorEnabled() {
		return s.prepareRuntimePartyFollowerSelectorWithTownActor(ctx, follower, leaderCharacterID, partyID, source)
	}
	charID, _, character, hasCharacter := s.selectedCharacterForEnter(ctx, follower)
	if charID == 0 {
		charID = follower.selectedCharacterID
	}
	follower.townMu.Lock()
	townPosition := follower.townPositionSnapshot
	follower.townMu.Unlock()
	areaState := byte(currentActorDefaultUserStateBits)
	if hasCharacter {
		if value := numericCharacterStatValue(character, "area_state"); value >= 0 && value <= 0xff {
			areaState = byte(value)
		}
	}
	areaBody := buildCurrentTownUserAreaNotificationBody(
		currentSceneActorObjectKey(charID),
		townPosition.TownID,
		0xff,
		townPosition.PositionX,
		townPosition.PositionY,
		townPosition.MovementCode,
		areaState,
	)
	if err := s.sendCurrentSceneFixedClass0Packet(
		follower,
		currentTownUserAreaNotificationMsgID,
		areaBody,
		source+"_passive_party_area_detach",
	); err != nil {
		return false, err
	}
	if err := s.sendCurrentPreDungeonContextPlayerState(follower, source+"_after_area_detach"); err != nil {
		return false, err
	}
	// 进本选择页这一条（厂商 `BuildEnterSelectDungeonPrefix` 的同名边界）：本会话此刻
	// 还在城镇、**没有任何副本运行时**，所以 all 档在这里按设计回落到只发自己
	// （reason=no_party_dungeon_instance_for_this_run）。口径与取源链见
	// dungeon_party_user_state_members.go。
	userStateBody, err := s.currentDungeonUserStateBody(
		follower,
		dungeonRuntimePointerForUserState(follower),
		currentSceneActorObjectKey(charID),
		source+"_follower_enter_select",
	)
	if err != nil {
		return false, err
	}
	if err := s.sendGameUpperRawClass(follower, uint16(dnfenum.CmdPacketExit), userStateBody, 0); err != nil {
		return false, err
	}
	partyState := runtimePartyStateSnapshot(follower)
	if partyState.PartyID != partyID || partyState.UserID != leaderCharacterID {
		return false, nil
	}
	// mode0/mode1/op3 replace the follower's scene actor owner. Reinstall the
	// authoritative op9 party roster before opening the selector, while keeping
	// the already-established dynamic UDP peer link intact (no repeated op11).
	// 相位收口开关（默认 current = 逐字节今天）见
	// sendDungeonEntryFollowerConversionRoster / partyDungeonEntryFollowerFrameEnv。
	if err := s.sendDungeonEntryFollowerConversionRoster(
		follower,
		partyState,
		source+"_after_passive_actor_rebuild",
	); err != nil {
		return false, err
	}
	if err := s.sendGameUpperSuccess(
		follower,
		uint16(dnfenum.CmdPacketEnterSelectDungeon),
		buildEnterSelectDungeonAckBody(),
	); err != nil {
		return false, err
	}
	fatigueBody := buildCurrentFatigueBody(character, hasCharacter)
	if err := s.sendGameUpperRawClass(follower, currentFatigueMsgID, fatigueBody, 0); err != nil {
		return false, err
	}
	if err := s.sendCurrentDungeonPermissionSnapshot(ctx, follower, source+"_after_fatigue"); err != nil {
		return false, err
	}
	if !follower.enterSelectDungeonContextSent {
		if err := s.sendCurrentTownEnterSelectContext(follower, source+"_after_fatigue"); err != nil {
			return false, err
		}
	}
	if !follower.enterSelectDungeonContextSent {
		return false, nil
	}
	follower.enterSelectDungeonSent = true
	follower.enterSelectDungeonAckSent = true
	s.logGameEvent(follower, "game-party-enter-select-follower-synchronized",
		"source", source,
		"leader_char_id", leaderCharacterID,
		"char_id", charID,
		"party_id", partyID,
		"sequence", "leader_selector_then_follower_op23_area_ff_op2_mode0_mode1_op3_op9_roster_without_op11_rehandshake_class1_op15_success_op36_optional_op5_op27")
	return true, nil
}

// partyDungeonEntryFollowerFrameEnv 按**相位**收口"队员被带进选择器"这一批里的那条
// class0/op9 kind0 名册帧（`prepareRuntimePartyFollowerEnterSelect` 的
// `…_after_passive_actor_rebuild` 那一次 `sendRuntimePartyRosterLocal`，序列自述见上面
// `game-party-enter-select-follower-synchronized` 的
// `…area_ff_op2_mode0_mode1_op3_op9_roster_without_op11_rehandshake_class1_op15_success…`）。
//
// # 为什么它是单变量阶梯上的下一刀（H2）
//
// 现场（2026-09-27，组队进图后**队员**客户端自己退队）：
//
//	RECV cmd=1 type=13 ENUM_CMDPACKET_LEAVE_PARTY body_len=0
//	EVENT game-party-membership-removed source=client_leave_party_op13 char_id=4
//	      party_id_before=1 member_count_before=2 → member_count_after=1
//
// 已排除：op9 的 owner 档、抑制 msg 11+153 / 37+30+19 / 3、跳过选择器、队友坐标 off、
// 以及 **H1**（`DNFBRIDGE_PARTY_DUNGEON_ENTRY_SLOT_TABLE=vendor_only` 实机仍退队 1 次）。
// 而"抑制全部 msg 9 ⇒ 零新增"仍成立 ⇒ 触发点是一条 class0/op9。
//
// 逐包核对（本仓夹具实测）后，队员在进图全程会收到**自己 actor 的三条** kind0 槽位表：
//
//	① 本函数这一条：跟随者选择器转换（op2 mode0 → mode1 → op3 → **op9**），
//	   位于 op28/op29 **之前** —— 队长没有这一条；
//	② start-map 提交尾段的厂商形状房间槽位表；
//	③ 落位里那条名册帧（H1 已把它作为**重复对**收口，实机证明不是触发点）。
//
// ⇒ 剩下的头号嫌疑就是 ①：它是"读条窗口里对同一个 actor 第三次声明队伍槽位表"的那一条，
// 而且相位最靠前（客户端刚被从城镇摘掉、actor 正在重建）。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_ENTRY_FOLLOWER_FRAME`
//	  - `current`（默认，零值）= 今天的行为：op153 实时信息 + 自己那条 op9 名册帧都发
//	    （逐字节、逐包序不变）；
//	  - `skip` = 只让位那条 op9：op153 实时信息**照发**（队伍框血条/槽位/小地图要它），
//	    并记 `game-party-dungeon-entry-follower-frame-skipped`。
//
// 安全阀：只有"权威队伍确实是同一支、且 ≥2 人"时才让位；单人队伍、队伍快照与本次要进的
// 队伍不一致（`prepareRuntimePartyFollowerEnterSelect` 上面已查 party_id 与队长）时
// 一律照发。⚠️ 残余风险（诚实标注）：队员被带进选择器后**取消**（不选图）时，它自己那条
// op9 名册帧在本次相位里就没有了，要等下一次队伍事件/进图相位才会重新投影；op153 仍在。
//
// 回退：把开关设回 `current`（或删掉该行）重启即可逐字节回到今天。
const partyDungeonEntryFollowerFrameEnv = "DNFBRIDGE_PARTY_DUNGEON_ENTRY_FOLLOWER_FRAME"

const (
	partyDungeonEntryFollowerFrameValueCurrent = "current"
	partyDungeonEntryFollowerFrameValueSkip    = "skip"
)

// partyDungeonEntryFollowerFrameMode 是 partyDungeonEntryFollowerFrameEnv 的归一化取值。
type partyDungeonEntryFollowerFrameMode int

const (
	// partyDungeonEntryFollowerFrameCurrent 是默认档（零值）= 今天的行为。
	partyDungeonEntryFollowerFrameCurrent partyDungeonEntryFollowerFrameMode = iota
	// partyDungeonEntryFollowerFrameSkip = 让位转换相位那条 op9（op153 照发）。
	partyDungeonEntryFollowerFrameSkip
)

func (mode partyDungeonEntryFollowerFrameMode) String() string {
	if mode == partyDungeonEntryFollowerFrameSkip {
		return partyDungeonEntryFollowerFrameValueSkip
	}
	return partyDungeonEntryFollowerFrameValueCurrent
}

// skipsRosterFrame 报告该档位是否让位转换相位那条 op9 名册帧。
func (mode partyDungeonEntryFollowerFrameMode) skipsRosterFrame() bool {
	return mode == partyDungeonEntryFollowerFrameSkip
}

// readPartyDungeonEntryFollowerFrameMode 解析 DNFBRIDGE_PARTY_DUNGEON_ENTRY_FOLLOWER_FRAME。
//
// **默认 skip（已现场验证的修复档）**：空/未设置/非法值都是 skip。
// 依据（2026-09-27 实机单变量，完整流程：组队→两人出房门→队长开选择器→队长选图 dungeon 3）：
//   - `skip`    ⇒ `client_leave_party_op13` **0 次**、`dungeon-runtime-prepared` 2（双方照常进图）；
//   - `current` ⇒ 每轮 **1 次**退队，服务端队伍降成单人，之后整队进图/队友端点/房间槽位表/
//     队友 actor 投影全部退化为单人（操作者报的"副本内不同步"上游）。
//
// 回退：取 `current` / `legacy` / `off` / `0` / `false` / `no` 逐字节回到改动前。
func readPartyDungeonEntryFollowerFrameMode(value string) partyDungeonEntryFollowerFrameMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case partyDungeonEntryFollowerFrameValueCurrent, "legacy", "off", "0", "false", "no":
		return partyDungeonEntryFollowerFrameCurrent
	default:
		return partyDungeonEntryFollowerFrameSkip
	}
}

// partyDungeonEntryFollowerFrame 返回当前 Service 的归一化档位（零值 options = current）。
func (s *Service) partyDungeonEntryFollowerFrame() partyDungeonEntryFollowerFrameMode {
	if s == nil {
		return partyDungeonEntryFollowerFrameCurrent
	}
	return s.options.partyDungeonEntryFollowerFrame
}

// sendDungeonEntryFollowerConversionRoster 在"队员被带进选择器"这一批里下发队伍名册。
//
// 默认档（current）逐字节等于改动前：op153 实时信息 + 自己那条 op9 名册帧
// （= `sendRuntimePartyRosterLocal`）。`skip` 档在安全阀满足时只让位 op9，op153 照发。
func (s *Service) sendDungeonEntryFollowerConversionRoster(
	session *gameSession,
	state alignedcmd.PartyState,
	source string,
) error {
	if !s.partyDungeonEntryFollowerFrame().skipsRosterFrame() {
		return s.sendRuntimePartyRosterLocal(session, state, source)
	}
	members := runtimePartyMembers(state)
	if session == nil || state.PartyID <= 0 || len(members) < 2 {
		// 安全阀：没有"同一支队伍且 ≥2 人"这条证据时绝不让位（单人队伍仍按今天发）。
		return s.sendRuntimePartyRosterLocal(session, state, source)
	}
	if err := s.sendRuntimePartyRealtimeInfoLocal(session, state); err != nil {
		return err
	}
	s.logGameEvent(session, "game-party-dungeon-entry-follower-frame-skipped",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"party_id", state.PartyID,
		"leader_char_id", state.UserID,
		"member_count", len(members),
		"msg_id", uint16(dnfenum.CmdPacketRecoverStamina),
		"switch", partyDungeonEntryFollowerFrameEnv,
		"mode", s.partyDungeonEntryFollowerFrame().String(),
		"phase", "follower_selector_conversion_before_op28_op29",
		"reason", "the_same_actor_receives_three_kind0_slot_tables_before_it_enters",
		"kept", "op153_realtime_info_still_sent")
	return nil
}
