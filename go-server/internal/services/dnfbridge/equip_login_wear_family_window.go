// 本文件是"登录引导窗口"（DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP）兜底在 **dnfbridge
// 调用点** 上的两处收口。窗口自身的语义与时长在 equip_login_wear_window.go（本轮
// 经操作者显式授权后可改）；equip/owner.go 的闸门判据一个字都没动。
//
//  1. 形状作用域（缺口 b）：窗口只允许被**该族形状**的 op19 消费/延续，
//     普通背包 op19（同容器搬运、宠物搬运、换装 swap…）既不消费也不延续窗口；
//  2. noop 分支的权威刷新开关（放大器）：DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH
//     （默认 on）由这里在运行时读取，并透传到域层的 noop 应答。
//
// 现场（`_dist/weapon-unequip-diagnosis.md` §1.5，conn `game-001200`，≈15:09:46 CST）：
// 同会话两条**逐字节同形**的 28 字节 op19（61423 / 61575）——第一条判 noop、
// 第二条真卸（`equipMove=unequip changed=true`），因为窗口已被 61423 一次性消费。
// 今天全服 65 次开窗只换来 26 次 `noopGate=login_wear_reassert_noop`。
//
// 形状（A 族）由客户端自己写在包里的字段界定，与 owner.go 的
// equipmentLoginWearReassertNoop 的"客户端可声明部分"逐条对应：
//
//	src list ∈ {0 主背包, 1 装扮栏}   src 实例 == 0
//	dst list == 3（装备）            dst 实例 != 0（客户端在装备端点指名那件物品）
//	moveCount == 1
//
// 权威占用（背包端点为空的确认）仍然只由 equip/owner.go 在事务里判 —— 桥侧不得
// 复制那份判据，否则就会出现"桥说是复述、域说是真移动"的两套口径。
//
// 第二条缺口（noop 回包不带任何权威状态）的修法在域侧：
// `inventory/handler.go` 的 ownerAppliedEquipmentMoveResult 在命中本条闸门时补发
// 与真落库路径相同的后置刷新（整容器动作 + mode0 外观 + 战力 413），由上面的开关控制。
//
// 两档（默认 + 一键回退）：
//
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH=on（默认，含未设置）
//	    noop 分支照发真落库路径那份权威刷新 ⇒ 客户端本地视图收敛、不再重发该族包；
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH=off（及 0/false/no/legacy/disable/none）
//	    noop 分支逐字节回到今天：只回一条 `msg_id=19`（moveValue=0）ACK。
//
// 2026-09-28 第三轮（上线后 16:56–17:11 现场，`_dist/weapon-unequip-stutter-round4.md`
// §2.3/§2.6）：15 秒窗口**之外**到达的同族 op19 仍然走 `equip/owner.go:1046` 的反转端点
// 兜底并真落库（角色 6 槽 17 100270515、角色 1 槽 12 101010856，时距 +46 s / +62 s / +68 s）。
// 于是把范围收口拆成两层，实现在 equip_login_wear_reassert_anchor.go：
//
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_ANCHOR=slot_refresh（默认 = 修复档）
//	    锚点从"场景就绪 ack"改成"该装备槽最近一次被权威刷新"：只要这一格还是服务端
//	    最后一次告诉客户端的那个样子，晚到的复述仍然判 noop；
//	    scene_ready（及 scene/ack/legacy/off/0/false/no/disable/disabled/none）
//	    = 一键回退到今天（只认会话窗口）。
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_PER_SLOT=on（默认 = 修复档）
//	    每个装备槽每会话只有一个复述处理窗口（默认 5000 ms，起点不滑动）；
//	    窗口过后该槽本会话不再吸收 ⇒ 玩家手动脱下最多被吃掉一次，之后一定能生效；
//	    off（及 0/false/no/legacy/disable/disabled/none）
//	    = 一键回退到今天（不按槽结算，窗口内每一条都吸收）。
//
// 两个回退字面量一起写 = 逐字节回到今天的闸门判定。
//
// 2026-09-28 第四轮（上线后 20:29-20:32 CST 现场，`packet_log.txt:77924` / `:78228`）：
// 客户端**又进了一次场景**（新的 legacy type=1345 ack）之后重述同一句"槽 10 上是 101600023"，
// 只是把点名的空端点从 142 换成 143；第三层把源端点算进身份 ⇒ 判不出是同一次复述，
// 而上一轮的每槽时钟早已过期 ⇒ 真落库卸下。第四层收口把它认出来：
//
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_ROUND=absorb（默认 = 修复档）
//	    新一轮里第一次重述同一句**装备端点**陈述 ⇒ 重开该槽窗口（每槽每轮最多一次）；
//	session（及 off/0/false/no/legacy/disable/disabled/none）
//	    = 一键回退到今天（轮次不推进，每槽每会话一次）。
//	    见 equip_login_wear_reassert_round.go：该文件同时说明为什么"点名的空端点不是身份"。
//
// 主开关 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP 保持现状（线上 `/opt/dnf90/dnf90.env:67` = 1）。
package dnfbridge

import (
	"os"
	"strings"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

const (
	// equipLoginWearReassertNoopRefreshEnv 是"noop 分支是否补权威刷新"的开关名。
	equipLoginWearReassertNoopRefreshEnv = "DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH"

	// 形状判定用的 wire list type。桥侧已有 currentSocketListEquipment(=3)；
	// 主背包 0 与装扮栏 1 在这里按现场 op19 的形状就地声明。
	loginWearFamilySourceListMain   byte = 0
	loginWearFamilySourceListAvatar byte = 1
)

// equipLoginWearReassertNoopRefreshEnabled 是 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH
// 的**运行时**读取（每次调用都读环境变量，不缓存、不进 options.go，因此线上改档
// 不需要重新编译，与 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP 的读法一致）。
//
// 只有**显式**写 off（及其别名）才回退到"只回 msg_id=19"；其余取值（含未设置、
// on、1、true、写错的字串）都是修复档 —— 默认即修复，回退必须显式。
func equipLoginWearReassertNoopRefreshEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(equipLoginWearReassertNoopRefreshEnv))) {
	case "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// equipLoginWearReassertNoopRefreshMode 只用于启动日志/诊断。
func equipLoginWearReassertNoopRefreshMode() string {
	if equipLoginWearReassertNoopRefreshEnabled() {
		return "on"
	}
	return "off"
}

// equipLoginWearReassertNoopRefreshRequested 是上面那个纯函数在 Service 上的透传。
// nil Service 回落修复档（与 equipAppearanceRefreshUsesMode0 的零值口径一致）。
func (s *Service) equipLoginWearReassertNoopRefreshRequested() bool {
	if s == nil {
		return true
	}
	return equipLoginWearReassertNoopRefreshEnabled()
}

// loginWearReassertFamilyOp19Shape 判定一条 op19 body 是否属于"登录复述穿戴态"这一族
// （现场 A 形状）。它只用客户端**自己声明**的字段，不读库、不判占用：
//
//	0113 0029 … 0000 0a00 00000000 01000000 03 0c00 6dc10506 00000000 ffffffff 0000
//	             src=(list0,slot10,IV=0) count=1 dst=(list3,slot12,IV=101040493)
//
// 解析复用 dnfinventory.DecodeMoveItemspaceRequest（28 字节当前 EXE 布局，桥侧
// item_slot_family_guard.go 已在用同一份解码器），因此偏移量只有一处真相。
// 长度不是 28 字节（解码失败）一律**不**算这一族 —— 形状不明的包不许吃窗口。
func loginWearReassertFamilyOp19Shape(body []byte) bool {
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return false
	}
	if request.SourceListType != loginWearFamilySourceListMain &&
		request.SourceListType != loginWearFamilySourceListAvatar {
		return false
	}
	if request.DestinationListType != currentSocketListEquipment {
		return false
	}
	if request.MoveCount != 1 {
		return false
	}
	if request.SourceInstanceValue != 0 || request.DestinationInstanceValue == 0 {
		return false
	}
	return true
}

// equipLoginWearFamilyWindowOpenForOp19 是 game_aligned.go op19 分支的调用点助手，
// 回答"这条 op19 是否可以按登录复述兜底处理"（透传到 alignedcmd.Request.LoginWearWindowOpen）。
//
// 顺序就是判据：
//   - 形状不符 ⇒ 直接 false，**连窗口函数都不调用** ⇒ 不吃窗口、不延续窗口（缺口 b）；
//   - 形状相符 ⇒ 先算今天那套**会话级窗口**（equip_login_wear_window.go，语义、时长与
//     副作用一个字都没改），再由 equip_login_wear_reassert_anchor.go 的两层范围收口
//     决定"窗口之外还允不允许吸收"：
//     锚点（默认 slot_refresh = 该装备槽的权威纪元）+ 每槽（默认 on = 每槽每会话
//     只有一个复述处理窗口）。现场 +46 s / +62 s / +68 s 那几条真卸就是这个缺口。
//
// 这里不复制窗口起点/时长/档位：窗口语义只有 equip_login_wear_window.go 一处真相；
// 每槽账本只有 equip_login_wear_reassert_anchor.go 一处真相。
//
// 2026-09-28 第五轮：本函数拆成"判读日志 + 吸收判定"两半。**判据、顺序与返回值
// 一个字节都没改**（判读那一半先做形状检查，再调吸收那一半；主开关关闭时同样先
// 做形状检查再返回 false），只是把这一族的每一条 op19 写进一行
// `EVENT kind=game-equip-direction-judgement`（见 equip_direction_judgement.go），
// 让"这次登录的复述包是被窗口层吸收的、还是被域层的方向判定吸收的"一眼可判。
func (s *Service) equipLoginWearFamilyWindowOpenForOp19(session *gameSession, body []byte) bool {
	if session == nil || !loginWearReassertFamilyOp19Shape(body) {
		return false
	}
	absorbed := s.equipLoginWearFamilyAbsorbForOp19(session, body)
	// 形状已确认（28 字节 op19），解码失败在形状判定里已被排除；失败则不打这一行。
	if request, err := dnfinventory.DecodeMoveItemspaceRequest(body); err == nil {
		s.logEquipDirectionJudgement(session, request, absorbed)
	}
	return absorbed
}

// equipLoginWearFamilyAbsorbForOp19 是上面那条函数"判定"的那一半（原函数体，
// 语义一个字都没改）：主开关、会话窗口、槽锚点、每槽、轮次四层收口全在这里。
func (s *Service) equipLoginWearFamilyAbsorbForOp19(session *gameSession, body []byte) bool {
	if !equipLoginWearReassertNoopEnabled() {
		return false
	}
	if !loginWearReassertFamilyOp19Shape(body) {
		return false
	}
	// 重试档（方案 C，默认）：同一个装备槽在本登录纪元里第 2 条及以后的同形包
	// 不是登录批的复述，而是玩家拖不动在重试 ⇒ 桥侧不再吸收，把决定权交回域侧
	// （域侧两条闸门看同一个序号）⇒ 反转端点兜底执行厂商口径的真·脱下。
	// 序号由 game_aligned.go 在分发前记一次（noteWornSlotClaimOccurrence）；
	// 0 = 未计数（非形状 b / 开关写 all / 测试字面量）⇒ 行为与今天逐字节一致。
	// 见 equip_worn_slot_claim_occurrence.go。
	if session != nil && session.equipWornSlotClaimOccurrence > 1 {
		return false
	}
	// 命中前窗口是否已被消费，只用于现场日志（repeat 档下"第 2..N 条"是可观测事实）。
	// 必须在窗口判定**之前**取：默认 repeat 档下窗口函数每次命中都会置 Consumed。
	repeated := session.equipLoginWearWindowConsumed
	inWindow := s.equipLoginWearWindowOpenForOp19(session)
	scope := s.loginWearReassertScopeForOp19(session, body, inWindow)
	if !scope.absorb {
		return false
	}
	if scope.round {
		// 第四层收口（默认档，见 equip_login_wear_reassert_round.go）：该槽的每槽时钟早已
		// 过期，但客户端**又收到过一次场景就绪 ack**（新一轮），并且这一轮里重述的还是同一句
		// 装备端点陈述（点名的空端点从 142 换成了 143 —— 源端点不是身份）。
		//
		// 现场：会话 game-000009，10:41:01.145 靠每槽锚点吸收了 src=(1,142) 那条，
		// 10:41:35.780 又来一次场景就绪 ack（新一轮），10:41:41.750 的 src=(1,143) 落在
		// 新会话窗口**之内**（+5.97 s）却被上一轮的每槽时钟否决 ⇒ 真落库卸下 101600023。
		// 这条事件是操作者复验本档是否生效的第一手判据：
		// 命中它 = 那件装备没被卸下；看不到它且 `equipMove=unequip` 又出现 = 回退档或未生效。
		s.logGameEvent(session, "game-equip-login-wear-round-reassert-absorbed",
			"reason", "same_worn_endpoint_statement_restated_in_a_new_scene_entry_round",
			"anchor", s.loginWearReassertAnchor(),
			"per_slot", s.loginWearReassertPerSlot(),
			"repeat", s.loginWearReassertRepeat(),
			"round_mode", s.loginWearReassertRound(),
			"late", scope.late,
			"worn_slot", scope.slot,
			"round", session.equipLoginWearReassertRound,
			"absorbed_round", scope.absorbedRound,
			"window_ms", loginWearReassertWindowDurationMillis(),
			"per_slot_window_ms", loginWearReassertEchoIntervalMillis,
			"evidence", "relaunch_10_41_41_750_rotated_nomination_142_to_143_real_unequip_of_101600023")
		return true
	}
	if scope.late {
		// 会话窗口之外、由**每槽锚点**吸收：现场 09:09:20.875025325Z（角色 6 槽 17）与
		// 09:01:49.314243779Z（角色 1 槽 12）就是这一类包真落库卸下的。
		// 这条事件是操作者复验默认档是否生效的唯一直接判据（见本轮报告第 7 节）。
		s.logGameEvent(session, "game-equip-login-wear-slot-reassert-absorbed",
			"reason", "late_family_op19_inside_the_worn_slot_authority_epoch",
			"anchor", s.loginWearReassertAnchor(),
			"per_slot", s.loginWearReassertPerSlot(),
			"repeat", s.loginWearReassertRepeat(),
			"worn_slot", scope.slot,
			"window_ms", loginWearReassertWindowDurationMillis(),
			"per_slot_window_ms", loginWearReassertEchoIntervalMillis,
			"evidence", "weapon_unequip_stutter_round4_2_3_late_family_op19_really_unequipped")
		return true
	}
	if scope.repeat {
		// 每槽时钟已过期、但**与该槽最近一次被吸收的那条陈述逐字段相同**的重复：
		// 2026-09-28 18:32 部署件上线后操作者报的"重新登陆还会卸下"就是这一类
		// （会话 game-000006 的 10:37:20.896 与 10:38:29.707，两条都与 10:37:14.350
		// 那条逐字节同形）。这条事件是操作者复验本档是否生效的第一手判据：
		// 命中它 = 武器没被卸下；看不到它且 `equipMove=unequip` 又出现 = 回退档或未生效。
		s.logGameEvent(session, "game-equip-login-wear-repeat-reassert-absorbed",
			"reason", "identical_family_op19_statement_after_the_per_slot_window",
			"anchor", s.loginWearReassertAnchor(),
			"per_slot", s.loginWearReassertPerSlot(),
			"repeat", s.loginWearReassertRepeat(),
			"worn_slot", scope.slot,
			"window_ms", loginWearReassertWindowDurationMillis(),
			"per_slot_window_ms", loginWearReassertEchoIntervalMillis,
			"evidence", "relog_diagnosis_10_37_20_896_and_10_38_29_707_real_unequips_of_101040493")
		return true
	}
	if repeated {
		// 现场 61423 noop → 61575 unequip 之间那一步：让 packet_log 能直接读出
		// "兜底覆盖到了第 N 条"，而不必去数 equipMove=unequip 有没有再出现。
		s.logGameEvent(session, "game-equip-login-wear-window-family-repeat",
			"reason", "identical_family_op19_within_window_after_the_first_hit",
			"window_mode", s.loginWearReassertWindowMode(),
			"anchor", s.loginWearReassertAnchor(),
			"per_slot", s.loginWearReassertPerSlot(),
			"evidence", "weapon_unequip_diagnosis_1_5_two_byte_identical_packets_one_noop_one_unequip")
	}
	return true
}
