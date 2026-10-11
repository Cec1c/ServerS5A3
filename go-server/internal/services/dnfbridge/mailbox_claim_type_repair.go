package dnfbridge

import (
	"strings"
)

// 本文件补上"领取邮件时客户端闪退"的**通用**成因中的一条：邮件附件的类型 token
// 修补只在 op96（打开邮箱）跑，导致**玩家邮箱开着的时候新到的那封信**在 op97
// (class0/0x61) 快照里少了 `[creature]` 分支的 5 个字节。
//
// ── 现场与证据 ────────────────────────────────────────────────────────
//
// 运维原话（2026-09-29）：「收取邮件**有概率**游戏闪退」——不限小晶块。
//
// 服务端下行形状（全仓唯一构造点 mail/body.go）：
//
//	writeSummary(): [mailID u32][senderID u32][name dstring][gold u32]
//	                [itemID u32][1][count u32][0x0B u16][0x0D u8][0x0E u32]
//	                [0x12 u8][0x13 u8][0x14 u16]
//	                (若是 [creature] 分支: [u32 0][u8 0])   ← 只有这个分支多 5 字节
//	                [0x77 行 = 119 字节]
//	                [剩余秒 u32][mailID u32][mailType u8]
//
// 分支判据（mail/body.go `mailboxAttachmentRequiresClass26`）读的是**盘里 Extra 的
// 字符串** `mailbox_equipment_type`；而客户端按**道具自身类型**（= 运行时 PVF）决定
// 它读不读那 5 个字节。两者一旦不一致，客户端就会把下一行的头几个字节当成本行的
// 余下字段 ⇒ 整页邮件行错位 ⇒ 越界读/闪退。这不是"某类道具必然崩"，而是
// "快照里恰好有一件没带 token 的 creature 附件时才崩" ⇒ 运维看到的"概率"。
//
// 谁会把 token 漏掉：Extra 由两个来源写——玩家发送（op94/op315 的
// `grantAttachmentExtra`）与**投放端修补**（`reconcileCurrentMailAttachmentExpirations`，
// 由 `currentMailAttachmentTypeTokens` 按运行时 PVF 算出）。后者只在
// `game_aligned.go` 的 op96 分支里跑（`reconcileCurrentJoustSettlementMailBeforeOpen`），
// 所以：**GM 面板在玩家邮箱已经开着时投递的信、以及任何"最近一次 op96 之后才到"的信，
// 其附件没有任何 token**，op97 行宽随之短 5 字节。
// 而 op95 领取之后服务端立刻重发一页 op97（`handleMailboxExtract` 里
// `NewOwner(...).Open` + `buildMailboxListNotification`），那一页里所有**剩余**的
// 未领取信都会按错行宽渲染 ⇒ 玩家看到的就是"领取后闪退"。
//
// 历史同一个坑：`mailboxAttachmentRequiresClass26` 的注释记着上一轮"只做 EqualFold
// 就漏掉了 [creature]，于是 [creature] 附件（宠物更名卡等）少写 5 个字节，客户端按
// 错误长度解析整封邮件列表"。上一轮只修了**判据的写法**，没有修**证据的来源**
// （仍然是可缺失的 Extra）——本文件补上后者。
//
// ── 开关 ──────────────────────────────────────────────────────────────
//
// `DNFBRIDGE_MAILBOX_CLAIM_TYPE_REPAIR`（默认 = on，即修复后的行为）：
//
//	未设置/on/enabled          → op95 领取前也跑一次附件类型/到期/耐久修补
//	legacy/off/0/false/disabled → 只在 op96 跑（逐字节等于改动前）
//
// 修补本身是幂等的、只写运行时 PVF 已经声明过的值（见
// reconcileCurrentMailAttachmentExpirations 的文件注释），并且失败只记日志、
// 不阻断领取。
const (
	mailboxClaimTypeRepairEnv      = "DNFBRIDGE_MAILBOX_CLAIM_TYPE_REPAIR"
	mailboxClaimTypeRepairValueOn  = "on"
	mailboxClaimTypeRepairValueOff = "legacy"
)

// mailboxClaimTypeRepairEnabled 读 DNFBRIDGE_MAILBOX_CLAIM_TYPE_REPAIR 并归一化：
// 空/未设置/非法值一律按**修复档**（宁可多跑一次幂等修补，也不要让客户端拿到
// 行宽不一致的邮件页）；只有显式写出回退字面量才回到"只在 op96 修补"。
func (s *Service) mailboxClaimTypeRepairEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(envString(mailboxClaimTypeRepairEnv, "")))
	switch value {
	case mailboxClaimTypeRepairValueOff, "off", "0", "false", "disabled":
		return false
	default:
		return true
	}
}
