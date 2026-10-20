package mail

import (
	"os"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 已领取信件的收尾档位：`DNFBRIDGE_MAILBOX_CLAIMED_LETTER=finish|keep`。
//
// 现场（2026-09-28，角色 6 `test78`，账号 `dnf:10011`，`/opt/dnf90/logs/packet_log.txt`）：
// `dnf_mails` 里 `claimed_flag=1 AND deleted_flag=0` 的空信会**永久**留在邮箱里 ——
//
//   - `Owner.Claim` 把 `Claimed` 当成"这封信已经完成"直接跳过（owner.go 的候选循环），
//     一封都没选中时回 `ErrMailboxAlreadyClaimed`（客户端 0x04）；
//   - 客户端单封领取（op95 count=1）**不会**像"全部接受"那样再补一条 op134 删除，
//     所以这封信既领不动、也不会有任何变化，只能永远画在邮箱列表里。
//
// finish（默认档）把这条路走通：
//
//  1. 领取一封"已经领取过"的信时**不重复发货**（附件在旧实现里就已经转出，只剩下
//     投影没清 —— 与 clearUnclaimableMailboxAssets 同一口径）；
//  2. **领完即结清**：一封领干净了的信（金币与附件都进账、或本来就是空收据）在本次
//     领取里直接从收件箱移除。现场 packet_log（角色 6，op95 全是单封 count=1）显示
//     客户端的「全部接受」会自己补一条 op134 删除，但"点开一封信再点收取"不补 ——
//     不结清就会留下玩家报的那种空邮件；厂商的领取结果里本来就有 RemovedFromInbox
//     这一支（MailboxHandler.cs:365-370：领取成功后告诉客户端这封信没了 + 刷列表），
//     我们的客户端不自己删，所以由服务端替它完成；
//  3. 收藏（`mailbox_saved`）的信绝不自动移除；
//  4. 删除一封已经结清的信变成幂等无操作（与 GM 删除路径 gm.go:566-568 同口径），
//     这样"服务端结清 + 客户端照自己的选择列表补发 op134"不会整批失败；
//  5. 删除信件时把聚合里的金币/附件一起清掉：`MailboxFieldMails` 是整表重写
//     （mysql replaceMails），聚合清了，`dnf_mail_attachments` /
//     `dnf_mail_attachment_extra` 的行才会跟着删掉，不会再出现"已删除的信还留着
//     附件行"这种形状（现场角色 3 mail 2/5 就是这个形状）；
//  6. 打开邮箱时的存量大扫除（Owner.Open）连**已删除却还挂着附件行**的存量形状
//     一起清掉，让"已领取/已删除的信不带可领取投影"这条不变量在下一次打开邮箱后
//     对存量数据也成立。
//
// keep 是回退档：以上六条全部关掉，逐字节回到改动前的行为（跳过已领取的信、
// 0x04、领完的信留作空收据、删除时保留聚合里的附件/金币、删除已删除的信回 0x03、
// 打开邮箱也不动已删除信的附件行）。
//
// 为什么这个开关放在**邮件模块**而不是 `internal/services/dnfbridge/options.go`：
// 它改的是邮箱聚合（`dnf_mails` / `dnf_mail_attachments`）的领域规则和它自己的
// 持久化写法，桥接层只负责把 op95/op134 转发进来，对"什么算结清"没有话语权。
// 本仓已有同样的领域级开关先例：`internal/modules/dnf/equip/owner.go` 的
// `DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP`。启动日志 `dnfbridge started` 仍会打印
// 生效档位（mailbox_claimed_letter），操作者不必猜。
const MailboxClaimedLetterEnv = "DNFBRIDGE_MAILBOX_CLAIMED_LETTER"

// ClaimedLetterMode 是已领取信件的收尾档位。
type ClaimedLetterMode string

const (
	// ClaimedLetterFinish 是默认档：领完即结清（已领取的空收据也从收件箱移除）。
	ClaimedLetterFinish ClaimedLetterMode = "finish"
	// ClaimedLetterKeep 是回退档：领完的信留作空收据（改动前的行为）。
	ClaimedLetterKeep ClaimedLetterMode = "keep"
)

// String 让启动日志与错误信息直接打印档位名。
func (m ClaimedLetterMode) String() string {
	if m == ClaimedLetterKeep {
		return string(ClaimedLetterKeep)
	}
	return string(ClaimedLetterFinish)
}

// normalizeClaimedLetterMode 认 keep/legacy/off/0/false/no 为回退档，其余（含空值）
// 都是默认档 finish：配置写错时**按修好的行为走**，而不是悄悄退回旧行为。
func normalizeClaimedLetterMode(raw string) ClaimedLetterMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "keep", "legacy", "off", "0", "false", "no":
		return ClaimedLetterKeep
	default:
		return ClaimedLetterFinish
	}
}

// ClaimedLetterModeFromEnv 读当前进程的档位（NewOwner 用它做默认值）。
func ClaimedLetterModeFromEnv() ClaimedLetterMode {
	return normalizeClaimedLetterMode(os.Getenv(MailboxClaimedLetterEnv))
}

// WithClaimedLetterMode 显式覆盖档位（测试与桥接层显式档位用）。
// 传零值等于传 finish。
func (o Owner) WithClaimedLetterMode(mode ClaimedLetterMode) Owner {
	o.claimedLetter = normalizeClaimedLetterMode(string(mode))
	return o
}

// finishesClaimedLetters 报告是否启用"结清已领取信件"这条领域规则。
// Owner 零值（claimedLetter 为空串）按默认档处理。
func (o Owner) finishesClaimedLetters() bool {
	return o.claimedLetter != ClaimedLetterKeep
}

// claimedLetterSettled 判断一封已经领取过的信是否已经兑现干净：没有金币、没有附件。
// 这种信就是一张空收据，结清它不会丢任何资产。
func claimedLetterSettled(mail dnfrepo.MailRecord) bool {
	return mail.Claimed && mail.Gold == 0 && len(mail.Attachments) == 0
}
