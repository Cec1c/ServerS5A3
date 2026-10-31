package dnfbridge

import (
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件修的是"自制史诗 NPC（史诗制作者）选择武器 → 客户端弹『用户信息错误』"。
//
// 一、现场（香港 192.0.2.12，/opt/dnf90/logs/packet_log.txt 2026-09-25，当前这份日志
// 由 19:26 的部署轮转而来；操作者就是在这个会话里点的武器）：
//
//	:2152 RECV kind=game-legacy conn_id=game-000016 channel_id=253 cmd=1 type=1417
//	      runtime_cmd_name=ENUM_CMDPACKET_EPIC_PRODUCTION_START_FINISH
//	      header_seq=90 body_len=4 len=17 hex=018905110000004d7854e85a0008f56f06
//	:2155 EVENT kind=game-upper-epic-production-start-blocked conn_id=game-000016
//	      channel_id=253 target_item_id=108000520 character_id=3
//	      reason=dnf epic production request is invalid
//	:2157 SEND kind=game-upper conn_id=game-000016 pkt_seq=286 msg_id=1417
//	      plain_body_len=2 len=18 hex=018905120000007a3d5fad50000000000003
//
// 逐字段读：
//   - RECV header = 13 字节（class=01 / type=0x0589=1417 / len=0x11=17 / counter /
//     header_seq=0x5a=90 / 1 字节填充），body = 4 字节 `08 f5 6f 06` = 小端 108000520
//     = **席蒿待罪**（真 PVF：equipment/character/priest/weapon/cross/108000520.equ，
//     `[equipment type] [weapon]`、`[custom ability type] epic production`、
//     `[usable job] [priest] [at priest]`；etc/epicproduction/epicproduction.etc 的
//     `[at priest] [indexes]` 第一项就是它）。⇒ 请求 body 形状与
//     decodeCurrentEpicProductionStartRequest（恰好 4 字节）一致，解析没有问题。
//   - SEND header = 16 字节（含 4 字节填充），body = `00 03` = 失败标志 0 + 失败码 3
//     （currentEpicProductionGenericError）。body 里**没有任何文本**，所以客户端弹出的
//     「用户信息错误」只可能是**客户端本地**按失败码取的文案，不是我们下发的字符串。
//   - 事件字段只有 `target_item_id / character_id / reason`（没有 job、没有 job_tag）
//     ⇒ 命中的是 handleCurrentEpicProductionStart 里"角色读取/账号归属"那一支，
//     即 epic_production.go 的 `character.AccountID != s.accountID()`。
//
// 二、根因：账号口径取错（共享兜底账号 vs 会话账号）
//
//	session（RECV 同会话）：
//	  :1460 EVENT kind=game-login-token-account-bound conn_id=game-000016
//	        account_id=dnf:10001 account_changed=false
//	角色行（香港库 dnf_characters）：character_id=3 → account_id=dnf:10001
//	服务端 env：DNFBRIDGE_ACCOUNT_ID=dnf:1（共享兜底账号）
//
//	而 `s.accountID()`（repository_access.go:16）返回的是 **options.accountID / 本地
//	单账号 profile 兜底账号**（香港 = dnf:1），不是会话账号。于是
//	`dnf:10001 != dnf:1` ⇒ 恒不匹配 ⇒ 通用失败码 3 ⇒ 客户端弹「用户信息错误」。
//
//	同一缺陷的第二次独立现场（更早一轮，packet_log.20260924-195607.txt:15768-15779）：
//	target_item_id=101040510（PVF `[swordman]/[at swordman]` 第 5 项）character_id=2
//	（香港库 account_id=dnf:10005）、同样 `reason=dnf epic production request is invalid`
//	+ 同样的 `00 03` 回包 ⇒ 两把不同的武器、两个不同的角色、同一分支。
//
// 三、为什么必须用会话账号：角色表就是用会话账号建的
//
//	buildUpperGetUserInfoRosterBody → accountIDForSession(session)
//	（repository_access.go:52）⇒ 玩家能选到的角色**一定**属于
//	accountIDForSession(session)。归属校验若拿另一个账号源去比，只会在
//	"会话账号 != 兜底账号"的服务器上把整条链路打死（香港就是这种部署）。
//	本仓库自己的文档也写着同一口径（changlog.md:4503，2026-07-28 epic production 第一期）：
//	"All ownership checks use the authenticated session account rather than the
//	local-profile fallback account, so two-account sessions cannot cross-write a character."
//	→ 2026-07-29 的合并（changlog.md:4225）把这条口径退回成了兜底账号，本文件把它修回来。
//
// 四、开关
//
//	DNFBRIDGE_EPIC_PRODUCTION_ACCOUNT_SCOPE=session（默认）/ shared
//	  shared = 一键回退到改动前行为（用 s.accountID() 共享兜底账号比对）。
//	DNFBRIDGE_EPIC_PRODUCTION_INFO_PUSH=always（默认）/ selected_only
//	  always = 厂商 9.16 形状：**进屋即发 op1355**（哪怕还没选武器，target=0）。
//	  证据（D:\DNF90-test 厂商 9.16 原版二进制 23DF8D1C…DF94，packet_log.txt 5 条样本）：
//	    :2701 SEND msg_id=1355 len=48 hex=004b0530000000fc33a5e62d00000000
//	         0100000007000000000000000000000000000000000000000000000000000000
//	    :3084 …2f6b86842d00000000 0200000007000000…   （character_id=2）
//	  去掉 16 字节 class0 头后的 body = u32 角色号 + 28 字节状态（[0:4]=本周剩余、
//	  [4:8]=目标武器、[8:12]=充能点、[16:20]/[20:24]=两组材料 carry）——
//	  与 buildCurrentEpicProductionInfoBody 逐字节相同，且厂商事件自述
//	  `target_item_id=0 … weekly_remaining_count=7` ⇒ 厂商在 target=0 时**照发**。
//	  selected_only = 回退到改动前（target<=0 不发）。
//
// 五、不确定点（如实记录）
//   - op1417 成功/失败的**响应**形状没有厂商夹具：厂商那份 packet_log 里
//     一条 op1417 都没有（操作者在参考环境里从没点过武器），`msg_id=1417` 计数 0。
//     本文件不改响应形状（成功 = u8 1 + u32 目标 + u32 序号；失败 = u8 0 + 失败码），
//     沿用仓库既有形状，只把**账号口径**改对。
const (
	epicProductionAccountScopeEnv = "DNFBRIDGE_EPIC_PRODUCTION_ACCOUNT_SCOPE"
	epicProductionInfoPushEnv     = "DNFBRIDGE_EPIC_PRODUCTION_INFO_PUSH"
)

const (
	epicProductionAccountScopeValueSession = "session"
	epicProductionAccountScopeValueShared  = "shared"

	epicProductionInfoPushValueAlways       = "always"
	epicProductionInfoPushValueSelectedOnly = "selected_only"
)

// epicProductionAccountScopeMode 是 epicProductionAccountScopeEnv 的归一化取值。
type epicProductionAccountScopeMode int

const (
	// epicProductionAccountScopeSession 是默认档（零值）= 修正后行为：
	// 用会话已识别的账号（登录令牌 / 客户端进程登记）判定角色归属，识别不出才
	// 退回共享兜底账号 —— 与角色表的账号口径完全一致。
	epicProductionAccountScopeSession epicProductionAccountScopeMode = iota
	// epicProductionAccountScopeShared 是回退档 = 改动前行为：
	// 一律用 s.accountID()（options.accountID / 本地单账号 profile 兜底账号）比对。
	// 在"多账号 + 共享兜底账号"的部署上，这一档会让每一次史诗制作请求都返回失败码 3。
	epicProductionAccountScopeShared
)

func (mode epicProductionAccountScopeMode) String() string {
	if mode == epicProductionAccountScopeShared {
		return epicProductionAccountScopeValueShared
	}
	return epicProductionAccountScopeValueSession
}

// readEpicProductionAccountScopeMode 解析 DNFBRIDGE_EPIC_PRODUCTION_ACCOUNT_SCOPE。
// **默认 session（= 修正后行为）**：空/未设置/非法值都是 session；只有显式写
// `shared` 才回到改动前的账号口径（一键回退）。
func readEpicProductionAccountScopeMode(value string) epicProductionAccountScopeMode {
	if strings.EqualFold(strings.TrimSpace(value), epicProductionAccountScopeValueShared) {
		return epicProductionAccountScopeShared
	}
	return epicProductionAccountScopeSession
}

// epicProductionInfoPushMode 是 epicProductionInfoPushEnv 的归一化取值。
type epicProductionInfoPushMode int

const (
	// epicProductionInfoPushAlways 是默认档（零值）= 厂商 9.16 行为：
	// 进屋（actor bound、scene commit 之前）就发 op1355，target=0 也发。
	epicProductionInfoPushAlways epicProductionInfoPushMode = iota
	// epicProductionInfoPushSelectedOnly 是回退档 = 改动前行为：没选武器（target<=0）不发。
	epicProductionInfoPushSelectedOnly
)

func (mode epicProductionInfoPushMode) String() string {
	if mode == epicProductionInfoPushSelectedOnly {
		return epicProductionInfoPushValueSelectedOnly
	}
	return epicProductionInfoPushValueAlways
}

// readEpicProductionInfoPushMode 解析 DNFBRIDGE_EPIC_PRODUCTION_INFO_PUSH。
// **默认 always（= 厂商形状）**；只有显式写 `selected_only` 才回到旧行为。
func readEpicProductionInfoPushMode(value string) epicProductionInfoPushMode {
	if strings.EqualFold(strings.TrimSpace(value), epicProductionInfoPushValueSelectedOnly) {
		return epicProductionInfoPushSelectedOnly
	}
	return epicProductionInfoPushAlways
}

// epicProductionAccountScope 返回当前 Service 的账号口径档位
// （零值 options 的测试/探针路径 = session = 修正后行为）。
func (s *Service) epicProductionAccountScope() epicProductionAccountScopeMode {
	if s == nil {
		return epicProductionAccountScopeSession
	}
	return s.options.epicProductionAccountScope
}

// epicProductionInfoPush 返回当前 Service 的 op1355 下发档位
// （零值 options 的测试/探针路径 = always = 厂商形状）。
func (s *Service) epicProductionInfoPush() epicProductionInfoPushMode {
	if s == nil {
		return epicProductionInfoPushAlways
	}
	return s.options.epicProductionInfoPush
}

// epicProductionAccountIDForSession 返回史诗制作链路（op1417/op1418/op1419/op1420/op1421
// 与 op1355）应当使用的账号：
//
//	session 档（默认）：accountIDForSession(session) —— 会话已识别的账号优先，
//	  识别不出才退回共享兜底账号；**与角色表构建用的是同一个函数**，因此
//	  "能选到的角色"必然属于它，归属校验不会自相矛盾。
//	shared 档（回退）：s.accountID() —— 改动前的兜底账号口径。
//
// accountIDForSession 在会话确实拿不到令牌时会写一条 WARN
// （warnSharedAccountFallback，每会话一次），这正是判读"落到共享账号"的入口，保留。
func (s *Service) epicProductionAccountIDForSession(session *gameSession) string {
	if s == nil {
		return ""
	}
	if s.epicProductionAccountScope() == epicProductionAccountScopeShared {
		return strings.TrimSpace(s.accountID())
	}
	return strings.TrimSpace(s.accountIDForSession(session))
}

// epicProductionSessionOwnsCharacter 判定该会话是否有权对这条角色行做史诗制作。
//
// 返回 (true, "") 表示放行；否则第二个返回值是**可判读的失败原因**
// （写进 `game-upper-epic-production-*-blocked` 事件的 `reason`）。
// 改动前这一支只写 `reason=dnf epic production request is invalid`，与
// "PVF 不支持该武器"、"角色不存在"等完全同形，现场无法当场判死；现在分开：
//
//	character_load_failed   —— 仓储读取报错（含 error 字段）
//	character_not_found     —— 仓储里没有这条角色行
//	character_owner_mismatch—— 角色行账号 != 本会话账号（含两侧账号与档位）
//
// 安全边界不变：仍然是**拒绝**而不是放行（fail-closed），且仍然回同形的失败码 3。
func (s *Service) epicProductionSessionOwnsCharacter(character dnfrepo.CharacterRecord, sessionAccountID string) (bool, string) {
	sessionAccountID = strings.TrimSpace(sessionAccountID)
	if sessionAccountID == "" {
		return false, "session_account_unresolved"
	}
	if strings.TrimSpace(character.AccountID) != sessionAccountID {
		return false, "character_owner_mismatch"
	}
	return true, ""
}
