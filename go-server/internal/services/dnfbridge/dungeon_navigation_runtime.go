package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentSelectDungeonEntryLevelTooLowReason byte = 14

// 进图前置校验失败必须**回包**：旧实现只在等级不足档回拒绝包，其余失败
// （疲劳不足 / 疲劳不可知 / 队伍超限 / 其它）只记一条
// `game-dungeon-select-blocked reason=dungeon_owner_validation_failed` 然后
// `return nil`。客户端在选图界面等 op16 应答，现场症状就是「开始冒险没反应」。
//
// 拒绝码取值来源：本仓只读引用 A21 参考实现
// `Server/DfoServer/Network/Builders/Dungeon/DungeonAdmissionRejectBuilder.cs:16-41`
// 的 `ResolveErrorCode`（包体形状与本仓已上线的 `{0,reason,0}` 同形）：
//
//	InsufficientFatigue                         -> 0x16
//	MemberEntryLimitReached / MissingPermission  -> 0xAD
//	InvalidSelectionState                        -> 0x13
//	Unknown / DungeonUnavailable（厂商 default）  -> 0x01
//
// `ErrEntryFatigueUnknown` 在厂商表里没有独立项：它表示"服务端拿不到疲劳值、
// 因此无法证明可以进图"的状态类失败，按 A21 对 `EntryCostFailureKind.InvalidState`
// 的处理（`Network/Handlers/Dungeon/DungeonEntryHandler.cs:4454-4457` ->
// `DungeonAdmissionReject.InvalidSelectionState`）取 0x13。
// 等级不足档 14 是本仓既有已上线取值，保持不变；`{0,reason,0}` 的第三个字节
// （A21 里的 member slot context）沿用本仓既有写法的 0，不引入未证明的槽位语义。
const (
	currentSelectDungeonInsufficientFatigueReason   byte = 0x16
	currentSelectDungeonMemberEntryLimitReason      byte = 0xad
	currentSelectDungeonInvalidSelectionStateReason byte = 0x13
	// currentSelectDungeonMissingRequiredItemReason 是厂商 `DungeonAdmissionRejectBuilder.cs:16-41`
	// 里 MissingRequiredItem 的取值：PVF `[required item]` 没带够时客户端**据此渲染系统聊天栏
	// 那一行**（"缺少 XXX"）。此前本仓没有这一行 case，于是落到 default 0x01（DungeonUnavailable）——
	// 玩家被拦住却看不到任何提示，正是"黄龙大会缺入选资格、聊天框系统栏没弹东西"的成因。
	currentSelectDungeonMissingRequiredItemReason byte = 0x11
)

// dungeonEntryRequiredItemReasonEnv 选择"PVF [required item] 不足"回哪个原因码。
//
//	""/"missing"（默认 = 修复档）：回 0x11（厂商 MissingRequiredItem，客户端会渲染系统栏提示）。
//	"unavailable"（回退档）：回 0x01（改动前逐字节相同，被拦但无提示）。
const dungeonEntryRequiredItemReasonEnv = "DNFBRIDGE_DUNGEON_ENTRY_REQUIRED_ITEM_REASON"

// dungeonEntryRequiredItemReasonUnavailableValue 是回退档取值。
const dungeonEntryRequiredItemReasonUnavailableValue = "unavailable"

// dungeonEntryRequiredItemReasonFixed 报告当前是否处于修复档（默认）。
func dungeonEntryRequiredItemReasonFixed() bool {
	return !strings.EqualFold(
		strings.TrimSpace(os.Getenv(dungeonEntryRequiredItemReasonEnv)),
		dungeonEntryRequiredItemReasonUnavailableValue,
	)
}

func (s *Service) handleDungeonSelectUpper(session *gameSession, body []byte) error {
	if session != nil && isPVPChannel(session.channel) {
		return nil
	}
	return s.handleDungeonSelectUpperPlanned(session, body, nil, true)
}

// dispatchRoutedPartyEnterAsLeader 把队员转交过来的 op16 异步交给队长的事件循环执行。
//
// 不能同步调用：调用方正跑在队员自己的事件循环上，而队长侧的进图准备会通过
// callGameSession 反过来向队员的事件循环 SubmitBlocking，同步就会互等到超时
// （线上实测 error="prepare party member 4 selector: context deadline exceeded"）。
// 异步投递后：队员立刻返回 → 它的事件循环空闲 → 队长侧回调队员时能立刻执行。
func (s *Service) dispatchRoutedPartyEnterAsLeader(leaderSession, followerSession *gameSession, body []byte) {
	if s == nil || leaderSession == nil {
		return
	}
	leader := leaderSession
	followerCharacterID := selectedCharacterID(followerSession)
	payload := append([]byte(nil), body...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		err := s.callGameSession(ctx, leader, "runtime-party-routed-enter-select", func() error {
			return s.handleDungeonSelectUpperPlanned(leader, payload, nil, true)
		})
		if err != nil {
			s.logInfo("dnfbridge routed party dungeon entry deferred",
				"conn_id", leader.connID,
				"leader_char_id", leader.selectedCharacterID,
				"follower_char_id", followerCharacterID,
				"error", err)
			return
		}
		s.logPacketEvent("game-party-dungeon-entry-routed-async-committed",
			"conn_id", leader.connID,
			"leader_char_id", leader.selectedCharacterID,
			"follower_char_id", followerCharacterID,
			"body_len", len(payload))
	}()
}

// isNonLeaderPartyMember reports whether characterID is a member of a
// multi-member party whose leader is somebody else. Only such a session must
// not be allowed to commit a party dungeon entry of its own.
func isNonLeaderPartyMember(state alignedcmd.PartyState, characterID uint16) bool {
	if state.PartyID <= 0 || state.UserID == 0 || characterID == 0 {
		return false
	}
	if state.UserID == characterID {
		return false
	}
	return len(runtimePartyMembers(state)) > 1
}

type runtimePartyDungeonEntryPlan struct {
	randomizedObjects *currentDungeonRandomizedOwner
	dungeonID         int64
	mazeIndex         int
	storyQuestID      int64
	maps              map[worldmap.RoomCoordinate]int64
	room              worldmap.RoomCoordinate
	mapID             int64
	boss              worldmap.RoomCoordinate
	seed              uint32
}

type runtimePartyPreparedDungeonEntry struct {
	session   *gameSession
	runtime   *runtimeDungeonState
	scene     worldmap.DungeonRoomScene
	plan      runtimePartyDungeonEntryPlan
	committed bool
	// request 是队长那次 op16 的原始请求（队员复用同一份，见
	// applyRuntimePartyDungeonEntryPlan）。barrier 放行后重新进入 op28 之后的尾段时要用它，
	// 所以在这里留一份，不依赖调用方再传。
	request dungeoncmd.SelectDungeonRequest
	// op28Staged 表示这条队员记录已经走完 barrier 之前的全部动作（…→op28），
	// 只剩下 op28 之后的部分等队长统一放行（见 party_dungeon_entry_barrier.go）。
	op28Staged bool
}

func (s *Service) handleDungeonSelectUpperPlanned(
	session *gameSession,
	body []byte,
	prepared *runtimePartyPreparedDungeonEntry,
	fanoutParty bool,
) error {
	return s.handleDungeonSelectUpperPlannedForAdmission(session, body, prepared, fanoutParty, false)
}

// A claimed automatic encounter bypasses only its own admission interception.
// Keep the public ordinary-party selector contract and all scene/asset gates.
func (s *Service) handleDungeonSelectUpperPlannedAdmitted(session *gameSession, body []byte, prepared *runtimePartyPreparedDungeonEntry, fanoutParty bool) error {
	return s.handleDungeonSelectUpperPlannedForAdmission(session, body, prepared, fanoutParty, true)
}

func (s *Service) handleDungeonSelectUpperPlannedForAdmission(session *gameSession, body []byte, prepared *runtimePartyPreparedDungeonEntry, fanoutParty, admitted bool) error {
	var fiendWarSelectionDiagnostic *currentFiendWarAdmissionDiagnostic
	if !admitted && prepared == nil && isCurrentFiendWarChannel(session) {
		diagnostic := newCurrentFiendWarAdmissionDiagnostic(session, 0)
		if state, ok := s.runtimeRaidForMember(session.selectedCharacterID); ok {
			diagnostic.captureRaid(state)
		}
		diagnostic.reason = "op16_received"
		fiendWarSelectionDiagnostic = &diagnostic
		defer func() {
			if diagnostic.reason == "" {
				diagnostic.setReason("op16_unclassified")
			}
			s.logCurrentFiendWarAdmissionDiagnostic(diagnostic)
		}()
	}
	// barrier 放行后的重入：这条队员记录在第一次调用里已经把 …→op28 全部发完
	// （`op28Staged`），这里只补 op28 之后的尾段。见 party_dungeon_entry_barrier.go。
	if prepared != nil && prepared.op28Staged {
		return s.sendPartyDungeonEntryTailAfterOp28(
			session,
			prepared.runtime,
			prepared.scene,
			prepared,
			prepared.request,
			nil,
			nil,
			alignedcmd.PartyState{},
			false,
		)
	}
	if len(body) != dungeoncmd.SelectDungeonRequestSize {
		if fiendWarSelectionDiagnostic != nil {
			fiendWarSelectionDiagnostic.setReason("op16_body_size_mismatch")
		}
		s.logGameEvent(session, "game-dungeon-select-blocked",
			"body_len", len(body),
			"expected_body_len", dungeoncmd.SelectDungeonRequestSize,
			"reason", "current_exe_op16_request_boundary_mismatch")
		return nil
	}
	request, err := dungeoncmd.DecodeSelectDungeonRequest(body)
	if err != nil {
		if fiendWarSelectionDiagnostic != nil {
			fiendWarSelectionDiagnostic.setReason("op16_decode_failed")
		}
		s.logGameEvent(session, "game-dungeon-select-blocked",
			"body_len", len(body),
			"reason", "current_exe_op16_request_malformed",
			"error", err)
		return nil
	}
	if fiendWarSelectionDiagnostic != nil {
		fiendWarSelectionDiagnostic.requestDungeon = uint32(request.DungeonID)
		fiendWarSelectionDiagnostic.setReason("op16_decoded")
	}
	request.DungeonID = s.resolveCurrentSingleAntonSelector(session, request.DungeonID)
	request.DungeonID, err = s.resolveCurrentDespairSelection(session, request.DungeonID)
	if err != nil {
		return s.refuseDungeonSelectEntryValidation(session, request, err)
	}
	request.DungeonID, err = s.resolveCurrentGriefSelection(session, request.DungeonID)
	if err != nil {
		return s.refuseDungeonSelectEntryValidation(session, request, err)
	}
	if !s.currentPreparedDungeonSelectionAllowed(session, uint32(request.DungeonID), prepared) {
		if fiendWarSelectionDiagnostic != nil {
			fiendWarSelectionDiagnostic.setReason("pre_admission_selection_rejected")
		}
		s.logGameEvent(session, "game-raid-dungeon-selection-rejected", "dungeon_id", request.DungeonID)
		return s.sendGameUpperRawClass(session, uint16(dnfenum.GameTypeSelectDungeon), []byte{0, currentSelectDungeonInvalidSelectionStateReason, 0}, 1)
	}
	// 队伍判据取**权威**名单（中央 RuntimePartyManager 按角色），不是会话本地缓存：
	// 选图那次 op16 可能落在同角色的另一条连接上（辅助/替换 socket），
	// 那条连接永远收不到 publish，只读缓存就会在队伍成立的情况下退化成单人。
	// 见 party_manager_runtime.go 的 runtimePartyStateForSession。
	partyStateAtRequest := s.runtimePartyStateForSession(session)
	if !admitted && prepared == nil {
		if handled, err := s.interceptCurrentFiendWarNormalSelectionWithDiagnostic(session, uint32(request.DungeonID), fiendWarSelectionDiagnostic); handled {
			return err
		}
	}
	partyMemberIndex, partyMemberIndexed := runtimePartyMemberIndex(session.selectedCharacterID, partyStateAtRequest)
	s.logGameEvent(session, "game-dungeon-select-request-decoded",
		"body_len", len(body),
		"dungeon_id", request.DungeonID,
		"difficulty", request.Difficulty,
		"entry_option", request.EntryOption,
		"selection_mode", request.SelectionMode,
		"runtime_state", request.RuntimeState,
		"runtime_token", request.RuntimeToken,
		"reserved", request.Reserved,
		"party_state", request.PartyState,
		"offset16_unproven", request.LeaderObjectKey,
		"special_mode", request.SpecialMode,
		"source", "current_exe_sub_342EA40_fixed_21_byte_writer")
	// 组队进入选择器/进图的现场快照：无论后面走哪条分支都先记一条，事后单看日志
	// 就能判断"这个 op16 是谁发的、服务端认为谁是队长、当时是否在队伍里"。
	// 另外把"这条 op16 是不是落在角色目录那条会话上"一并打出来：现场 2026-09-26 HK
	// 的 `party_id=0` 正是"选图落在同角色的另一条连接上"这一类形状，这两个字段让下一次
	// 复测**单行**就能判定（`session_is_character_directory_owner=false` ⇒ 就是它）。
	directoryOwnerIdentity, directoryOwnerCurrent := s.onlineGameSessionCharacterSnapshot(session.selectedCharacterID)
	directoryOwnerIsSelf := directoryOwnerCurrent && directoryOwnerIdentity.session == session
	var directoryOwnerConnID string
	var directoryOwnerGeneration uint64
	if directoryOwnerIdentity.session != nil {
		directoryOwnerConnID = directoryOwnerIdentity.session.connID
		directoryOwnerGeneration = directoryOwnerIdentity.generation
	}
	s.logGameEvent(session, "game-dungeon-select-party-context",
		"char_id", session.selectedCharacterID,
		"party_id", partyStateAtRequest.PartyID,
		"party_leader_id", partyStateAtRequest.UserID,
		"party_member_count", len(runtimePartyMembers(partyStateAtRequest)),
		"is_leader", partyStateAtRequest.UserID == session.selectedCharacterID,
		"fanout", fanoutParty,
		"leader_driven", prepared != nil,
		"session_generation", session.characterGeneration,
		"session_is_character_directory_owner", directoryOwnerIsSelf,
		"directory_owner_conn_id", directoryOwnerConnID,
		"directory_owner_generation", directoryOwnerGeneration,
		"enter_select_sent", session.enterSelectDungeonSent,
		"enter_select_ack_sent", session.enterSelectDungeonAckSent,
		"enter_select_context_sent", session.enterSelectDungeonContextSent,
		"dungeon_id", request.DungeonID,
		"difficulty", request.Difficulty)
	// 深渊派对（hell party）进图前置条件：`[hell dungeon]` / `[hell quest]` 来自
	// worldmap area（本仓已解析），未完成就必须**回拒绝包**而不是静默丢弃 ——
	// 静默丢弃就是操作者报的"卡在地下城选择界面"。见 dungeon_hell_party_entry.go。
	//
	// `prepared == nil` 才代表**这一条会话自己的客户端**发了这次 op16：队长带队友进图时
	// 服务端会把队长那条 op16 内部扇出给每个队友会话（prepared != nil），那些会话不是
	// 发起者，只做逐槽位投影（`clientRequest=false`），否则一个没做完任务的队友会被当成
	// 发起者拒绝、把整队的进图序列从中间打断。
	if decision := s.evaluateHellPartyDungeonEntry(
		context.Background(), session, request, prepared == nil,
	); decision.Intercepted {
		return s.refuseHellPartyDungeonEntry(session, request, decision)
	}
	if err := s.commitPendingDungeonReturnForSceneRequest(
		session,
		"current_exe_op16_after_pending_town_transition",
	); err != nil {
		s.logGameEvent(session, "game-dungeon-select-blocked",
			"dungeon_id", request.DungeonID,
			"difficulty", request.Difficulty,
			"reason", "pending_town_transition_commit_failed",
			"error", err)
		return nil
	}
	if prepared == nil {
		if handled, err := s.replayDungeonSelectAckForActiveRuntime(session, request, len(body)); handled || err != nil {
			return err
		}
	}
	// 队伍里只有一个进图 owner：队长。但客户端给队员的选择器页面同样是"可交互"
	// 的（我们为了避免队员看到空白地图，必须下发同一份 op15 选择器上下文），
	// 所以队员按 space 也会发 op16（runtime_token=0xFFFF + 队长的 object key）。
	// 这种请求不能自己建副本（旧实现会把队伍拆散、队员客户端闪退），但也不能
	// 直接丢掉——否则玩家看到的就是"开始冒险没反应"。
	// 处理方式：先给队员回它自己那次 op16 的 ACK，然后把这份选择转交给队长去执行，
	// 由队长走正常流程把整队带进副本。
	if prepared == nil && fanoutParty && isNonLeaderPartyMember(partyStateAtRequest, session.selectedCharacterID) {
		leaderSession, leaderOnline := s.onlineGameSession(partyStateAtRequest.UserID)
		if !leaderOnline || leaderSession == nil || leaderSession == session {
			s.logGameEvent(session, "game-party-follower-select-deferred",
				"char_id", session.selectedCharacterID,
				"party_id", partyStateAtRequest.PartyID,
				"leader_char_id", partyStateAtRequest.UserID,
				"dungeon_id", request.DungeonID,
				"difficulty", request.Difficulty,
				"runtime_token", request.RuntimeToken,
				"leader_object_key", request.LeaderObjectKey,
				"reason", "leader_session_unavailable_for_routed_entry")
			return nil
		}
		selectAckBody := buildSelectDungeonBodyForCharacter(session.selectedCharacterID)
		if err := s.sendGameUpperSuccess(
			session,
			uint16(dnfenum.CmdPacketSelectDungeon),
			selectAckBody,
		); err != nil {
			return err
		}
		s.logGameEvent(session, "game-party-follower-select-routed-to-leader",
			"char_id", session.selectedCharacterID,
			"party_id", partyStateAtRequest.PartyID,
			"leader_char_id", partyStateAtRequest.UserID,
			"dungeon_id", request.DungeonID,
			"difficulty", request.Difficulty,
			"runtime_token", request.RuntimeToken,
			"leader_object_key", request.LeaderObjectKey,
			"reason", "follower_pressed_enter_party_entry_runs_as_leader")
		// 这份请求正跑在**队员自己的事件循环**上，而队长侧的进图准备会反过来调用队员会话
		// （prepareRuntimePartyDungeonEntry -> synchronizeRuntimePartyMembersEnterSelect
		// -> callGameSession(队员) 的 SubmitBlocking）。同步等下去就是两个事件循环互等，
		// 直到 createWriteTimeout 到期。线上证据（2026-09-24 双开实测）：
		//   game-party-follower-select-routed-to-leader char_id=4
		//   -> game-party-dungeon-entry-member-prepared char_id=4
		//   -> game-party-dungeon-entry-blocked char_id=3 error="prepare party member 4
		//      selector: context deadline exceeded"
		// 玩家看到的就是"开始冒险没反应"，而且队员侧已经收到半套选择器包
		// （区域脱离/角色重建/op15），于是进图超时后 2P 的血槽 UI 也就错了。
		// 因此改成异步投递到队长的事件循环执行：队员立刻拿到自己的 op16 ACK，
		// 队长的事件循环空闲后跑正常进图流程，再回调队员时就不会互等。
		s.dispatchRoutedPartyEnterAsLeader(leaderSession, session, body)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if prepared == nil {
		if err := s.validateCurrentRiftPartyUnlock(ctx, session, partyStateAtRequest, int64(request.DungeonID)); err != nil {
			return s.refuseDungeonSelectEntryValidation(session, request, err)
		}
	}
	var runtime *runtimeDungeonState
	var scene worldmap.DungeonRoomScene
	if prepared != nil {
		if prepared.session != session || prepared.runtime == nil || prepared.runtime.Session == nil ||
			!sameSelectDungeonRequest(prepared.runtime.Request, request) {
			return fmt.Errorf("prepared party dungeon entry does not belong to the target session")
		}
		runtime, scene = prepared.runtime, prepared.scene
	} else {
		var err error
		runtime, scene, err = s.prepareDungeonRuntime(ctx, session, request)
		if err != nil {
			return s.refuseDungeonSelectEntryValidation(session, request, err)
		}
	}
	runtime.partyMemberIndex, runtime.partyMemberIndexed = partyMemberIndex, partyMemberIndexed
	var preparedFollowers []*runtimePartyPreparedDungeonEntry
	if prepared == nil && fanoutParty {
		var err error
		preparedFollowers, err = s.prepareRuntimePartyDungeonEntry(ctx, session, partyStateAtRequest, request, runtime, scene)
		if err != nil {
			s.logGameEvent(session, "game-party-dungeon-entry-blocked",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"difficulty", request.Difficulty,
				"reason", "party_entry_preflight_failed_before_any_member_commit",
				"error", err)
			return nil
		}
	}
	if prepared == nil && runtime.guildDungeonClear == nil {
		if err := s.freezeCurrentGuildDungeonGroup(ctx, session, runtime, preparedFollowers); err != nil {
			return s.refuseDungeonSelectEntryValidation(session, request, err)
		}
	} else if err := validatePreparedGuildDungeon(runtime); err != nil {
		return s.refuseDungeonSelectEntryValidation(session, request, err)
	}
	if err := s.prepareCurrentRaidDungeonEntry(session, runtime, prepared, preparedFollowers); err != nil {
		return s.refuseDungeonSelectEntryValidation(session, request, err)
	}
	defer func() {
		session.dungeon.mu.Lock()
		committed := session.dungeon.runtime == runtime
		session.dungeon.mu.Unlock()
		if !committed {
			s.cancelUncommittedLukeEntry(runtime)
			s.cancelUncommittedCurrentFiendWarEntry(runtime)
		}
	}()
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		// 返回城镇原点不可用 = 这次选择的会话状态不成立（厂商 InvalidSelectionState）。
		return s.refuseDungeonSelectEntry(session, request,
			"ordinary_dungeon_town_return_origin_unavailable",
			"entry_town_return_origin_unavailable",
			currentSelectDungeonInvalidSelectionStateReason,
			err,
			"maze_index", runtime.MazeIndex)
	}
	if !currentTownSelectorOriginBound(session) {
		// 会话里没有 op35 快照，这次是用角色记录里的持久化位置（town_id/area_id/pos_x/pos_y）
		// 落回的返回城镇原点。留一条正向日志，复测时能直接看到"为什么以前会卡在选择地图界面、
		// 这次为什么没卡"。
		s.logGameEvent(session, "game-dungeon-select-town-return-origin-from-character-stats",
			"char_id", session.selectedCharacterID,
			"dungeon_id", request.DungeonID,
			"maze_index", runtime.MazeIndex,
			"town_id", runtime.townReturnOrigin.TownID,
			"area_id", runtime.townReturnOrigin.AreaID,
			"position_x", runtime.townReturnOrigin.PositionX,
			"position_y", runtime.townReturnOrigin.PositionY)
	}
	if _, err := s.buildCurrentDungeonEntryPackets(runtime, scene); err != nil {
		// 请求模式不受支持 = InvalidSelectionState；其余（迷宫/首领/随机对象等
		// 内容前置）按厂商 default 归 DungeonUnavailable。
		reason, class := currentSelectDungeonUnavailableReason, "entry_preflight_unavailable"
		if errors.Is(err, errDungeonEntryModeUnsupported) {
			reason, class = currentSelectDungeonInvalidSelectionStateReason, "entry_mode_unsupported"
		}
		return s.refuseDungeonSelectEntry(session, request,
			"dungeon_entry_packet_preflight_failed",
			class,
			reason,
			err,
			"maze_index", runtime.MazeIndex)
	}
	if prepared == nil {
		if err := s.consumeCurrentHellPartyGroupEntry(ctx, session, runtime, preparedFollowers); err != nil {
			return s.refuseDungeonSelectEntryValidation(session, request, err)
		}
	}
	transferCommitted := false
	if prepared == nil && runtime.antonHatcheryTransfer != nil {
		if err := s.commitCurrentAntonHatcheryTransferGroup(session, runtime, preparedFollowers); err != nil {
			return s.refuseDungeonSelectEntryValidation(session, request, err)
		}
		transferCommitted = true
	}
	if prepared == nil && len(preparedFollowers) > 0 && !transferCommitted {
		if err := s.commitRuntimePartyPreparedFollowers(preparedFollowers); err != nil {
			s.logGameEvent(session, "game-party-dungeon-entry-blocked",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"difficulty", request.Difficulty,
				"reason", "party_follower_runtime_commit_failed_before_leader_commit",
				"error", err)
			return nil
		}
	}
	if !transferCommitted && (prepared == nil || !prepared.committed) {
		if err := s.commitDungeonRuntime(session, runtime); err != nil {
			s.rollbackRuntimePartyPreparedFollowers(preparedFollowers)
			if errors.Is(err, dnfdungeon.ErrDailyEntryLimit) || errors.Is(err, dnfdungeon.ErrDayDungeonClosed) || errors.Is(err, dnfdungeon.ErrEntryItemConsumeInsufficient) {
				return s.refuseDungeonSelectEntryValidation(session, request, err)
			}
			s.logGameEvent(session, "game-dungeon-select-blocked",
				"dungeon_id", request.DungeonID,
				"maze_index", runtime.MazeIndex,
				"reason", "dungeon_runtime_commit_failed",
				"error", err)
			return nil
		}
	}
	s.commitCurrentFiendWarReservation(runtime)
	// Followers are installed before the leader can still reject admission.
	// Start their dungeon clocks only after the entire group has committed.
	s.activateCurrentPetGrowthDungeonClock(session, runtime)
	for _, member := range preparedFollowers {
		if member != nil && member.committed {
			s.activateCurrentPetGrowthDungeonClock(member.session, member.runtime)
		}
	}
	resetDungeonEntrySceneGates(session)
	if err := s.sendCurrentSceneOp9PreviewActorRemovalOnce(session, "select_dungeon_after_runtime_commit_before_op27"); err != nil {
		return err
	}

	if prepared == nil {
		selectAckBody := buildSelectDungeonBodyForCharacter(session.selectedCharacterID)
		s.logGameEvent(session, "game-dungeon-select-ack-send",
			"msg_id", uint16(dnfenum.CmdPacketSelectDungeon),
			"body_len", len(selectAckBody)+1,
			"compatibility_mode", selectAckBody[0],
			"compatibility_object_key", binary.LittleEndian.Uint16(selectAckBody[1:3]),
			"request_body_len", len(body),
			"source", "current_exe_sub_1CFC410_generic_success_byte_trailing_body_ignored",
			"reason", "stop_rejected_op16_request_echo")
		if err := s.sendGameUpperSuccess(
			session,
			uint16(dnfenum.CmdPacketSelectDungeon),
			selectAckBody,
		); err != nil {
			return err
		}
	} else {
		s.logGameEvent(session, "game-party-dungeon-select-follower-ack-suppressed",
			"msg_id", uint16(dnfenum.CmdPacketSelectDungeon),
			"request_body_len", len(body),
			"reason", "follower_did_not_submit_op16_and_receives_leader_owned_op28_op29_only")
	}
	if err := s.sendCurrentDungeonResourceState(session, runtime, "select_dungeon_after_op16_ack_before_op27"); err != nil {
		return err
	}
	if err := s.sendCurrentPreDungeonContextPlayerState(session, "select_dungeon_after_op5_before_op27"); err != nil {
		return err
	}
	// 厂商 packet_order ①（`…op2-mode1-all-op11-remotes-op3-all…`）：`op2` mode1 之后、
	// op27/op28 之前还有一条 **op11 remotes**（只含对端行的 22B 行表，body 与 formation
	// 那份逐字节相同）。形状复用同一个 builder，不新增包型。
	// 见 party_dungeon_entry_peer_endpoints.go。
	entryRemotesSent, err := s.sendDungeonEntryPeerEndpointRemotes(
		session,
		"select_dungeon_after_op2_mode1_before_op27",
	)
	if err != nil {
		return err
	}
	if !session.enterSelectDungeonContextSent {
		if err := s.sendCurrentDungeonContextOp27(session, "select_dungeon_after_pre_actor_binding"); err != nil {
			return err
		}
		session.enterSelectDungeonContextSent = true
	} else if s.hellBlockedSlotsPending(session) {
		// `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 且这次选图判出了 blocked 槽位、而进本选择页
		// 早在 op15 那一步就发过了（`enterSelectDungeonContextSent=true` ⇒ 下面这条
		// op27 本来会被跳过）：blocked 槽位是随**这一条** op27 下发的，不补发就等于
		// 客户端永远收不到。**只在 on 档 + blocked 非空时**多这一条；默认档与
		// "全队都合规"这条路径逐字节不变（既有钉死用例
		// `TestHandleDungeonSelectUpperSuccessPathStaysByteIdentical` 钉的正是后者）。
		if err := s.sendCurrentDungeonContextOp27(session, "select_dungeon_after_pre_actor_binding_hell_blocked_slots"); err != nil {
			return err
		}
	} else {
		s.logGameEvent(session, "game-upper-current-dungeon-context-op27-duplicate-skipped",
			"source", "select_dungeon_after_pre_actor_binding",
			"char_id", session.selectedCharacterID,
			"msg_id", currentDungeonContextMsgID,
			"reason", "town_enter_select_page_already_committed_before_real_op16")
	}
	entryPackets, err := s.buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		// 这里 `return nil` 会把整条进图序列停在 op27 之后：客户端读条到 90% 左右消失、
		// 进图看不到任何 actor（连自己的角色模型都没有）。留一条统一格式的失败日志，
		// 复测时一眼能分辨"是 op28/op29 打包失败"还是"op29 之后的落位步骤失败"。
		s.logDungeonEntryStepFailure(session, "build_entry_packets_op28_op29", prepared, err)
		s.logGameEvent(session, "game-upper-current-dungeon-info-op28-deferred",
			"source", "select_dungeon_after_op27",
			"char_id", session.selectedCharacterID,
			"msg_id", currentDungeonInfoNotification,
			"dungeon_id", runtime.Dungeon.ID,
			"reason", "typed_runtime_packet_plan_failed",
			"error", err)
		return nil
	}
	s.logGameEvent(session, "game-upper-current-dungeon-info-op28-send",
		"source", "select_dungeon_after_op27",
		"char_id", session.selectedCharacterID,
		"msg_id", currentDungeonInfoNotification,
		"classification", 0,
		"body_len", len(entryPackets.DungeonInfo),
		"dungeon_id", runtime.Dungeon.ID,
		"difficulty", runtime.Request.Difficulty,
		"entry_option", runtime.Request.EntryOption,
		"maze_index", runtime.MazeIndex,
		"boss", runtime.BossCoordinate.String(),
		"body_source", "current_exe_sub_1D440C0_real_pvf_runtime",
		"start_map", "deferred_until_op28_runtime_acceptance")
	if err := s.sendGameUpperRawClass(session, currentDungeonInfoNotification, entryPackets.DungeonInfo, 0); err != nil {
		s.logDungeonEntryStepFailure(session, "send_op28_dungeon_info", prepared, err)
		return err
	}
	// 与厂商同名的事件（`game-party-dungeon-entry-prepared`）：把本会话这一条进场序列的
	// 实际顺序串 / leader_slot / local_slot / release 记下来，并附上厂商自己写的
	// `vendor_packet_order` 作对照。**只写日志**，不改变任何包。
	s.logDungeonEntrySequence(
		session,
		partyMemberIndex,
		partyMemberIndexed,
		prepared != nil,
		partyStateAtRequest,
		entryRemotesSent,
		runtime.Dungeon.ID,
		scene.Map.Map.ID,
		len(body),
	)
	// 队员这条走到 op28 就是 barrier 集合点：op800/op29 及之后全部押后，等队长把
	// 所有成员都收上来再统一放行（厂商 `release=false` 的那一条）。见
	// party_dungeon_entry_barrier.go。
	if prepared != nil && s.partyDungeonEntryBarrier().holds() {
		prepared.op28Staged = true
		s.logGameEvent(session, "game-party-dungeon-entry-member-staged-at-op28",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"party_id", partyStateAtRequest.PartyID,
			"char_id", session.selectedCharacterID,
			"local_slot", partyMemberIndex,
			"dungeon_id", runtime.Dungeon.ID,
			"map_id", scene.Map.Map.ID,
			"barrier", "all_members_through_op28_before_any_op800_op29",
			"reason", "vendor_holds_op800_op29_until_every_member_reached_op28")
		return nil
	}
	// 厂商 `packet_order` 的分界点：`…-all-op26-leader-op28-then-all-member-barrier`。
	// `DNFBRIDGE_PARTY_DUNGEON_ENTRY_BARRIER=vendor`（默认）时，队长的 op29 及之后
	// 全部压到"所有成员都走到 op28"之后再统一放行。见 party_dungeon_entry_barrier.go。
	if s.partyDungeonEntryBarrier().holds() && prepared == nil && fanoutParty && len(preparedFollowers) > 0 {
		return s.releasePartyDungeonEntryAfterOp28Barrier(
			session,
			body,
			preparedFollowers,
			partyStateAtRequest,
			runtime,
			scene,
			request,
			entryRemotesSent,
		)
	}
	return s.sendPartyDungeonEntryTailAfterOp28(
		session,
		runtime,
		scene,
		prepared,
		request,
		body,
		preparedFollowers,
		partyStateAtRequest,
		fanoutParty,
	)
}

// sendPartyDungeonEntryTailAfterOp28 是进场序列 op28 之后的**全部**动作，从
// `game-party-dungeon-entry-prepared`（op28 之后那一条同名日志）一路做到
// `leaveTownPresenceForDungeon`：
//
//	op800 场景 actor 快照 → op29 start-map → actor announce/cache →
//	post-start-map 落位 → op36 fatigue → op9 房间槽位表 → 可选队员 fan-out
//
// 之所以单独拆出来：厂商的 barrier 语义要求"所有成员先走完 op28，再由最后一名成员
// 放行、统一下发 op800+op29"。队长与队员在 barrier 后都要从**同一点**继续，
// 所以尾段必须能被两方各自调用一次（见 releasePartyDungeonEntryAfterOp28Barrier）。
func (s *Service) sendPartyDungeonEntryTailAfterOp28(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	prepared *runtimePartyPreparedDungeonEntry,
	request dungeoncmd.SelectDungeonRequest,
	body []byte,
	preparedFollowers []*runtimePartyPreparedDungeonEntry,
	partyStateAtRequest alignedcmd.PartyState,
	fanoutParty bool,
) error {
	// 本次 start-map 提交开始：复位"本提交已经发过 kind0 槽位表"的标记，供进图落位里
	// 那条既有名册帧判位（DNFBRIDGE_PARTY_DUNGEON_ENTRY_SLOT_TABLE=vendor_only）。
	session.dungeonPartySlotTableSentOnStartMap = false
	// 厂商 packet_order 里的 `…-before-any-op800-op29`：barrier 放行后，每个成员
	// **各自**先收一条 class0/op800 actor 场景快照，再收 op29 本体。
	if err := s.sendPartyDungeonEntryActorSceneSnapshot(
		session,
		"select_dungeon_after_all_member_op28_barrier",
	); err != nil {
		s.logDungeonEntryStepFailure(session, "send_op800_actor_scene_snapshot", prepared, err)
		return err
	}
	// op28 已经由调用方发出（`game-upper-current-dungeon-info-op28-send`）。这里重新
	// 组装一次 op29 的 body：`buildCurrentDungeonEntryPackets` 是纯函数，输入只有
	// runtime+scene，barrier 前后两次调用结果一致。
	//
	// 柱子（map-owned 特殊被动对象）的注册表 / 预生成掉落行必须在**构建 body 之前**
	// 准备好：构建器只做纯读（`currentDungeonPassiveObjectDropEntries`）。两个开关都是
	// 默认档时 `prepareCurrentDungeonPassiveObjectDrops` 第一行返回，一个字节都不动。
	// 见 dungeon_passive_object_drop.go。
	if err := s.prepareCurrentDungeonPassiveObjectDrops(session, runtime, scene, "select_dungeon_after_op28"); err != nil {
		s.logDungeonEntryStepFailure(session, "prepare_passive_object_drop_rows", prepared, err)
		return fmt.Errorf("prepare dungeon passive-object drop rows before op29: %w", err)
	}
	entryPackets, err := s.buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		s.logDungeonEntryStepFailure(session, "build_entry_packets_after_op28_barrier", prepared, err)
		return fmt.Errorf("rebuild dungeon entry packets for the op28 barrier tail: %w", err)
	}
	roomSnapshot := runtime.Room.Snapshot()
	// 锦标赛：op28 之后补发 NOTI 386（对阵表 + 路径 actor 表）+ 387（房间格与种子）。
	// 默认档 / 非锦标赛地图 ⇒ 一个字节都不发（函数第一行返回）。
	// 进场这一次必然是本房间的首次投影 ⇒ revisit=false
	// （厂商 `DungeonMapHandler.cs:464/:490 isFirstRunStartMap = run.RoomStates.Count == 0`）。
	projectionErr := s.sendCurrentTournamentStartMapProjection(session, runtime, scene, false)
	if entryPackets.StartMapSuppressed {
		// 厂商在锦标赛地图上**根本不发** START_MAP（`DungeonMapHandler.cs:778-779` 不组装体、
		// `:943 if (!sendTournamentProjection)` 不发送），房间与 actor 全部由 386/387 承担。
		// ⇒ 这里必须**跳过 op29**。这不是"少发一段然后当成功"：替代公告要么已经发出，
		// 要么整次入场在此具名硬失败（386/387 是唯一公告者，没有它就等于没有 actor 公告）。
		if projectionErr != nil {
			s.logGameEvent(session, "game-dungeon-tournament-start-map-projection-failed",
				"char_id", session.selectedCharacterID,
				"dungeon_id", runtime.Dungeon.ID,
				"map_id", scene.Map.Map.ID,
				"reason", "tournament_map_has_no_ordinary_start_map_and_the_substitute_projection_failed",
				"suppression_reason", entryPackets.StartMapSuppressedReason,
				"error", projectionErr)
			s.logDungeonEntryStepFailure(session, "send_tournament_start_map_projection_instead_of_op29", prepared, projectionErr)
			return fmt.Errorf("send the tournament start-map projection that replaces op29: %w", projectionErr)
		}
		s.logGameEvent(session, "game-dungeon-tournament-start-map-suppressed",
			"source", "select_dungeon_after_op28",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"map_id", scene.Map.Map.ID,
			"msg_id", currentDungeonStartNotification,
			"suppressed_start_map_body_len", len(entryPackets.StartMap),
			"substitute_msg_id", a21NotiTournamentInfo,
			"substitute_map_info_msg_id", a21NotiTournamentMapInfo,
			"first_sequence", runtime.Tournament.boundFirstSequence(),
			"actor_count", len(roomSnapshot.Monsters)+len(roomSnapshot.ExtendedActors),
			"object_key_announcer", "tournament_info_path_actor_segment",
			"reason", entryPackets.StartMapSuppressedReason,
			"actor_ids_unchanged", "op39_runtime_object_key_space_is_the_same_first_sequence_space")
	} else {
		s.logGameEvent(session, "game-upper-current-dungeon-start-op29-send",
			"source", "select_dungeon_after_op28",
			"char_id", session.selectedCharacterID,
			"msg_id", currentDungeonStartNotification,
			"classification", 0,
			"body_len", len(entryPackets.StartMap),
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"seed", runtime.Seed,
			"actor_count", len(roomSnapshot.Monsters)+len(roomSnapshot.ExtendedActors),
			"body_source", "current_exe_sub_1D479A0_real_pvf_runtime")
		if err := s.sendGameUpperRawClass(session, currentDungeonStartNotification, entryPackets.StartMap, 0); err != nil {
			s.logDungeonEntryStepFailure(session, "send_op29_start_map", prepared, err)
			return err
		}
		// 深渊房间：START_MAP 之后补 NOTI 679（`DungeonMapHandler.cs:679-688`）。
		// 非深渊房间/`DNFBRIDGE_HELL_PARTY_MONSTER_INFO=legacy`/没有波次 ⇒ 一个字节都不发。
		// 见 dungeon_hell_party_wave.go。
		if err := s.sendCurrentHellPartyMonsterInfo(session, runtime, runtime.Room, scene, "select_dungeon_after_op29_start_map"); err != nil {
			s.logDungeonEntryStepFailure(session, "send_op679_hell_party_monster_info", prepared, err)
			return err
		}
	}
	// 无论 op29 是否发出，这一句都必须执行：它是 actor `objectKey` 在**运行时**的绑定
	// （`session.BindHostileObject` + `monsters[].State = announced`），op39 的 owner 校验
	// 读的就是它。锦标赛地图上公告者换成 386，但 sequence 空间逐值不变。
	announcedActors, err := runtime.Room.AnnounceAllActors(runtime.Session)
	if err != nil {
		s.logDungeonEntryStepFailure(session, "announce_start_map_actors", prepared, err)
		return fmt.Errorf("commit current start-map runtime actors after op29 send: %w", err)
	}
	if err := runtime.cacheCurrentDungeonRoom(); err != nil {
		s.logDungeonEntryStepFailure(session, "cache_start_map_room", prepared, err)
		return fmt.Errorf("cache current start-map runtime room after actor announce: %w", err)
	}
	s.logGameEvent(session, "game-upper-current-dungeon-start-op29-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"announced_actor_count", announcedActors)
	// 全员读条屏障（`DNFBRIDGE_PARTY_DUNGEON_LOADING_BARRIER=vendor`，默认）：
	// 组队副本里落位包压到"全队 op37 finish-loading 到齐"再统一放行
	// （厂商 `party_dungeon_shared_loading_barrier`）。见 party_dungeon_loading_barrier.go。
	if s.deferPartyDungeonPostStartMapPlacement(session, runtime, "select_dungeon_after_op29_commit") {
		// 已挂起，落位交给屏障放行。
	} else if err := s.sendCurrentPostStartMapPlayerPlacement(session, runtime, scene, "select_dungeon_after_op29_commit"); err != nil {
		s.logDungeonEntryStepFailure(session, "post_start_map_player_placement", prepared, err)
		return fmt.Errorf("send current post-start-map player state: %w", err)
	}
	// 厂商 packet_order ③ 的收尾两包：start-map（op29）提交之后先补一条 op36 fatigue 状态，
	// 再给每个成员各发一条 class0/op9 kind0 房间槽位表（`…ff-op36-op9-room-slot-table`）。
	if err := s.sendDungeonStartMapFatigue(session, "select_dungeon_after_op29_commit"); err != nil {
		s.logDungeonEntryStepFailure(session, "send_op36_start_map_fatigue", prepared, err)
		return fmt.Errorf("send dungeon start-map fatigue: %w", err)
	}
	// 默认档在进场这一次是"补第二条"（既有的 `…_post_start_map` op9 已在本次提交里发过）；
	// `DNFBRIDGE_PARTY_DUNGEON_ROOM_SLOT_TABLE=once` 可让它跳过一次。
	// 见 dungeon_party_room_slot_table.go。
	if err := s.sendDungeonPartyRoomSlotTable(
		session,
		runtime,
		scene,
		dungeonPartyRoomSlotTableTriggerEntry,
		"select_dungeon_after_op29_commit",
	); err != nil {
		s.logDungeonEntryStepFailure(session, "send_op9_room_slot_table", prepared, err)
		return fmt.Errorf("send dungeon room slot table: %w", err)
	}

	s.logGameEvent(session, "game-dungeon-runtime-prepared",
		"dungeon_id", request.DungeonID,
		"difficulty", request.Difficulty,
		"maze_index", runtime.MazeIndex,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"map_path", scene.Map.Map.Path,
		"monsters", len(scene.Monsters),
		"hostiles", len(scene.ExpectedHostiles),
		"blocking_hostiles", len(scene.BlockingHostiles),
		"npcs", len(scene.NPCs),
		"passive_objects", len(scene.PassiveObjects)+len(scene.SpecialPassiveObjects),
		"summons", len(scene.Summons),
		"runtime_monsters", runtime.Room.MonsterCount(),
		"opaque_hostiles", runtime.Room.OpaqueHostileCount(),
		"tutorial_completed_reentry", runtime.tutorialCompletedReentry,
		"entry_sequence", "requester_class1_op16_ack_or_party_follower_no_ack_then_class0_op5_then_final_owner_mode0_actor_binding_mode1_then_op27_page_enter_runtime_recompute_gate_then_op28_op29_then_full_equipment_mode1_mode3_optional_pvf_tutorial_op3_user_state_op359_op356_op124_op9_op120",
		"deferred", "real_hud_and_remaining_scene_objects",
		// 深渊房间（见 dungeon_hell_room.go）：现场 A/B 就靠这三个字段。
		// hell_room_set=false ⇒ op28 仍是 0xff/0xff 的缺席投影。
		"hell_room_switch", s.hellRoomMode().String(),
		"hell_room_set", runtime.HellRoomSet,
		"hell_room_map_id", runtime.HellRoom.MapID,
		"hell_room_x", runtime.HellRoom.X,
		"hell_room_y", runtime.HellRoom.Y,
		"start_map_hell_party_mode", runtime.StartMapHellPartyMode,
		"start_map_hell_fog", runtime.StartMapHellFogFlag)
	if fanoutParty {
		if err := s.enterRuntimePartyFollowers(session, body, preparedFollowers, partyStateAtRequest); err != nil {
			return err
		}
	}
	s.leaveTownPresenceForDungeon(session)
	s.recordCurrentLukeScene(session, runtime, "enter")
	s.recordCurrentFiendWarMap(session, runtime)
	return nil
}

// logDungeonEntryStepFailure 记录进图序列里某一步的失败。
//
// 这些步骤以前只把 error 交给上层：队员那次进图是队长事件循环 `callGameSession(队员)` 里跑的，
// 出错时日志里只剩队长那句 `enter runtime party member N into dungeon: ...`，看不出断在哪一步。
// 操作者看到的现象（队长正常、队员读条到 90% 消失、进图没有角色模型）恰好对应"op29 已发出、
// 后面的落位/绑定断掉"，所以每一步都给一条带 `step` 的独立日志。
func (s *Service) logDungeonEntryStepFailure(
	session *gameSession,
	step string,
	prepared *runtimePartyPreparedDungeonEntry,
	err error,
) {
	if s == nil || session == nil || err == nil {
		return
	}
	entryMode := "local_or_solo"
	if prepared != nil {
		entryMode = "party_follower_leader_driven"
	}
	s.logGameEvent(session, "game-party-dungeon-entry-step-failed",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"step", step,
		"entry_mode", entryMode,
		"symptom_if_last_line", "client_loading_bar_vanishes_without_actor_models",
		"error", err)
}

func (s *Service) prepareRuntimePartyDungeonEntry(
	ctx context.Context,
	leader *gameSession,
	state alignedcmd.PartyState,
	request dungeoncmd.SelectDungeonRequest,
	leaderRuntime *runtimeDungeonState,
	leaderScene worldmap.DungeonRoomScene,
) ([]*runtimePartyPreparedDungeonEntry, error) {
	if leader == nil || state.PartyID <= 0 || state.UserID != leader.selectedCharacterID {
		return nil, nil
	}
	members := runtimePartyMembers(state)
	if len(members) <= 1 {
		return nil, nil
	}
	plan := runtimePartyDungeonEntryPlan{
		randomizedObjects: leaderRuntime.randomizedObjects,
		dungeonID:         leaderRuntime.Dungeon.ID,
		mazeIndex:         leaderRuntime.MazeIndex,
		storyQuestID:      leaderRuntime.storyExperience.QuestID,
		room:              leaderScene.Coordinate,
		mapID:             leaderScene.Map.Map.ID,
		boss:              leaderRuntime.BossCoordinate,
		seed:              leaderRuntime.Seed,
		maps:              make(map[worldmap.RoomCoordinate]int64),
	}
	for _, room := range leaderRuntime.Session.Rooms() {
		if room.Map != nil {
			plan.maps[room.Coordinate] = room.Map.Map.ID
		}
	}
	prepared := make([]*runtimePartyPreparedDungeonEntry, 0, len(members)-1)
	inRunTransfer := leaderRuntime.antonHatcheryTransfer != nil
	if inRunTransfer {
		ids := make([]uint16, 0, len(members))
		for _, member := range members {
			ids = append(ids, member.UserID)
		}
		if !sameRaidParticipants(ids, leaderRuntime.antonHatcheryTransfer.Entry.Participants) {
			return nil, fmt.Errorf("Anton hatchery transfer party differs from frozen run participants")
		}
	}
	for _, member := range members {
		if member.UserID == 0 || member.UserID == leader.selectedCharacterID {
			continue
		}
		// 频道隔离（彻底隔离口径）：整队进副本要求**同频道同区域**，跨频道队友一律不带。
		if !inRunTransfer && (s.onlinePlayers == nil || !s.onlinePlayers.PeerInSameArea(leader.selectedCharacterID, member.UserID)) &&
			!s.currentFiendWarAdmissionAllowsPeer(leader, member.UserID, state.PartyID, int64(request.DungeonID)) {
			return nil, fmt.Errorf("party member %d is not present in the leader town channel/area", member.UserID)
		}
		follower, ok := s.onlineGameSession(member.UserID)
		if !ok || follower == leader {
			return nil, fmt.Errorf("party member %d has no resident game session", member.UserID)
		}
		followerState := s.runtimePartyStateForSession(follower)
		if followerState.PartyID != state.PartyID || followerState.UserID != state.UserID {
			return nil, fmt.Errorf("party member %d has stale party state", member.UserID)
		}
		if !inRunTransfer {
			if ready, reason := s.currentTownEnterSelectReady(follower); !ready {
				return nil, fmt.Errorf("party member %d selector is not ready: %s", member.UserID, reason)
			}
		}
		followerRuntime, followerScene, err := s.prepareDungeonRuntimePlanned(ctx, follower, request, &plan)
		if err != nil {
			return nil, fmt.Errorf("prepare party member %d for dungeon %d: %w", member.UserID, request.DungeonID, err)
		}
		if inRunTransfer && (followerRuntime.antonHatcheryTransfer == nil || followerRuntime.antonHatcheryTransfer.Entry.RunID != leaderRuntime.antonHatcheryTransfer.Entry.RunID || !sameRaidParticipants(followerRuntime.antonHatcheryTransfer.Entry.Participants, leaderRuntime.antonHatcheryTransfer.Entry.Participants)) {
			return nil, fmt.Errorf("Anton hatchery transfer follower has another active run")
		}
		memberIndex, memberIndexed := runtimePartyMemberIndex(member.UserID, state)
		followerRuntime.partyMemberIndex, followerRuntime.partyMemberIndexed = memberIndex, memberIndexed
		if err := applyRuntimePartyDungeonEntryPlan(followerRuntime, followerScene, plan); err != nil {
			return nil, fmt.Errorf("apply leader dungeon selection to party member %d: %w", member.UserID, err)
		}
		if err := freezeCurrentDungeonTownReturnOrigin(follower, followerRuntime); err != nil {
			return nil, fmt.Errorf("freeze party member %d town return origin: %w", member.UserID, err)
		}
		if _, err := s.buildCurrentDungeonEntryPackets(followerRuntime, followerScene); err != nil {
			return nil, fmt.Errorf("build party member %d dungeon entry packets: %w", member.UserID, err)
		}
		prepared = append(prepared, &runtimePartyPreparedDungeonEntry{
			session: follower,
			runtime: followerRuntime,
			scene:   followerScene,
			plan:    plan,
			request: request,
		})
		s.logGameEvent(follower, "game-party-dungeon-entry-member-prepared",
			"party_id", state.PartyID,
			"leader_char_id", leader.selectedCharacterID,
			"char_id", member.UserID,
			"dungeon_id", followerRuntime.Dungeon.ID,
			"maze_index", followerRuntime.MazeIndex,
			"room", followerScene.Coordinate.String(),
			"map_id", followerScene.Map.Map.ID,
			"seed", followerRuntime.Seed)
	}
	if err := s.freezeCurrentGuildDungeonGroup(ctx, leader, leaderRuntime, prepared); err != nil {
		return nil, err
	}
	for _, member := range prepared {
		if inRunTransfer {
			continue
		}
		ready := false
		err := s.callGameSession(ctx, member.session, "runtime-party-dungeon-selector-preflight", func() error {
			var prepareErr error
			ready, prepareErr = s.prepareRuntimePartyFollowerEnterSelect(
				member.session,
				leader.selectedCharacterID,
				state.PartyID,
				"leader_op16_party_follower_preflight",
			)
			return prepareErr
		})
		if err != nil {
			return nil, fmt.Errorf("prepare party member %d selector: %w", member.session.selectedCharacterID, err)
		}
		if !ready {
			return nil, fmt.Errorf("party member %d selector is not ready", member.session.selectedCharacterID)
		}
	}
	return prepared, nil
}

func applyRuntimePartyDungeonEntryPlan(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	plan runtimePartyDungeonEntryPlan,
) error {
	if runtime == nil || runtime.Session == nil {
		return errors.New("party member runtime is unavailable")
	}
	if runtime.Dungeon.ID != plan.dungeonID || runtime.MazeIndex != plan.mazeIndex ||
		scene.Coordinate.X != plan.room.X || scene.Coordinate.Y != plan.room.Y ||
		scene.Map.Map.ID != plan.mapID ||
		runtime.BossCoordinate.X != plan.boss.X || runtime.BossCoordinate.Y != plan.boss.Y {
		return fmt.Errorf(
			"topology mismatch got dungeon=%d maze=%d room=%s map=%d boss=%s want dungeon=%d maze=%d room=%s map=%d boss=%s",
			runtime.Dungeon.ID, runtime.MazeIndex, scene.Coordinate.String(), scene.Map.Map.ID, runtime.BossCoordinate.String(),
			plan.dungeonID, plan.mazeIndex, plan.room.String(), plan.mapID, plan.boss.String(),
		)
	}
	resolved := runtime.Session.Rooms()
	if len(resolved) != len(plan.maps) {
		return fmt.Errorf("resolved room count mismatch got=%d want=%d", len(resolved), len(plan.maps))
	}
	for _, room := range resolved {
		wantMapID, ok := plan.maps[room.Coordinate]
		if !ok || room.Map == nil || room.Map.Map.ID != wantMapID {
			gotMapID := int64(0)
			if room.Map != nil {
				gotMapID = room.Map.Map.ID
			}
			return fmt.Errorf("room %s map mismatch got=%d want=%d known=%t", room.Coordinate, gotMapID, wantMapID, ok)
		}
	}
	runtime.Seed = plan.seed
	if visit := runtime.Rooms[runtimeDungeonRoomKeyFromScene(scene)]; visit != nil {
		visit.Seed = plan.seed
		visit.DropRNG = plan.seed
	}
	return nil
}

func (s *Service) commitRuntimePartyPreparedFollowers(prepared []*runtimePartyPreparedDungeonEntry) error {
	committed := make([]*runtimePartyPreparedDungeonEntry, 0, len(prepared))
	for _, member := range prepared {
		if member == nil || member.session == nil || member.runtime == nil {
			s.rollbackRuntimePartyPreparedFollowers(committed)
			return errors.New("prepared party member runtime is incomplete")
		}
		if err := s.commitDungeonRuntimeWithPetClock(member.session, member.runtime, false); err != nil {
			s.rollbackRuntimePartyPreparedFollowers(committed)
			return fmt.Errorf("commit party member %d runtime: %w", member.session.selectedCharacterID, err)
		}
		member.committed = true
		committed = append(committed, member)
	}
	return nil
}

func (s *Service) rollbackRuntimePartyPreparedFollowers(prepared []*runtimePartyPreparedDungeonEntry) {
	for _, member := range prepared {
		if member == nil || member.session == nil || member.runtime == nil || !member.committed {
			continue
		}
		member.session.dungeon.mu.Lock()
		petClockRolledBack := false
		if member.session.dungeon.runtime == member.runtime {
			member.session.dungeon.runtime = nil
			s.releasePartyDungeonInstance(member.runtime, member.session.selectedCharacterID, "party_entry_rollback")
			if dungeonRuntimeOwnsCharacter(member.runtime, member.session.selectedCharacterID) {
				if err := queueCurrentPetGrowthClockTransition(member.session, currentPetGrowthClockTown, s.gameplayNow(), "party_entry_rollback"); err != nil {
					s.logGameEvent(member.session, "game-pet-growth-clock-transition-rejected", "source", "party_entry_rollback", "error", err)
				}
				petClockRolledBack = true
			}
		}
		member.session.dungeon.mu.Unlock()
		if petClockRolledBack {
			s.flushCurrentPetGrowthClockTransitions(member.session, "party_entry_rollback")
		}
		member.committed = false
	}
}

func runtimePartyMemberIndexForSession(session *gameSession) (byte, bool) {
	if session == nil || session.selectedCharacterID == 0 {
		return 0, false
	}
	return runtimePartyMemberIndex(session.selectedCharacterID, runtimePartyStateSnapshot(session))
}

// runtimePartyMemberIndex returns the normalized fixed slot, not the slice index.
// runtimePartyMembers has already expanded legacy all-zero slots to 0..n-1.
// A one-member roster keeps the existing (0, false) sentinel.
func runtimePartyMemberIndex(characterID uint16, state alignedcmd.PartyState) (byte, bool) {
	members := runtimePartyMembers(state)
	if len(members) <= 1 {
		return 0, false
	}
	for _, member := range members {
		if member.UserID == characterID {
			return member.Slot, true
		}
	}
	return 0, false
}

func currentDungeonRuntimePartyMemberIndex(runtime *runtimeDungeonState) byte {
	if runtime == nil || !runtime.partyMemberIndexed {
		return 0xff
	}
	return runtime.partyMemberIndex
}

func (s *Service) enterRuntimePartyFollowers(
	leader *gameSession,
	body []byte,
	prepared []*runtimePartyPreparedDungeonEntry,
	state alignedcmd.PartyState,
) error {
	if leader == nil || leader.selectedCharacterID == 0 {
		return nil
	}
	if state.PartyID <= 0 || state.UserID != leader.selectedCharacterID {
		return nil
	}
	if len(prepared) == 0 {
		return nil
	}
	for _, member := range prepared {
		if member == nil || member.session == nil || member.runtime == nil || !member.committed {
			return errors.New("party follower entry was not prepared and committed")
		}
		follower := member.session
		followerParty := s.runtimePartyStateForSession(follower)
		if followerParty.PartyID != state.PartyID || followerParty.UserID != state.UserID {
			return fmt.Errorf("party member %d changed party state during dungeon entry", follower.selectedCharacterID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		err := s.callGameSession(ctx, follower, "runtime-party-dungeon-entry", func() error {
			return s.handleDungeonSelectUpperPlanned(follower, append([]byte(nil), body...), member, false)
		})
		cancel()
		if err != nil {
			return fmt.Errorf("enter runtime party member %d into dungeon: %w", follower.selectedCharacterID, err)
		}
	}
	return nil
}

func (s *Service) leaveTownPresenceForDungeon(session *gameSession) {
	if s == nil || s.onlinePlayers == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	// 进副本前先取一份**权威队伍名单**：下面要把"还在同一支队伍里"的队友从 op6 里排除。
	partyState := s.runtimePartyStateForSession(session)
	_, others, removed := s.onlinePlayers.LeaveAreaSession(session.selectedCharacterID, session)
	if removed && len(others) > 0 {
		s.broadcastTownPlayerLeaveForDungeonEntry(session, others, partyState)
	}
}

// broadcastTownPlayerLeaveForDungeonEntry 是"进副本离开城镇区域"这一步的收尾广播。
//
// **对同队队友不能发 op6**：本仓库自己的注释已经写明（online_player_manager.go 的
// broadcastTownPlayerAreaChange）——"当前客户端把 op6 同时当成 town-actor destroy 与
// **party-member removal**；对队友只能用原生 op23（`town_party_member_area_change_0x0017`），
// 它会把这个 actor 移出渲染区域但保留队伍名册与 minimap 身份"。
//
// 现场（2026-09-25 04:0x，packet_log.txt 3246/3509/3886/4687）：`town_copresence_leave_0x0006`
// 确实发给了仍在队伍里的队友，而操作者随后看到"回城后队长看不到队员 / 队员看不到自己角色"
// 这类单向丢失 —— 与"客户端把 op6 当成员移除"这条既有结论一致。
//
// 非队友（路人）保持 op6 不变：他们本来就不在名册里，必须把 actor 从城镇场景删掉。
func (s *Service) broadcastTownPlayerLeaveForDungeonEntry(
	mover *gameSession,
	others []onlinePlayerInfo,
	state alignedcmd.PartyState,
) {
	if s == nil || s.onlinePlayers == nil || mover == nil || len(others) == 0 {
		return
	}
	partyMembers := make(map[uint16]struct{}, len(runtimePartyMembers(state)))
	if state.PartyID > 0 {
		for _, member := range runtimePartyMembers(state) {
			if member.UserID != 0 && member.UserID != mover.selectedCharacterID {
				partyMembers[member.UserID] = struct{}{}
			}
		}
	}
	leaveBody := buildTownUserLeaveBody(mover.selectedCharacterID)
	// 队友那条用 op23：坐标取 mover 在**城镇在线表里登记的最后位置**（进副本那一刻的位置）。
	for i := range others {
		peer := others[i].Session
		if peer == nil {
			continue
		}
		if _, sameParty := partyMembers[others[i].CharacterID]; sameParty {
			if peerState := s.runtimePartyStateForSession(peer); peerState.PartyID == state.PartyID {
				areaBody := buildCurrentTownUserAreaNotificationBody(
					currentSceneActorObjectKey(mover.selectedCharacterID),
					byte(others[i].TownID),
					byte(others[i].AreaID),
					others[i].PositionX,
					others[i].PositionY,
					others[i].Direction,
					others[i].AreaState,
				)
				_ = s.sendCurrentSceneFixedClass0Packet(
					peer,
					currentTownUserAreaNotificationMsgID,
					areaBody,
					"town_party_member_dungeon_entry_area_change_0x0017",
				)
				continue
			}
		}
		_ = s.sendCurrentSceneFixedClass0Packet(
			peer,
			currentTownUserLeaveMsgID,
			leaveBody,
			"town_copresence_leave_0x0006",
		)
	}
}

func (s *Service) replayDungeonSelectAckForActiveRuntime(
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
	requestBodyLen int,
) (bool, error) {
	if session == nil {
		return false, nil
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil {
		session.dungeon.mu.Unlock()
		return false, nil
	}
	activeRequest := runtime.Request
	snapshot := runtime.Session.Snapshot().Run
	session.dungeon.mu.Unlock()

	if snapshot.Status != worldmap.DungeonRunActive ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) ||
		!sameSelectDungeonRequest(activeRequest, request) {
		return false, nil
	}

	selectAckBody := buildSelectDungeonBodyForCharacter(session.selectedCharacterID)
	s.logGameEvent(session, "game-dungeon-select-duplicate-ack-send",
		"msg_id", uint16(dnfenum.CmdPacketSelectDungeon),
		"body_len", len(selectAckBody)+1,
		"mode", selectAckBody[0],
		"reader_key", binary.LittleEndian.Uint16(selectAckBody[1:3]),
		"request_body_len", requestBodyLen,
		"dungeon_id", snapshot.DungeonID,
		"maze_index", snapshot.MazeIndex,
		"room", snapshot.Current.String(),
		"replayed_packets", 0,
		"reason", "idempotent_retry_for_same_active_current_dungeon")
	if err := s.sendGameUpperSuccess(
		session,
		uint16(dnfenum.CmdPacketSelectDungeon),
		selectAckBody,
	); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Service) sendSelectDungeonFailure(session *gameSession, reason byte) error {
	// Current EXE op16 reads failure, reason, and a separate supplemental byte.
	return s.sendGameUpperRaw(session, uint16(dnfenum.CmdPacketSelectDungeon), []byte{0, reason, 0})
}

func (s *Service) sendSelectDungeonFailureForError(session *gameSession, reason byte, err error) error {
	if reason == currentSelectDungeonMissingPermissionReason && errors.Is(err, errDungeonMapQuestRequired) {
		return s.sendCurrentRiftUnlockFailure(session, err)
	}
	return s.sendSelectDungeonFailure(session, reason)
}

// currentSelectDungeonEntryFailureReason 把一次进图前置失败映射成
// `{u8 0}{u8 reason}{u8 context}` 里的 reason 字节，并给出给日志用的稳定类别串。
// 未知失败按厂商 default（DungeonUnavailable）回 0x01 —— 静默丢弃才是缺陷。
func currentSelectDungeonEntryFailureReason(err error) (byte, string) {
	switch {
	case errors.Is(err, errDungeonMapQuestRequired):
		return currentSelectDungeonMissingPermissionReason, "entry_map_quest_required"
	case errors.Is(err, dnfdungeon.ErrDayDungeonClosed):
		return currentSelectDungeonUnavailableReason, "entry_day_dungeon_closed"
	case errors.Is(err, dnfdungeon.ErrDailyEntryLimit):
		return currentSelectDungeonMemberEntryLimitReason, "entry_daily_limit"
	case errors.Is(err, dnfdungeon.ErrEntryItemConsumeInsufficient):
		return currentSelectDungeonMissingRequiredItemReason, "entry_required_item_missing_at_commit"
	case errors.Is(err, dnfdungeon.ErrEntryLevelTooLow):
		return currentSelectDungeonEntryLevelTooLowReason, "entry_level_too_low"
	case errors.Is(err, dnfdungeon.ErrEntryFatigueExhausted):
		return currentSelectDungeonInsufficientFatigueReason, "entry_fatigue_exhausted"
	case errors.Is(err, dnfdungeon.ErrEntryFatigueUnknown):
		return currentSelectDungeonInvalidSelectionStateReason, "entry_fatigue_unknown"
	case errors.Is(err, dnfdungeon.ErrEntryPartyLimit):
		return currentSelectDungeonMemberEntryLimitReason, "entry_party_limit"
	case errors.Is(err, dnfdungeon.ErrEntryRequiredItemMissing):
		// PVF `[required item]` 没带够：厂商回 MissingRequiredItem(0x11)，客户端据此渲染
		// 系统聊天栏那一行。回退档 `DNFBRIDGE_DUNGEON_ENTRY_REQUIRED_ITEM_REASON=unavailable`
		// 逐字节回到改动前的 0x01。
		if dungeonEntryRequiredItemReasonFixed() {
			return currentSelectDungeonMissingRequiredItemReason, "entry_required_item_missing"
		}
		return currentSelectDungeonUnavailableReason, "entry_required_item_missing"
	case errors.Is(err, dnfdungeon.ErrEntryRequiredItemUnknown):
		// 持仓事实读不到（fail-closed 拒绝）。厂商对"未知"没有独立取值，沿用同一档，
		// 类别串区分开，便于线上 grep 判断到底是缺件还是读不到。
		if dungeonEntryRequiredItemReasonFixed() {
			return currentSelectDungeonMissingRequiredItemReason, "entry_required_item_unknown"
		}
		return currentSelectDungeonUnavailableReason, "entry_required_item_unknown"
	default:
		return currentSelectDungeonUnavailableReason, "entry_unavailable"
	}
}

// sessionHasActiveDungeonRuntime 报告该会话是否已经有一条活动副本 runtime。
func sessionHasActiveDungeonRuntime(session *gameSession) bool {
	if session == nil {
		return false
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	return session.dungeon.runtime != nil
}

// refuseDungeonSelectEntryValidation 是 `dungeon_owner_validation_failed` 的出口：
// 前置校验（等级/疲劳/队伍/索引）失败时按错误类别选拒绝码。
func (s *Service) refuseDungeonSelectEntryValidation(
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
	err error,
) error {
	reason, class := currentSelectDungeonEntryFailureReason(err)
	return s.refuseDungeonSelectEntry(
		session, request, "dungeon_owner_validation_failed", class, reason, err)
}

// refuseDungeonSelectEntry 是进图准备阶段所有"客户端必须收到答复"的失败的统一出口：
// 先记一条带判定依据的日志，再回 `{0,reason,0}`。拒绝都发生在 runtime/场景提交
// 之前，因此不需要回滚，也不得改动会话的进图进度位。
//
// 唯一保留静默的情形：会话**已经**有一条活动 runtime。那时客户端已经在副本里，
// 不是在选图界面等应答；对"另一个副本"的重复 op16 回拒绝包会干扰在跑的 runtime，
// 既有用例 `dungeon_runtime_test.go` 的"mismatched duplicate select"正是钉住这条
// 口径。现场报障「开始冒险没反应」只出现在"没有 runtime、玩家卡在选图界面"这条
// 路径上，因此这里只对那条路径补齐回包。
func (s *Service) refuseDungeonSelectEntry(
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
	siteReason string,
	class string,
	reason byte,
	err error,
	extra ...any,
) error {
	reply := true
	suppressed := ""
	if sessionHasActiveDungeonRuntime(session) {
		reply, suppressed = false, "active_dungeon_runtime_keeps_established_silence"
	}
	// `DNFBRIDGE_HELL_PARTY_ENTRY=legacy` 是 A/B 回滚档，承诺深渊派对请求
	// "逐字节回到改动前行为（不校验、不回包）"——改动前这里就是静默丢弃，
	// 所以 legacy 档必须连这些下游拒绝包一起保持静默（见 dungeon_hell_party_entry_test.go
	// 的 legacy 用例）。普通选图请求（entry_option = 0）不受影响。
	if hellPartySelectRequested(request) && !s.hellPartyEntryGate().enforces() {
		reply, suppressed = false, "hell_party_legacy_mode_keeps_pre_change_silence"
	}
	var unlockFailure *dungeonMapQuestRequiredError
	unlockNotice := reason == currentSelectDungeonMissingPermissionReason && errors.As(err, &unlockFailure) && s.currentRiftEntryEnabled(unlockFailure.dungeonID)
	wireReason := reason
	if unlockNotice {
		wireReason = currentSelectDungeonInvalidSelectionStateReason
	}
	fields := []any{
		"char_id", sessionSelectedCharacterID(session),
		"dungeon_id", request.DungeonID,
		"difficulty", request.Difficulty,
		"reason", siteReason,
		"failure_class", class,
		"failure_reason_byte", wireReason,
		"reply_sent", reply,
		"failure_body_source", "a21_dungeon_admission_reject_0_success_1_reason_2_context",
	}
	if unlockNotice {
		fields = append(fields, "unlock_character_id", unlockFailure.characterID,
			"unlock_quest_id", unlockFailure.questID, "unlock_text_notice", true)
	}
	fields = append(fields, extra...)
	fields = append(fields, "error", err)
	if !reply {
		fields = append(fields, "reply_suppressed_reason", suppressed)
	}
	s.logGameEvent(session, "game-dungeon-select-blocked", fields...)
	if !reply {
		return nil
	}
	return s.sendSelectDungeonFailureForError(session, reason, err)
}

func sameSelectDungeonRequest(left, right dungeoncmd.SelectDungeonRequest) bool {
	return dnfdungeon.SameSelectRequest(left, right)
}

func (s *Service) handleDungeonMoveMap(session *gameSession, body []byte) error {
	return s.handleDungeonMoveMapPlanned(session, body, nil, true)
}

type runtimePartyDungeonMovePlan struct {
	target runtimeDungeonRoomKey
	seed   uint32
}

func (s *Service) handleDungeonMoveMapPlanned(
	session *gameSession,
	body []byte,
	partyPlan *runtimePartyDungeonMovePlan,
	fanoutParty bool,
) error {
	if len(body) != dungeoncmd.MoveMapRequestSize {
		s.logGameEvent(session, "game-dungeon-move-blocked",
			"body_len", len(body),
			"expected_body_len", dungeoncmd.MoveMapRequestSize,
			"expected_layout", "current_exe_op45_plaintext_after_client_cipher_bypass",
			"reason", "current_exe_op45_plaintext_request_boundary_mismatch")
		return nil
	}
	request, err := dungeoncmd.DecodeMoveMapRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-move-blocked",
			"body_len", len(body),
			"reason", "current_exe_op45_request_malformed",
			"error", err)
		return nil
	}
	if session == nil {
		return nil
	}
	if fanoutParty && partyPlan == nil {
		if err := s.preflightRuntimePartyRoomOwners(session, request); err != nil {
			s.logGameEvent(session, "game-dungeon-move-blocked",
				"next_x", request.NextX, "next_y", request.NextY,
				"reason", "party_member_room_owner_preflight_failed", "error", err)
			return nil
		}
	}

	session.dungeon.mu.Lock()
	before := runtimeDungeonRoomKey{}
	beforeKnown := false
	if session.dungeon.runtime != nil && session.dungeon.runtime.Session != nil {
		if scene, ok := session.dungeon.runtime.Session.Scene(); ok {
			before, beforeKnown = runtimeDungeonRoomKeyFromScene(scene), true
		}
	}
	// "门口就位"选票：op45 的 PositionX/PositionY（body[2:6]/[6:10]，偏移由既有
	// dungeoncmd.DecodeMoveMapRequest 给出并已用现场 body 逐字节复核）是当前客户端在副本里
	// **唯一**带已证实坐标字段的上报（副本内没有 op35/op36）。这里只记录 + 记日志：
	// 门规则（3 取 2 / 4 取 3）本轮只做证据收集，不据此放行任何过图。
	requestSource := "client_op45"
	if partyPlan != nil {
		// The follower plan reuses the leader's body. Its coordinates are not
		// an arrival report from this session and must never cast another vote.
		requestSource = "internal_party_plan"
	}
	if beforeKnown && partyPlan == nil {
		s.recordCurrentDungeonDoorArrivalLocked(session, before, request)
	}
	err = s.handleDungeonMoveRequestLocked(session, request, len(body), requestSource, partyPlan)
	after := runtimeDungeonRoomKey{}
	afterKnown := false
	afterSeed := uint32(0)
	committedScene := worldmap.DungeonRoomScene{}
	if session.dungeon.runtime != nil && session.dungeon.runtime.Session != nil {
		if scene, ok := session.dungeon.runtime.Session.Scene(); ok {
			after, afterKnown = runtimeDungeonRoomKeyFromScene(scene), true
			afterSeed = session.dungeon.runtime.Seed
			committedScene = scene
		}
	}
	session.dungeon.mu.Unlock()
	if err != nil {
		return err
	}
	if fanoutParty && beforeKnown && afterKnown && before != after {
		if err := s.moveRuntimePartyFollowers(session, body, runtimePartyDungeonMovePlan{target: after, seed: afterSeed}); err != nil {
			return err
		}
	}
	// 厂商 packet_order ③（`…op36-op9-room-slot-table`）：**过图**（op45 → op29 提交）之后
	// 同样要给每个成员各发一条 class0/op9 kind0 房间槽位表。改动前这条路径**一条 op9 都不发**
	// ——这正是"过图后队员卡在等待队员到达"的缺口。
	//
	// 放在 `session.dungeon.mu` **之外**：这一步要读队伍状态与角色记录
	// （`runtimePartyStateSnapshot` / `characterForEnter`），不能在持有副本锁时做。
	// 队员各自的过图会走它们自己的 `handleDungeonMoveMapPlanned`，因此每个成员各得一条
	// （与厂商 conn59/conn61 各一条同形）。见 dungeon_party_room_slot_table.go。
	if beforeKnown && afterKnown && before != after {
		session.dungeon.mu.Lock()
		var fatigueErr error
		if session.dungeon.runtime != nil && session.dungeon.runtime.fatigueRooms != nil {
			fatigueErr = s.debitDungeonRoomFatigue(session, session.dungeon.runtime, committedScene)
		}
		session.dungeon.mu.Unlock()
		if fatigueErr != nil {
			return fatigueErr
		}
		if err := s.sendDungeonStartMapFatigue(session, "room_transition_after_op29_commit"); err != nil {
			return fmt.Errorf("send dungeon start-map fatigue after room transition: %w", err)
		}
		if err := s.sendDungeonPartyRoomSlotTable(
			session,
			nil,
			committedScene,
			dungeonPartyRoomSlotTableTriggerMove,
			"room_transition_after_op29_commit",
		); err != nil {
			return fmt.Errorf("send dungeon room slot table after room transition: %w", err)
		}
		s.recordCurrentLukeScene(session, session.dungeon.runtime, "map")
		s.recordCurrentFiendWarMap(session, session.dungeon.runtime)
	}
	return nil
}

func (s *Service) moveRuntimePartyFollowers(leader *gameSession, body []byte, plan runtimePartyDungeonMovePlan) error {
	if leader == nil || leader.selectedCharacterID == 0 {
		return nil
	}
	state := s.runtimePartyStateForSession(leader)
	if state.PartyID <= 0 || state.UserID != leader.selectedCharacterID {
		return nil
	}
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == 0 || member.UserID == leader.selectedCharacterID {
			continue
		}
		follower, ok := s.onlineGameSession(member.UserID)
		if !ok || follower == leader {
			return fmt.Errorf("party member %d has no resident game session during room transition", member.UserID)
		}
		followerState := s.runtimePartyStateForSession(follower)
		if followerState.PartyID != state.PartyID || followerState.UserID != state.UserID {
			return fmt.Errorf("party member %d changed party state during room transition", member.UserID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		err := s.callGameSession(ctx, follower, "runtime-party-dungeon-room-transition", func() error {
			if err := s.handleDungeonMoveMapPlanned(follower, append([]byte(nil), body...), &plan, false); err != nil {
				return err
			}
			// A local refusal is intentionally silent for client requests. It
			// must not masquerade as a successful internal party transition.
			follower.dungeon.mu.Lock()
			defer follower.dungeon.mu.Unlock()
			runtime := follower.dungeon.runtime
			if runtime == nil || runtime.Session == nil {
				return errDungeonWorldMapUnavailable
			}
			scene, ok := runtime.Session.Scene()
			if !ok || runtimeDungeonRoomKeyFromScene(scene) != plan.target || runtime.Seed != plan.seed {
				return fmt.Errorf("%w: follower did not commit planned party room", errDungeonMoveRuntimeOwnerMismatch)
			}
			return nil
		})
		cancel()
		if err != nil {
			return fmt.Errorf("move runtime party member %d with leader: %w", member.UserID, err)
		}
	}
	return nil
}

// handleDungeonMoveRequestLocked runs with session.dungeon.mu held. Every op45
// is handled exactly once: an uncleared-room request is rejected and never
// retained for a later unsolicited op29.
func (s *Service) handleDungeonMoveRequestLocked(
	session *gameSession,
	request dungeoncmd.MoveMapRequest,
	requestBodyLen int,
	requestSource string,
	partyPlan *runtimePartyDungeonMovePlan,
) error {
	runtime := session.dungeon.runtime
	if runtime == nil || runtime.Session == nil || runtime.Room == nil {
		s.logGameEvent(session, "game-dungeon-move-blocked",
			"next_x", request.NextX,
			"next_y", request.NextY,
			"reason", "dungeon_runtime_missing")
		return nil
	}
	if err := s.persistCurrentDungeonRoomClearQuestLocked(session, runtime); err != nil {
		return err
	}
	plan, err := s.planCurrentDungeonMove(runtime, request)
	if err != nil {
		currentScene, hasCurrentScene := runtime.Session.Scene()
		source := plan.Source.Coordinate.String()
		if hasCurrentScene {
			source = currentScene.Coordinate.String()
		}
		s.logGameEvent(session, "game-dungeon-move-blocked",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"source", source,
			"next_x", request.NextX,
			"next_y", request.NextY,
			"request_source", requestSource,
			"reason", "target_room_preflight_failed",
			"error", err)
		return nil
	}
	if partyPlan != nil {
		target := runtimeDungeonRoomKeyFromScene(plan.Target)
		if target != partyPlan.target {
			return fmt.Errorf("party dungeon room topology mismatch got=%+v want=%+v", target, partyPlan.target)
		}
		plan.Seed = partyPlan.seed
	}
	s.cancelCurrentDungeonReturnAfterDungeonEvidenceLocked(
		session,
		runtime,
		"accepted_current_exe_op45_after_pending_op24",
	)
	if plan.LayerBaseAck {
		chain := runtime.LayerChains[plan.Source.Coordinate]
		if chain == nil || !chain.Consumed || !chain.FinalAckPending {
			return fmt.Errorf("%w: layered base ACK state changed before commit", errDungeonMoveRuntimeOwnerMismatch)
		}
		chain.FinalAckPending = false
		s.logGameEvent(session, "game-dungeon-layer-base-ack-consumed",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"room", plan.Source.Coordinate.String(),
			"map_id", plan.Source.Map.Map.ID,
			"request_body_len", requestBodyLen,
			"request_source", requestSource,
			"response", "none",
			"reason", "current_exe_final_same_coordinate_change_map_confirmation_after_operation2_mode0_base_restore")
		return nil
	}
	// 换房 op29 之前同样先备好 map-owned 注册表 / 预生成掉落行（纯读的构建器依赖它）。
	// 两个开关都是默认档时这一行是空操作。见 dungeon_passive_object_drop.go。
	if plan.PayloadMode != currentDungeonStartMapPayloadCached {
		if err := s.prepareCurrentDungeonPassiveObjectDropsAfterActors(session, runtime, plan.Target, "move_map_target_room", plan.NextObjectKey); err != nil {
			s.logGameEvent(session, "game-dungeon-move-blocked",
				"dungeon_id", runtime.Dungeon.ID,
				"maze_index", runtime.MazeIndex,
				"source", plan.Source.Coordinate.String(),
				"target", plan.Target.Coordinate.String(),
				"reason", "passive_object_drop_prepare_failed",
				"error", err)
			return nil
		}
		// Preparation may have reserved passive rewards beyond the planned
		// actors. Committing the move must not rewind the solo allocator.
		plan.NextObjectKey = max(plan.NextObjectKey, runtime.NextObjectKey)
	}
	startMapBody, err := buildCurrentDungeonMoveStartMapBody(runtime, plan)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-move-blocked",
			"dungeon_id", runtime.Dungeon.ID,
			"maze_index", runtime.MazeIndex,
			"source", plan.Source.Coordinate.String(),
			"target", plan.Target.Coordinate.String(),
			"reason", "target_start_map_build_failed",
			"error", err)
		return nil
	}
	targetSnapshot := plan.TargetRoom.Snapshot()
	packetActorCount := len(targetSnapshot.Monsters) + len(targetSnapshot.ExtendedActors)
	if plan.PayloadMode == currentDungeonStartMapPayloadCached {
		packetActorCount = 0
	}
	s.logGameEvent(session, "game-dungeon-move-start-map-send",
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"source", plan.Source.Coordinate.String(),
		"target", plan.Target.Coordinate.String(),
		"map_id", plan.Target.Map.Map.ID,
		"seed", plan.Seed,
		"actor_count", packetActorCount,
		"revisit", plan.Revisit,
		"layered", plan.Layered,
		"layer_return", plan.LayerReturn,
		"layer_exit", plan.LayerExit,
		"layer_base_ack", plan.LayerBaseAck,
		"layer_index", plan.LayerIndex,
		"story_stage", plan.StoryStage,
		"story_stage_index", plan.StoryStageIndex,
		"operation", plan.Operation,
		"payload_mode", plan.PayloadMode,
		"move_kind", request.MoveKind,
		"request_body_len", requestBodyLen,
		"request_source", requestSource,
		"body_len", len(startMapBody),
		"sequence", "current_exe_op45_request_to_class0_op29_start_map")
	if err := s.sendGameUpperRawClass(session, currentDungeonStartNotification, startMapBody, 0); err != nil {
		return err
	}
	// 深渊房间：每一条（首次进入的那一间）START_MAP 之后补 NOTI 679，位置与厂商一致
	// （`DungeonMapHandler.cs:658-688`：先 START_MAP、再若干投影，最后 679）。
	// 缓存档的 op29（`PayloadMode == cached`）不是一次真正的房间投影，厂商那条路
	// 也不会重发 679（`hellPartyMonsterInfoAfterStartMap` 只在房间状态创建那一次装配），
	// 所以这里同样只在非缓存档发。见 dungeon_hell_party_wave.go。
	if plan.PayloadMode != currentDungeonStartMapPayloadCached {
		if err := s.sendCurrentHellPartyMonsterInfo(session, runtime, plan.TargetRoom, plan.Target, "move_after_op29_start_map"); err != nil {
			return err
		}
	}
	if err := runtime.cacheCurrentDungeonRoom(); err != nil {
		return fmt.Errorf("cache source dungeon room before transition commit: %w", err)
	}
	var revisitScene *worldmap.DungeonRoomScene
	if plan.Revisit && !plan.StoryStage {
		revisitScene = &plan.TargetVisit.Scene
	}
	var committedScene worldmap.DungeonRoomScene
	if plan.StoryStage {
		transition, commitErr := runtime.Session.CommitStoryStage(plan.StoryStageIndex)
		if commitErr != nil {
			return fmt.Errorf("commit current dungeon story stage after op29 send: %w", commitErr)
		}
		if transition.Revisit != plan.Revisit {
			return fmt.Errorf("%w: story-stage revisit changed before commit", errDungeonMoveRuntimeOwnerMismatch)
		}
		committedScene = transition.Scene
	} else if plan.Layered {
		committedScene, err = runtime.Session.CommitLayered(plan.Target.Coordinate, plan.LayerIndex)
		if err != nil {
			return fmt.Errorf("commit current layered dungeon room after op29 send: %w", err)
		}
	} else if plan.LayerReturn {
		if plan.TargetVisit == nil {
			return fmt.Errorf("%w: layered base visit missing at commit", errDungeonMoveRuntimeOwnerMismatch)
		}
		committedScene, err = runtime.Session.CommitLayeredBase(plan.Target.Coordinate, plan.TargetVisit.Scene)
		if err != nil {
			return fmt.Errorf("commit cached layered dungeon base after op29 send: %w", err)
		}
	} else if plan.LayerExit {
		transition, moveErr := runtime.Session.MoveByteTransition(request.NextX, request.NextY, revisitScene)
		if moveErr != nil {
			return fmt.Errorf("commit terminal layered dungeon exit after op29 send: %w", moveErr)
		}
		committedScene = transition.Scene
	} else {
		transition, moveErr := runtime.Session.MoveByteTransition(request.NextX, request.NextY, revisitScene)
		if moveErr != nil {
			return fmt.Errorf("commit current dungeon room after op29 send: %w", moveErr)
		}
		committedScene = transition.Scene
	}
	if committedScene.Coordinate != plan.Target.Coordinate || committedScene.Map.Map.ID != plan.Target.Map.Map.ID {
		return fmt.Errorf("%w: planned=%s/%d committed=%s/%d",
			errDungeonMoveRuntimeOwnerMismatch,
			plan.Target.Coordinate,
			plan.Target.Map.Map.ID,
			committedScene.Coordinate,
			committedScene.Map.Map.ID,
		)
	}
	// ── 操作者缺陷「组队死亡后队长无法复活」的根因修复 ────────────────────────────
	//
	// 现场（`packet_log.txt`，2026-09-29，`conn_id=game-002700` char4 = 队长，深渊 310）：
	//
	//	05:55:45.971 `game-dungeon-character-death-op32-send char_id=4 … timer_delay_ms=10000`
	//	05:55:45.971 `game-dungeon-character-death-timer-scheduled … due_at_utc=2026-09-29T05:55:55.971416941Z`
	//	05:55:49.396 `game-dungeon-character-death-timer-cancelled char_id=4 previous_generation=5
	//	              current_generation=6 timer_name=dnf-dungeon-death:game-002700:run:1
	//	              queue_item_cancelled=true source=dungeon_room_transition_committed`
	//	05:58:38.563 `game-dungeon-use-coin-blocked conn_id=game-002700 char_id=4 target_object_key=4
	//	              expected_object_key=4 run_token=1 death_timer_waiting=false reason=dungeon_run_not_active`
	//
	// 因果链（**代码判据，不是推测**）：`handleCurrentDungeonUseCoin`（复活币 op41）的第一道闸门
	// 就是 `waiting := runtime.deathReturnWaiting`，`!waiting` ⇒ 直接
	// `game-dungeon-use-coin-blocked … reason=death_timer_not_waiting`
	// （`dungeon_death_return.go:405,417-430`）；`fireCurrentDungeonDeathReturn` 的回城/复活窗口
	// 同样以 `deathReturnWaiting` 为前提（`:238-248`）。过图这一步把
	// `deathReturnWaiting` 置 false 并**清空 `deathReturnTransition`**（`:329-332`），
	// 于是 05:55:49 之后这名角色在本局剩下的时间里**既不会回城、也无法复活**。
	// 客户端那一侧同一时刻仍在按 op32 的 `timer_delay_ms=10000` 走自己的倒计时
	// ⇒ 操作者口径「队长无法复活」。
	//
	// 同时这一条也是"死亡后下一个房间模型再次出现"的一半：死亡倒计时被抹掉之后，
	// 服务端不再认为这名角色处于权威死亡态，过图时就会照常把队友 actor 重新投影出去。
	//
	// 修法：**过图不取消已成立的死亡回城倒计时**。倒计时本身已经有自己的守卫
	// （`fireCurrentDungeonDeathReturn` 会重查 run 身份 / generation / `currentDungeonDeathRuntimeOwner`），
	// 所以保留它是安全的：到期即按 DNF 语义把死亡角色送回城镇，复活窗口也重新可用。
	//
	// 开关 `DNFBRIDGE_PARTY_DEATH_RETURN_ROOM_TRANSITION`：未设置/空/任意值 = 保留档
	// （默认，修复）；只有字面量 `legacy` 才回到"过图即取消"（= 逐字节等于改动前）。
	// 单人（无队伍）**不适用**：`legacy` 与保留档在 `partyInstance == nil` 时都走原逻辑，
	// 保证单人深渊/普通图的行为一字不改。
	if !keepDungeonDeathReturnAcrossRoomTransition(runtime) {
		s.cancelCurrentDungeonDeathReturnLocked(session, runtime, "dungeon_room_transition_committed")
	}
	runtime.Room = plan.TargetRoom
	runtime.commitDungeonNextObjectKey(plan.NextObjectKey)
	runtime.Seed = plan.Seed
	if !plan.LayerReturn {
		if _, err := s.applyPVFTutorialBasicActionRoomToSession(
			session,
			runtime,
			committedScene,
			"room_transition_before_actor_announce",
		); err != nil {
			return fmt.Errorf("apply tutorial basic-action target room: %w", err)
		}
		committedScene, _ = runtime.Session.Scene()
	}
	if plan.StoryStage {
		if plan.StoryStageIndex != runtime.StoryStageIndex+1 {
			return fmt.Errorf("%w: story-stage index changed before commit", errDungeonMoveRuntimeOwnerMismatch)
		}
		runtime.StoryStageIndex = plan.StoryStageIndex
		runtime.LayeredMapIndex = -1
		runtime.LayeredMapActive = false
	} else if plan.Layered {
		if runtime.LayerChains == nil {
			runtime.LayerChains = make(map[worldmap.RoomCoordinate]*runtimeDungeonLayerChain)
		}
		chain := runtime.LayerChains[plan.Target.Coordinate]
		if chain == nil {
			chain = &runtimeDungeonLayerChain{BaseKey: plan.LayerBaseKey}
			runtime.LayerChains[plan.Target.Coordinate] = chain
		} else if chain.BaseKey != plan.LayerBaseKey {
			return fmt.Errorf("%w: layered base key changed before commit", errDungeonMoveRuntimeOwnerMismatch)
		}
		chain.Consumed = false
		chain.FinalAckPending = false
		runtime.LayeredMapIndex = plan.LayerIndex
		runtime.LayeredMapActive = true
	} else if plan.LayerReturn {
		chain := runtime.LayerChains[plan.Target.Coordinate]
		if chain == nil || chain.BaseKey != plan.LayerBaseKey {
			return fmt.Errorf("%w: layered return chain changed before commit", errDungeonMoveRuntimeOwnerMismatch)
		}
		chain.Consumed = true
		chain.FinalAckPending = true
		runtime.LayeredMapIndex = -1
		runtime.LayeredMapActive = false
	} else if plan.LayerExit {
		chain := runtime.LayerChains[plan.Source.Coordinate]
		if chain == nil || chain.BaseKey != plan.LayerBaseKey {
			return fmt.Errorf("%w: terminal layered exit chain changed before commit", errDungeonMoveRuntimeOwnerMismatch)
		}
		chain.Consumed = true
		chain.FinalAckPending = false
		runtime.LayeredMapIndex = -1
		runtime.LayeredMapActive = false
	} else {
		runtime.LayeredMapIndex = -1
		runtime.LayeredMapActive = false
	}
	announcedActors := 0
	if plan.PayloadMode != currentDungeonStartMapPayloadCached {
		announcedActors, err = runtime.Room.AnnounceAllActors(runtime.Session)
		if err != nil {
			return fmt.Errorf("commit target-room runtime actors after op29 send: %w", err)
		}
	}
	if err := runtime.cacheCurrentDungeonRoom(); err != nil {
		return fmt.Errorf("cache committed target dungeon room: %w", err)
	}
	// FINISH_LOADING's main class0 state and op30 completion are room-scoped.
	// Keep the player/equipment/skill initialization gates intact: an ordinary
	// room move only needs the next room's loading completion, not a new actor.
	session.setFinishLoadingGateState(false)
	session.setFinishLoadingGateCompletion(false)
	s.logGameEvent(session, "game-dungeon-move-committed",
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"source", plan.Source.Coordinate.String(),
		"target", committedScene.Coordinate.String(),
		"map_id", committedScene.Map.Map.ID,
		"announced_actor_count", announcedActors,
		"revisit", plan.Revisit,
		"layered", plan.Layered,
		"layer_return", plan.LayerReturn,
		"layer_exit", plan.LayerExit,
		"layer_base_ack", plan.LayerBaseAck,
		"layer_index", plan.LayerIndex,
		"story_stage", plan.StoryStage,
		"story_stage_index", plan.StoryStageIndex,
		"operation", plan.Operation,
		"payload_mode", plan.PayloadMode,
		"cached_defeated_actor_count", len(committedScene.DefeatedObjects),
		"room_cleared", committedScene.Cleared,
		"request_source", requestSource,
		"finish_loading_main_state_rearmed", true)
	return nil
}
