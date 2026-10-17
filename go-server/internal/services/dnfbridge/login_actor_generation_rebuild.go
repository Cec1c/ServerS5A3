package dnfbridge

// 本文件是**登录后装备栏按"没穿上"渲染（0 耐久 / 非权威属性）**这条缺陷的第三轮修法。
//
// ── 一、前两轮做到了什么、还差什么（一句话）
//
// 第一轮：把登录引导的装备载体**位置**与场景路径对齐（清单见
// `login_equipment_container.go`）。第二轮：在登录路径的场景就绪提交点（客户端本次进场的
// 第一条 legacy type1345）**补发一次 list 3 容器**（`login_equipment_scene_ready_replay.go`）。
// 现场实测（操作者 2026-09-28 原话：「光剑问题还是一模一样。」）：
//
//	EVENT kind=game-login-equipment-scene-ready-replay-sent char_id=6 list=3 rows=31
//	      entry_count=31 body_len=3692 slots_sent=0..32（含 12）
//	      nonzero_durability_carried=...,12:48,... switch=after_scene_ready
//
// —— 包真的发了、内容也是权威的（槽 12 = 101040493、+0x0B 耐久 48、DB
// `dnf_equipment_entries` 同一份），**画面却没变**。⇒ 缺的不是物品栏容器，
// 而是**actor 世代**：mode0（场景对象/外观世代）+ mode1（actor 绑定 + 装备 create 行）。
// 容器只是"物品栏那一份副本"，客户端**已穿戴状态与属性**的权威来源是 actor 自己的
// 装备对象树，而它在场景提交那一刻被重建 —— 提交前/提交点下发的那几份都落在
// 被销毁的那一代对象上。
//
// ── 二、进副本为什么是对的（现场逐包，file:line）
//
// 组队进副本那次（`packet_log.txt` 14:27:20–14:27:22Z，conn game-000011，角色 6）：
//
//	14:27:20.520 RECV legacy type37 finish-loading（客户端"读条完成"）
//	14:27:22.181 EVENT game-party-dungeon-post-start-map-placement-released
//	              source=party_dungeon_shared_loading_barrier
//	14:27:22.181 EVENT game-upper-select-inventory-bootstrap-send … list_type=3
//	              char_id=6 entry_count=31 body_len=3692          （七个容器）
//	14:27:22.191 SEND class0/op2 2161  mode1 完整装备 create 行（31 行，槽 12 耐久 48）
//	14:27:22.19x SEND … op359/op356/op124/op9/op120/op3
//
// 代码路径：`party_dungeon_loading_barrier.go:470-479`（放行时先
// `sendPendingPostStartMapPlayerPlacement`，再 `sendCurrentFinishLoadingReleaseTail`）
// → `scene_post_start_map.go:376`（`sendCurrentSelectInventoryBootstrap`，七个容器）
// → `scene_post_start_map.go:381-406`（
// `buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(..., true)` 的
// **完整装备 mode1**，`userinfo_current_entry.go:160-192`）→ `:404` 发出。
//
// ⇒ 装备载体的**相对位置**是：**在客户端自己提交场景（读条完成 / 房间 commit）之后**，
// 而且载体是「容器 + **mode1 装备 create 行**」两件一起，**没有 mode0**。
//
// ── 三、登录路径差在哪（现场逐包）
//
// 同一次登录（`packet_log.txt` 15:06:33Z，conn game-000007，角色 6）：
//
//	15:06:33.432 SEND class0/op2 498   mode0 actor create
//	15:06:33.432 SEND class0/op2 163   mode1 binding-only
//	15:06:33.56x SEND class0/op13 ×7   {0,1,2,7,12,38,**3**}  ← 装备容器在提交**前**
//	15:06:33.661 SEND class0/op2 2161  mode1 完整装备 create 行（31 行）
//	15:06:33.694 SEND class0/op2 498   mode0 name-tag 重放          ← 提交**前**
//	15:06:33.784 SEND class0/op24 12   **typed op24 场景最终提交**
//	15:06:33.794 RECV legacy type1345 4  客户端声明"场景就绪"
//	15:06:33.804–.828 场景尾包 op985/op99/op1239 + HUD op1340/op1346/op1371/op413
//	15:06:35.446 RECV legacy type217 3   ← **提交之后客户端的第一条报文**
//	15:06:35.865 RECV legacy type19 28   "复述穿戴态"（服务端回 noop + mode0 498 + 两条 op14）
//	15:06:35.997 RECV legacy type503 4
//	15:06:36.001 RECV legacy type868 1
//	15:06:37.275 RECV upper op2 31（SET_UDP_IP_PORT）
//
// 代码路径：`initial_town_player_state.go:107`（七容器）/`:167-206`（完整装备 mode1）/
// `:222`（mode0 name-tag 重放）全部在 `initial_town_route_packets.go:174`
// （`sendCurrentTownTransitionLocked` → typed op24）**之前**；
// `initial_town_route_packets.go:327-336` 在 op24 之后只把路由推到
// `currentInitialTownRoutePlayerStateSent` 就返回，**提交之后一个装备载体都没有**。
// 登录路径**从不**跑 `town_post_transition_player_state.go`
//（全日志 `game-town-post-transition-player-state-sent` 只出现在
// `dungeon_return_town.go:788-805` 的两条 source，即副本返回）。
//
// ── 四、为什么不能在 type1345 那个回调里重放（三处现场判决，原文引用）
//
//	(1) `changlog.md:9943-9944`（2026-07-29「stop the legacy town-ready callback from
//	    reactivating an unbound scene object」）：
//	      "Fresh client crashes at 18:31, 19:17, and 22:09 shared exception
//	       `0xC0000005` at `0x0262EBC0`, with `ECX=0` and read address `0x000004C8`.
//	       … the type-3013 scene object was constructed with a null owner at
//	       `this+0x590`, then the legacy type1345 callback's repeated
//	       actor/finish/HUD chain allowed its update tick to dereference `owner+0x4C8`."
//	      "Legacy type1345/u32(2) now accepts only the already-owned deferred select-scene
//	       tail plus the independent op413 gauge. **It cannot replay mode0, mode1,
//	       op105/op102, op37, op30, op19, or op120.**"
//	(2) `game_dispatch_legacy.go:218-222`：
//	      "Live current-client traffic sends legacy type1345 with u32(2) immediately after
//	       the first typed op24. It completes the deferred scene tail and HUD gauge only.
//	       **Replaying actor/finish-loading state at this callback leaves the current EXE
//	       update loop with a null actor.**"
//	(3) `game_select_scene_deferred_tail.go:98-103`：
//	      "This callback does not own another actor/finish-loading generation: live traffic
//	       proved that **replaying mode0/mode1/op105/op37/op30 here leaves the client update
//	       loop with a null scene actor.**"
//
// **崩因是什么（本轮据此判定候选点）**：客户端在"自己发 type1345 那一刻"建了一个
// type-3013 场景对象（`this+0x590` 的 owner 还没绑），这条回调里紧接着灌进去的
// actor/finish 世代链会让它的 update tick 解引用 `owner+0x4C8`（null）。
// ⇒ 需要避开的**不是包本身**，而是"**与客户端自己的场景切换同一帧**"这个时机：
// 只要世代链落在**客户端提交完成之后、由客户端自己发起的新报文**上，
// 场景对象就已经绑好 owner，崩因不成立。
//
// ── 五、本轮选的点（为什么这一个安全）
//
// **登录路径上 type1345 ack 之后的第一条客户端报文**（现场 15:06:33.794 → 15:06:35.446，
// legacy type217；其它三次同角色登录也全部是 type217 领跑，见 §六）。
// 判据与安全性：
//
//	1. **不在 type1345 回调里**：这一包是客户端自己发的下一帧（现场距 ack +1.65s～+2.40s），
//	   服务端此时才回复，客户端早已跑完场景切换，type-3013 对象的 owner 已绑。
//	2. **同一相对位置有两条现场成功的先例**：进副本那次装备载体落在客户端 type37
//	   读条完成之后（§二）；回城那次 `town_post_transition_player_state.go:100-161`
//	   把 mode0 → mode1（→ op105/op102/op37/op30/op19/op120）整串放在**客户端真实城镇请求
//	   确认过的 typed op24 之后**（`dungeon_return_town.go:743-746/786-805`），
//	   同样是"客户端先开口、服务端再重建世代"。
//	3. **登录路径上已经有一条"提交之后发 mode0"的活证**：现场 15:06:35.888
//	   `game-aligned-command-post-action-send post_action=refresh_selected_actor_appearance
//	   msg_id=2 body_len=498`（= `sendSelectedActorMode0AppearanceRefresh`），
//	   它就发生在 type1345 之后，客户端**没有崩**。本轮补的正是它缺的那一半：
//	   **带装备 create 行的 mode1**。
//	4. **不碰禁改名单**：不发 op105/op102/op37/op30/op19/op120，不发 op359/op356/op124/op9/op120/op3，
//	   不重放七个容器（list 3 那份已由上一轮的 `login_equipment_scene_ready_replay.go` 覆盖）。
//	   本文件只发 `class0/op2` 的 **mode0 + mode1** 两帧 —— 即 actor 世代本体。
//	5. **收口范围**：只在"选角/登录进城"这条路由、且本次进场 type1345 ack 之后的第一条
//	   客户端报文上触发**一次**；副本返回世代（`townPostTransition.generation != 0`）、
//	   活动副本运行时、以及路由未提交/未 ack 的会话一律不发（见 `loginActorGenerationRebuildOwnedLocked`）。
//
// ── 六、现场触发面（只读取证：`packet_log.txt` 里 4 次角色 6 登录，type1345 之后的第一条 RECV）
//
//	13:52:48.977 ack → 13:52:50.613 type217 (+1.64s)
//	14:23:51.732 ack → 14:23:53.594 type217 (+1.86s)
//	14:25:08.746 ack → 14:25:10.717 type217 (+1.97s)
//	15:06:33.794 ack → 15:06:35.446 type217 (+1.65s)
//
// 触发面写成"ack 之后的第一条**任意**入站游戏报文"而不是写死 type217：客户端换版本、
// 或者那次登录少发一个 type217 时，补发不会失约。事件里原样回显 `trigger=`，所以
// "这一次到底是谁触发的"在日志里是确定的。目标会话若在 ack 之后一直不发任何报文，
// 本轮什么也不发（与今天逐字节相同）—— 这是唯一已知的残余窗口，已在报告里如实写明。
//
// ── 七、开关（消费点自读 env，不进 options.go；默认即修复，回退必须显式）
//
//	DNFBRIDGE_LOGIN_ACTOR_GENERATION_REBUILD=after_scene_commit_first_client_packet
//	    （默认，含未设置/写错的值）→ 上面那条边界上补发 mode0 + mode1；
//	    事件 `game-login-actor-generation-rebuild-sent`（含 char_id / frames /
//	    mode0_body_len / mode1_body_len / mode1_create_count / 逐槽耐久 / trigger /
//	    boundary / ordering_position / switch）。
//	before_transition_only（别名 early_only/off/0/false/no/legacy/disable/disabled/none）
//	    一键回退到今天：本文件一个字节都不发，事件
//	    `game-login-actor-generation-rebuild-suppressed` 说明档位取值。

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"strings"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

const (
	// loginActorGenerationRebuildEnv 是"actor 世代要不要在提交点之后补发"的开关名。
	loginActorGenerationRebuildEnv = "DNFBRIDGE_LOGIN_ACTOR_GENERATION_REBUILD"

	loginActorGenerationRebuildAfterSceneCommit       = "after_scene_commit_first_client_packet"
	loginActorGenerationRebuildBeforeTransitionOnly   = "before_transition_only"
	loginActorGenerationRebuildBoundary               = "client_first_authored_packet_after_legacy_type1345_scene_ready_ack"
	loginActorGenerationRebuildPosition               = "after_typed_op24_final_transition_commit_after_type1345_tail_and_hud_before_any_further_client_frame"
	loginActorGenerationRebuildFrameSequence          = "class0_op2_mode0_scene_object_list_then_class0_op2_mode1_actor_equipment_create_rows"
	loginActorGenerationRebuildScenePathEvidence      = "dungeon_entry_sends_the_full_equipment_mode1_after_the_client_finish_loading_and_town_return_sends_mode0_mode1_after_a_client_confirmed_typed_op24_login_route_sent_both_before_typed_op24"
	loginActorGenerationRebuildCrashConditionExcluded = "type1345_callback_replayed_actor_finish_hud_generation_left_the_update_loop_with_a_null_scene_actor_owner_changlog_9943_9944_game_dispatch_legacy_218_222_game_select_scene_deferred_tail_98_103"
	// loginActorGenerationRebuildMode1FixedTailSize 是 mode1 在 create 行之后那段
	// 固定尾巴的长度（`userinfo_current_modes.go:195-244`，无 update 行、技能页默认档）。
	// 只用于审计自检：走位错一格就会被这条判据抓住。
	loginActorGenerationRebuildMode1FixedTailSize = 38
)

// loginActorGenerationRebuildEnabled 回答"要不要在提交点之后补发 actor 世代"。
// 只有**显式**写回退字面量才关闭；其余取值（含未设置、写错的串）都是修复档 ——
// 默认即修复，回退必须显式（与 loginEquipmentSceneReadyReplayEnabled 同一取舍）。
func loginActorGenerationRebuildEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginActorGenerationRebuildEnv))) {
	case loginActorGenerationRebuildBeforeTransitionOnly, "early_only",
		"off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// loginActorGenerationRebuildSwitch 只用于事件/诊断。
func loginActorGenerationRebuildSwitch() string {
	if loginActorGenerationRebuildEnabled() {
		return loginActorGenerationRebuildAfterSceneCommit
	}
	return loginActorGenerationRebuildBeforeTransitionOnly
}

// loginActorGenerationRebuildArmed 报告"这一包之前补发是否已经布防"。
// `handleGameStreamPacket` 在**派发之前**取一次：布防发生在 type1345 那一包的派发过程中，
// 所以"派发前已布防"恰好等价于"这是 ack 之后的第一条客户端报文"，
// 无需任何计数器或定时器。
func loginActorGenerationRebuildArmed(session *gameSession) bool {
	if session == nil {
		return false
	}
	session.townMu.Lock()
	armed := session.loginActorGenerationRebuildArmed
	session.townMu.Unlock()
	return armed
}

// armLoginActorGenerationRebuild 在登录路径的 type1345 场景就绪 ack 上布防。
//
// 只在**登录/选角进城**这条路由、且是本次进场的**第一条** ack 时布防
// （调用点已经把这两个条件判过了，见 `game_select_scene_deferred_tail.go`）。
func (s *Service) armLoginActorGenerationRebuild(session *gameSession) {
	if session == nil {
		return
	}
	session.townMu.Lock()
	if session.initialTownRouteCharacterID != 0 &&
		session.initialTownRouteCharacterID == session.selectedCharacterID {
		session.loginActorGenerationRebuildArmed = true
	}
	session.townMu.Unlock()
}

// loginActorGenerationRebuildOwnedLocked 是"这一代 actor 归不归本文件重建"的判据。
// 调用方须持 session.townMu。
//
// 必须同时成立：
//   - 路由归属 + 场景已提交 + 本次进场的 type1345 ack 已经消费（= 已经越过那条会崩的边界）；
//   - 本次选角开始后未布防新的城镇返回世代，且返回游标处于 idle、
//     没有挂账的返回重建。以前选角的单调代数不属于当前 actor；
//     本次选角之后发生的回城已经由
//     `town_post_transition_player_state.go` 重建过 mode0/mode1，再补一份就是
//     "把客户端绑到另一个原生对象上"（该文件 `:11-14` 的警告）。
func loginActorGenerationRebuildOwnedLocked(session *gameSession) bool {
	if session == nil {
		return false
	}
	if session.initialTownRouteCharacterID == 0 ||
		session.initialTownRouteCharacterID != session.selectedCharacterID ||
		session.initialTownRouteStage < currentInitialTownRouteTransitionSent ||
		!session.initialTownLegacySceneReadyAccepted {
		return false
	}
	session.townPostTransition.mu.Lock()
	townOwnsActor := session.townPostTransition.generation != session.initialTownRouteTownGeneration ||
		session.townPostTransition.stage != currentTownPostTransitionIdle
	session.townPostTransition.mu.Unlock()
	if townOwnsActor {
		return false
	}
	if session.confirmedDungeonReturnStatePending {
		return false
	}
	return true
}

// afterLoginSceneReadyFirstClientPacket 是 `handleGameStreamPacket` 的收尾钩子：
// 场景就绪 ack 之后的**第一条**客户端报文派发成功后补发 actor 世代。
//
// 失败只记事件、绝不向上传播：补发是修复动作，不允许把一条本来成功的客户端报文
// 变成错误（更不允许触发读循环的致命路径）。
func (s *Service) afterLoginSceneReadyFirstClientPacket(session *gameSession, trigger string) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	session.townMu.Lock()
	armed := session.loginActorGenerationRebuildArmed
	// 无论哪一档，布防一次只消费一次。
	session.loginActorGenerationRebuildArmed = false
	if !armed || !loginActorGenerationRebuildOwnedLocked(session) {
		session.townMu.Unlock()
		return
	}
	alreadySent := session.loginActorGenerationRebuildSent
	session.loginActorGenerationRebuildSent = true
	charID := session.selectedCharacterID
	session.townMu.Unlock()
	if alreadySent {
		return
	}
	if !loginActorGenerationRebuildEnabled() {
		// 回退档也要留下"开关取的是哪个值"的证据：这是操作者在 packet_log.txt 里
		// 区分"修复档没生效"和"我写了回退字面量"的唯一判据。
		s.logPacketEvent("game-login-actor-generation-rebuild-suppressed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", "after_login_scene_ready_first_client_packet",
			"char_id", charID,
			"trigger", trigger,
			"boundary", loginActorGenerationRebuildBoundary,
			"ordering_position", loginActorGenerationRebuildPosition,
			"frames", "none",
			"switch", loginActorGenerationRebuildSwitch(),
			"switch_env", loginActorGenerationRebuildEnv,
			"reason", "rollback_literal_keeps_the_pre_transition_only_actor_generation")
		return
	}
	if err := s.rebuildLoginActorGenerationAfterSceneCommit(session, trigger, charID); err != nil {
		s.logPacketEvent("game-login-actor-generation-rebuild-failed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", "after_login_scene_ready_first_client_packet",
			"char_id", charID,
			"trigger", trigger,
			"boundary", loginActorGenerationRebuildBoundary,
			"ordering_position", loginActorGenerationRebuildPosition,
			"switch", loginActorGenerationRebuildSwitch(),
			"switch_env", loginActorGenerationRebuildEnv,
			"error", err)
	}
}

// rebuildLoginActorGenerationAfterSceneCommit 是这条修复的正文：在客户端提交场景之后、
// 由客户端自己的下一条报文触发的这一刻，按**进副本那条路径的同一对载体**重建 actor 世代。
//
// actor 世代载体是两帧 `class0/op2`：
//  1. mode0（`buildCurrentSceneObjectListBodyForSessionInContextStrict`，owner =
//     `currentTownActorOwnerContext`）—— 与 `scene_post_start_map.go:341-365` /
//     `sendSelectedActorMode0AppearanceRefresh` 同一个构建器、同一个 owner 口径；
//  2. mode1（`buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(...,
//     true)`）—— 与 `scene_post_start_map.go:381-406`（进副本）**同一个构建器**，
//     带 31 条装备 create 行（槽 12 = 101040493、+10 耐久 48）。
//
// 世代重建成功后，仅补穿戴宠物的 durable op14；此前的刷新属于旧世代。
// 不重发宠物 table/growth 或容器引导。
func (s *Service) rebuildLoginActorGenerationAfterSceneCommit(session *gameSession, trigger string, charID uint16) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	loadedID, charName, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	if loadedID == 0 || loadedID != charID || !hasCharacter {
		return errLoginActorGenerationSelectedActorUnavailable(charID, loadedID, hasCharacter)
	}
	ownerChannel := currentTownActorOwnerContext(session)
	mode0Body, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(
		ctx,
		session,
		charID,
		charName,
		character,
		hasCharacter,
		ownerChannel,
	)
	if err != nil {
		return err
	}
	adventureSummary := s.currentAccountAdventureGroupSummaryForPacket(ctx, session, character, hasCharacter)
	mode1Body := s.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		ctx,
		session,
		character,
		hasCharacter,
		charID,
		uint32(adventureSummary.ManageLevel),
		ownerChannel,
		true,
	)
	createCount, slots, durability, nonzero, weapon, auditOK := loginActorGenerationMode1WireRowAudit(mode1Body)

	if err := s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		mode0Body,
		0,
	); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		mode1Body,
		0,
	); err != nil {
		return err
	}
	// 本档（DNFBRIDGE_SKILL_SWITCH_ANSWER_AT_SCENE_READY，默认 off ⇒ 零字节、零日志）：
	// 这条链【进出城镇与进副本都会走】，而"换装登记的 +10"在副本内不生效
	// （现场实测：副本内破极兵刃 Lv.16 = 基础 10 + 身上穿戴 +6，少了登记的 +10）。
	// 所以在装备数据发完之后，把登记应答也补一次 —— 让客户端在副本上下文里同样拿到登记。
	// 依据与取证见 skill_switch_inventory_downlink_record6.go 的「重进边界补发登记应答」段。
	s.answerSkillSwitchRegistrationAtSceneReady(session, trigger)
	// 2026-09-30 已移除：这里曾调用
	// `s.replayLoginSkillSwitchLoadoutAfterSceneCommit(...)`，在 actor 世代重建之后再补发
	// 一帧 `msg_id=1428`（「Buff 强化 / 技能换装」登记，第五轮边界修法）。
	//
	// 操作者真机验收（见 `_dist/buff-swap-equip-persistence.md` §15.1）：该帧确实发出去了两次
	// （`game-login-skill-switch-loadout-after-commit-sent` ×2），面板仍然为空 ⇒ **该修法被证伪**，
	// 随后已用 env 字面量回退（§15.2）。
	//
	// 这一行**故意不留在源码里**：只靠 env 关掉的话，任何一次环境重置/配置清理都会让它静默复活，
	// 把一帧已被验收证伪的下行重新发出去。删掉调用之后，**任何 env 取值都无法再发出这一帧** ——
	// 这比"保留读取 + 靠 env 关"更强。`DNFBRIDGE_LOGIN_SKILL_SWITCH_LOADOUT_AFTER_COMMIT`
	// 这个键**仅为兼容保留**（读取它只为日志，档位语义恒为「不发」），见
	// login_skill_switch_loadout_after_commit.go。

	fields := []any{
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", "after_login_scene_ready_first_client_packet",
		"char_id", charID,
		"object_key", currentSceneActorObjectKey(charID),
		"trigger", trigger,
		"frames", loginActorGenerationRebuildFrameSequence,
		"frame_count", 2,
		"msg_id", uint16(dnfenum.CmdPacketSetUDPIPPort),
		"classification", 0,
		"owner_channel", ownerChannel,
		"mode0_body_len", len(mode0Body),
		"mode0_body_source", "current_exe_sub_2008010_local_owner_mode0_scene_object_list_same_builder_as_scene_post_start_map",
		"mode1_body_len", len(mode1Body),
		"mode1_body_source", "current_exe_sub_2008010_local_owner_real_state_and_equipment_create_same_builder_as_scene_post_start_map",
		"mode1_stat_blob_len", currentMode1StatBlobWireSize,
		"mode1_row_walk", boolDigit(auditOK),
		"mode1_create_count", createCount,
		"actor_slot_count", len(slots),
		"actor_slots", strings.Join(slots, ","),
		"durability_carried", strings.Join(durability, ","),
		"nonzero_durability_carried", strings.Join(nonzero, ","),
		"actor_generation_authority", "mode0_mode1_actor_objects_are_the_authority_for_worn_state_and_stats_not_the_list_3_container",
		"boundary", loginActorGenerationRebuildBoundary,
		"ordering_position", loginActorGenerationRebuildPosition,
		"switch", loginActorGenerationRebuildSwitch(),
		"switch_env", loginActorGenerationRebuildEnv,
		"scene_path_evidence", loginActorGenerationRebuildScenePathEvidence,
		"crash_condition_excluded", loginActorGenerationRebuildCrashConditionExcluded,
	}
	fields = append(fields, weapon...)
	fields = append(fields,
		"row_facts_companion_event", "game-upper-selected-userinfo-mode1-equipment-create-rows",
	)
	s.logPacketEvent("game-login-actor-generation-rebuild-sent", fields...)
	// Actual r62 cold-login wire109/142 refreshed the pet before generation
	// rebuild147; the newly current slot26 cache then remained checksum-valid
	// zero. Restore repository-backed instance/enchant after this last mode1.
	if err := s.sendSelectedWornTitleEnchantRowRefresh(session, trigger+"_after_actor_generation_rebuild"); err != nil {
		return err
	}
	petErr := s.sendSelectedEquippedCreatureItemRefresh(session, trigger+"_after_actor_generation_rebuild")
	if petErr != nil {
		s.logPacketEvent("game-login-actor-generation-pet-refresh-failed",
			"conn_id", session.connID,
			"char_id", charID,
			"source", trigger+"_after_actor_generation_rebuild",
			"error", petErr)
	}
	buffErr := s.sendBuffSwapReapply(session, trigger+"_after_actor_generation_rebuild")
	if buffErr != nil {
		// Send/Write/provider returns are untrusted, including every joined leaf.
		// Keep the original object for errors.Join; only the log hashes its text.
		s.logPacketEvent("game-login-actor-generation-buff-refresh-failed",
			"conn_id", session.connID,
			"char_id", charID,
			"source", trigger+"_after_actor_generation_rebuild",
			"diagnostic_source", "buff_snapshot_send",
			"error", buffErr)
	}
	return errors.Join(petErr, buffErr)
}

type loginActorGenerationUnavailableError struct {
	charID       uint16
	loadedID     uint16
	hasCharacter bool
}

func (e loginActorGenerationUnavailableError) Error() string {
	return "login actor generation rebuild requires the selected actor: selected=" +
		strconv.Itoa(int(e.charID)) + " loaded=" + strconv.Itoa(int(e.loadedID)) +
		" found=" + strconv.FormatBool(e.hasCharacter)
}

func errLoginActorGenerationSelectedActorUnavailable(charID, loadedID uint16, hasCharacter bool) error {
	return loginActorGenerationUnavailableError{charID: charID, loadedID: loadedID, hasCharacter: hasCharacter}
}

// loginActorGenerationMode1WireRowAudit 从**已经装配好的 mode1 包体**里读回 actor 世代
// 的逐行事实：create 行数、每行的 actor 槽位、每行的耐久（行 +10 的 u16，
// 即客户端 `sub_1D77560` RVA 0x19776AB 真正读的那个字段）。
//
// 行宽按写入器自己的规则从字节里推：
//
//	+0 u8 actor 槽位 / +1 u32 itemID / +5 u32 实例 / +9 u8 ext / **+10 u16 耐久** /
//	+12 u32 链接（槽 9 且非 0 时多一段 u32 len + data）/ +16 u32 aux / +20 u8 / +21 u8 /
//	+22 u16 / （actor 槽位 <= 0x0b 时两段 u32 len + data）/ （=26 时 u32 上限覆盖）/
//	sub_1D6E020 计数 + n*8 / u32 / sub_225CCA0 计数 + n*4 / u16 / 索引态（u8 n + n*3
//	+ (n>0 ? 2 : 0) + (选中索引 != 0xff ? 4 : 0)）/ 3+2+5 / 两段 u32 len + data /
//	u8 / 一段 u32 len + data。
//
// 判据与写入器 `userinfo_current_equipment_wire.go:11-56` 的写法一一对应；
// 走完之后必须落在"create 行之后那个恒为 0 的 u32 终态字"上，并且尾巴长度等于
// `loginActorGenerationRebuildMode1FixedTailSize`（无 update 行、技能页默认档）。
// 两条自检任一不成立就返回 ok=false —— 宁可在日志里写"没读出来"，
// 也不许把一段错位的字节读成一组看似合理的耐久。
func loginActorGenerationMode1WireRowAudit(body []byte) (
	createCount int,
	slots []string,
	durability []string,
	nonzero []string,
	weapon []any,
	ok bool,
) {
	weapon = []any{"actor_weapon", "none"}
	if len(body) < currentMode1CreateRowsOffset+4 || body[0] != 1 {
		return 0, nil, nil, nil, weapon, false
	}
	createCount = int(body[currentMode1CreateCountOffset])
	position := currentMode1CreateRowsOffset
	readBlock := func(p int) (int, bool) {
		if p+4 > len(body) {
			return 0, false
		}
		n := int(int32(binary.LittleEndian.Uint32(body[p : p+4])))
		if n < 0 || p+4+n > len(body) {
			return 0, false
		}
		return p + 4 + n, true
	}
	for index := 0; index < createCount; index++ {
		start := position
		if start+16 > len(body) {
			return createCount, slots, durability, nonzero, weapon, false
		}
		actorSlot := int16(body[start])
		itemID := binary.LittleEndian.Uint32(body[start+1 : start+5])
		word := binary.LittleEndian.Uint16(body[start+10 : start+12])
		linked := binary.LittleEndian.Uint32(body[start+12 : start+16])
		p := start + 16
		if actorSlot == 9 && linked != 0 {
			stepped, stepOK := readBlock(p)
			if !stepOK {
				return createCount, slots, durability, nonzero, weapon, false
			}
			p = stepped
		}
		p += 8
		if actorSlot <= 0x0b {
			stepped, stepOK := readBlock(p)
			if !stepOK {
				return createCount, slots, durability, nonzero, weapon, false
			}
			p = stepped
			stepped, stepOK = readBlock(p)
			if !stepOK {
				return createCount, slots, durability, nonzero, weapon, false
			}
			p = stepped
		}
		if actorSlot == 26 {
			p += 4
		}
		if p+1 > len(body) {
			return createCount, slots, durability, nonzero, weapon, false
		}
		vector1Count := int(body[p])
		p += 1 + vector1Count*currentMode1EquipmentVectorRecordSize
		p += 4
		if p+1 > len(body) {
			return createCount, slots, durability, nonzero, weapon, false
		}
		vector2Count := int(body[p])
		p += 1 + vector2Count*4
		p += 2
		if p+1 > len(body) {
			return createCount, slots, durability, nonzero, weapon, false
		}
		indexedCount := int(body[p])
		p += 1 + indexedCount*3
		if indexedCount > 0 {
			if p+2 > len(body) {
				return createCount, slots, durability, nonzero, weapon, false
			}
			selectedIndex := body[p+1]
			p += 2
			if selectedIndex != 0xff {
				p += 4
			}
		}
		p += 3 + 2 + 5
		stepped, stepOK := readBlock(p)
		if !stepOK {
			return createCount, slots, durability, nonzero, weapon, false
		}
		p = stepped
		stepped, stepOK = readBlock(p)
		if !stepOK {
			return createCount, slots, durability, nonzero, weapon, false
		}
		p = stepped
		p++
		stepped, stepOK = readBlock(p)
		if !stepOK {
			return createCount, slots, durability, nonzero, weapon, false
		}
		p = stepped
		if p > len(body) {
			return createCount, slots, durability, nonzero, weapon, false
		}
		slots = append(slots, strconv.Itoa(int(actorSlot)))
		durability = append(durability, strconv.Itoa(int(actorSlot))+":"+strconv.Itoa(int(word)))
		if word != 0 {
			nonzero = append(nonzero, strconv.Itoa(int(actorSlot))+":"+strconv.Itoa(int(word)))
		}
		if actorSlot > 0x0b && word != 0 && len(weapon) == 2 {
			weapon = []any{
				"actor_weapon", "present",
				"actor_weapon_slot", strconv.Itoa(int(actorSlot)),
				"actor_weapon_item_id", strconv.FormatUint(uint64(itemID), 10),
				"actor_weapon_durability_sent", strconv.Itoa(int(word)),
			}
		}
		position = p
	}
	// 自检一：create 行之后紧跟的 u32 终态字恒为 0（`userinfo_current_modes.go:195`）。
	if position+4 > len(body) || binary.LittleEndian.Uint32(body[position:position+4]) != 0 {
		return createCount, slots, durability, nonzero, weapon, false
	}
	// 自检二：尾巴长度必须等于固定尾巴（走位错一格就会被抓住）。
	if len(body)-position != loginActorGenerationRebuildMode1FixedTailSize {
		return createCount, slots, durability, nonzero, weapon, false
	}
	return createCount, slots, durability, nonzero, weapon, true
}
