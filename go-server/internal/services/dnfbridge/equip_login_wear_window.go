// 本文件只负责一件事：把 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP（默认关闭）这条
// 兜底开关所需的"登录引导窗口"状态界定清楚，并在 op19 的 aligned 请求上透传。
//
// 现场（HK 线上 2026-09-27 09:44，角色 6 test78，`packet_log.20260927-094833.txt`
// 会话 game-000020 pkt_seq=127，客户端 `90CN_protocol.log` 同一时刻 pid=45316）：
//
//	RECV type=19 body=12000000000001000000030c006dc1050600000000ffffffff0000
//	  src=(list0 主背包,18,IV=0) dst=(list3 装备,12,IV=101040493)
//	⇒ 旧口径按"两端占用"反转端点 ⇒ equipMove=unequip，把穿着的武器卸进背包。
//
// 为什么不能无条件改判据（本轮实测+厂商对照）：
//   - 厂商真机（YKCN S4A21 `CipherPacket.log:1439` + `GameLog.log:2184`）在同一
//     登录窗口发的 op19 是 `00 1C 00 CA 65 00 00 01 00 00 00 03 0D 00 58 8D DC 17 …`
//     = src=(0,28,IV=26058) dst=(3,13,IV=0x17DC8D58)：源端实例非零。
//     厂商服务端 IsEquipmentClientEchoWhenSourceMissing 因此能用
//     SourceInstanceValue > 0 认出"客户端复述穿戴态"。
//   - 我们的客户端在 13 次完成登录收尾的连接里对角色 6 发了 11 次上面那个形状，
//     而角色 1：0/61、角色 3：0/41、角色 4：1/67 ⇒ 它是角色数据驱动的、
//     客户端主动请求把该装备挪下来，与厂商复述不是同一族包；
//     按厂商口径执行（卸下）在厂商服务端上也是同样结果，所以判据本身没错。
//
// 因此这里只提供默认关闭、可一键回退的兜底：窗口内、且这条包只可能由
// "客户端指名装备槽上那件物品"构成时判 noop（判据在
// internal/modules/dnf/equip/owner.go 的 equipmentLoginWearReassertNoop）。
//
// 窗口界定（只用服务端可观测事件，全部有现场证据）：
//   - 开启：本会话收到客户端的场景就绪应答（legacy type=1345，u32(2) 形状，
//     即 game-legacy-town-scene-ready-ack-accepted）的那一刻。13 次登录 op19
//     全部落在这条 ack 之后约 1 秒内（最远 1.6s）。
//   - 关闭（取先到）：
//     1) 一次性（仅 WINDOW=once 回退档）：本会话第一条 op19 消费掉窗口；
//     2) 期限：开启后 DNFBRIDGE_EQUIP_LOGIN_REASSERT_WINDOW_MS 内（默认 15s，回退 5s）。
//   - 窗口关闭后，op19 的判定与今天完全一致。
//
// 2026-09-28 第二轮（现场复发，`_dist/weapon-unequip-diagnosis.md` §1.5/§3.2）：
// "一次性 + 5 秒"这两个口径被现场证伪为**覆盖不全**的根因 —— conn `game-001200`
// 里两条**逐字节同形**的 28 字节 op19（61423 / 61575）第一条判 noop、第二条真卸，
// 差别只是窗口已被第一条消费；全服当天 65 次开窗只换来 26 次
// `noopGate=login_wear_reassert_noop`。因此改为**窗口内可重复命中**（默认 repeat），
// 并把时长放宽到 15s（现场该族包最晚出现在场景就绪 ack 之后约 3.1s）。
//
// 两个开关（都在运行时读 env，一键回退，默认取修复档）：
//
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_WINDOW=repeat|once
//	    repeat（默认）= 窗口在整个时长内对该族 op19 可重复命中：
//	        不再写 equipLoginWearWindowConsumed，只由超时关闭；
//	    once = 一键回到今天：本会话第一条 op19 消费掉窗口（Consumed）。
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_WINDOW_MS=<毫秒>
//	    默认 15000；写 5000 即逐字节回到今天的时长。任何正整数都接受，
//	    上限 loginWearReassertWindowMaxMillis（60s）；非法/≤0 回落到默认。
//	    它只是调参旋钮，不改变档位语义。
//
// **残余风险（必须知情）**：窗口（15s）内操作者**手动**把槽 12 那件拖下来，
// 这条真实脱下会被判 noop；客户端按 moveValue=0 回滚，**不丢数据**，但要再拖一次。
// 想消除它就把 WINDOW_MS 往下调（例如 5000）或 WINDOW=once。
//
// 2026-09-28 第三轮（`_dist/weapon-unequip-stutter-round4.md` §2.3/§2.6）：上面的
// "残余风险"只覆盖窗口**之内**；现场证明窗口**之外**（+46 s / +62 s / +68 s）到达的
// 同族包会走 `equip/owner.go:1046` 真落库卸下。范围收口已拆成两层、实现在
// equip_login_wear_reassert_anchor.go：
//
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_ANCHOR=slot_refresh|scene_ready
//	    默认 slot_refresh = 锚点改成"该装备槽最近一次被权威刷新"（覆盖晚到的复述）；
//	    scene_ready = 一键回退到今天（只认下面这个会话窗口）。
//	DNFBRIDGE_EQUIP_LOGIN_REASSERT_PER_SLOT=on|off
//	    默认 on = 每个装备槽每会话只有一个复述处理窗口（5000 ms，起点不滑动），
//	    窗口过后该槽不再吸收 ⇒ 手动脱下的暴露面从"15 s 内每一条"降到"每槽最多一次"；
//	    off = 一键回退到今天。
//
// **本文件的语义、时长与副作用一个字都没改**：WINDOW/WINDOW_MS 仍然只描述这个会话级
// 窗口；上面两个新开关只决定"窗口之外还允不允许吸收"与"每槽还允不允许吸收"。
// 回退档（scene_ready + off）下的闸门判定与本文件改动前逐字节相同。
package dnfbridge

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// 登录引导窗口的两个开关名与档位取值。
const (
	loginWearReassertWindowModeEnv   = "DNFBRIDGE_EQUIP_LOGIN_REASSERT_WINDOW"
	loginWearReassertWindowMillisEnv = "DNFBRIDGE_EQUIP_LOGIN_REASSERT_WINDOW_MS"

	// loginWearReassertWindowRepeat 默认档：窗口在整个时长内可重复命中。
	loginWearReassertWindowRepeat = "repeat"
	// loginWearReassertWindowOnce 回退档：今天的一次性消费。
	loginWearReassertWindowOnce = "once"
)

// 时长口径（毫秒）。
const (
	// loginWearReassertWindowDefaultMillis 是默认（修复档）窗口时长。
	// 依据：现场该族包最晚出现在场景就绪 ack 之后约 3.1s（game-001200 的 61575），
	// 15s 覆盖了它的 5 倍余量，同时仍短于"玩家看清画面 → 点开背包 → 手动拖装备"
	// 的常见时间尺度。
	loginWearReassertWindowDefaultMillis = 15000
	// loginWearReassertWindowLegacyMillis 是 2026-09-25..09-28 的现场时长（回退档写它）。
	loginWearReassertWindowLegacyMillis = 5000
	// loginWearReassertWindowMaxMillis 是硬上限：再长只会继续放大"窗口内手动脱下
	// 被判 noop"的风险面，没有现场依据支持。
	loginWearReassertWindowMaxMillis = 60000
)

// loginWearReassertWindowRepeatEnabled 回答窗口是否为"可重复命中"档（默认）。
// 只有**显式**写 once（及其别名）才回退到今天的一次性消费；其余取值（含未设置、
// repeat、写错的字串）都是修复档 —— 默认即修复，回退必须显式。
func loginWearReassertWindowRepeatEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginWearReassertWindowModeEnv))) {
	case loginWearReassertWindowOnce, "single", "one_shot", "oneshot", "legacy", "off", "0", "false", "no", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// loginWearReassertWindowMode 只用于启动日志/诊断。
func (s *Service) loginWearReassertWindowMode() string {
	if loginWearReassertWindowRepeatEnabled() {
		return loginWearReassertWindowRepeat
	}
	return loginWearReassertWindowOnce
}

// loginWearReassertWindowDurationMillis 解析生效的窗口毫秒数（运行时读 env）。
// 空值、非数字、≤0 一律回落默认 15000；超过上限卡到上限。
func loginWearReassertWindowDurationMillis() int {
	parsed, err := strconv.Atoi(strings.TrimSpace(os.Getenv(loginWearReassertWindowMillisEnv)))
	if err != nil || parsed <= 0 {
		return loginWearReassertWindowDefaultMillis
	}
	if parsed > loginWearReassertWindowMaxMillis {
		return loginWearReassertWindowMaxMillis
	}
	return parsed
}

// loginWearReassertWindowDuration 是上面那个毫秒数的 time.Duration 形式
// （判定与启动回显共用一处真相）。
func (s *Service) loginWearReassertWindowDuration() time.Duration {
	return time.Duration(loginWearReassertWindowDurationMillis()) * time.Millisecond
}

// equipLoginWearReassertNoopEnabled 是开关的桥侧副本（判断是否需要维护窗口状态）。
// 与 equip 包的同名判定保持同一组真值字符串。
func equipLoginWearReassertNoopEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(envString("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", ""))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// openEquipLoginWearWindow 在场景就绪 ack 收到时开启窗口（每会话一次）。
// 开关关闭时什么都不做，现场日志与行为保持今天的样子。
func (s *Service) openEquipLoginWearWindow(session *gameSession, source string) {
	if session == nil || !equipLoginWearReassertNoopEnabled() {
		return
	}
	session.equipLoginWearWindowOpenedAt = s.gameplayNow()
	session.equipLoginWearWindowConsumed = false
	// 每一次场景就绪 ack 同时推进"复述轮次"：客户端又走了一遍进场景，它这一遍的复述串
	// 会重述同一批穿戴格。只有默认档（ROUND=absorb）才推进；回退档（session）下这个
	// 计数恒为 0，每槽账本因此仍然是"每会话一次"，与今天逐字节相同。
	// 见 equip_login_wear_reassert_round.go。
	if loginWearReassertRoundAbsorbEnabled() {
		session.equipLoginWearReassertRound++
	}
	s.logGameEvent(session, "game-equip-login-wear-window-opened",
		"source", source,
		"window_ms", loginWearReassertWindowDurationMillis(),
		"window_mode", s.loginWearReassertWindowMode(),
		// 本会话生效的**范围收口档**（锚点 + 每槽），见 equip_login_wear_reassert_anchor.go。
		// 这两个键在两档下都打：档位可观测性不许依赖档位本身。
		"anchor", s.loginWearReassertAnchor(),
		"per_slot", s.loginWearReassertPerSlot(),
		"per_slot_window_ms", loginWearReassertEchoIntervalMillis,
		// 第四层收口（轮次）的档位与本轮轮次号：见 equip_login_wear_reassert_round.go。
		// 同样在两档下都打。
		"round_mode", s.loginWearReassertRound(),
		"round", session.equipLoginWearReassertRound,
		"switch", "DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP")
}

// equipLoginWearWindowOpenForOp19 判定这条 op19 是否落在窗口内。
//
// repeat 档（默认）：窗口在整个时长内可重复命中，**不**写 Consumed
// （现场 game-001200 的第二条同形包就是被一次性消费挡在门外才变成真卸下的）。
// once 档（回退）：逐字节等于今天 —— 本会话第一条 op19 消费掉窗口。
//
// 形状作用域（"只有复述穿戴态那一族才许消费窗口"）不在本函数里，而在调用点
// equipLoginWearFamilyWindowOpenForOp19（game_aligned.go 的 op19 分支）：
// 形状外的 op19 连本函数都不会被调用，因此既不消费也不延续窗口。
func (s *Service) equipLoginWearWindowOpenForOp19(session *gameSession) bool {
	if session == nil || !equipLoginWearReassertNoopEnabled() {
		return false
	}
	if session.equipLoginWearWindowOpenedAt.IsZero() {
		return false
	}
	if s.gameplayNow().Sub(session.equipLoginWearWindowOpenedAt) > s.loginWearReassertWindowDuration() {
		// 超时即关闭：标记消费掉，使窗口不会被后续流量"续期"。
		session.equipLoginWearWindowConsumed = true
		return false
	}
	if session.equipLoginWearWindowConsumed && !loginWearReassertWindowRepeatEnabled() {
		return false
	}
	session.equipLoginWearWindowConsumed = true
	return true
}
