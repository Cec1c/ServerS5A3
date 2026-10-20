package mail

import (
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 邮件附件行（固定 0x77 行）的耐久字段。
//
// 当前客户端的附件行把耐久放在 0x0B..0x0D —— 与场景物品行同一布局
// （current_item_list_stack_projection 把 Extra 的 durability/max_durability 写到
// 0x0B，mail.body 的 writeSummary 也是把 raw[0x0b:0x0d] 当耐久字段单独下发的）。
// mailboxAttachmentRaw 过去只写 itemID/count/bind/到期时间：GM 面板发的装备附件
// 既没有随行的 0x77 行、也没有实例耐久，于是玩家在邮箱里就已经看到"0 耐久"。
//
// 这里按 dnfbridge 补进附件 Extra 的运行时 PVF 耐久证据补齐该字段；附件自带
// 0x77 行时 Extra 优先（与场景物品行的读取顺序一致）。没有证据就保持原样，
// 绝不凭空造一个耐久值。
func mailboxAttachmentDurability(attachment dnfrepo.MailAttachment) uint16 {
	for _, key := range []string{"durability", "max_durability"} {
		raw := strings.TrimSpace(attachment.Extra[key])
		if raw == "" {
			continue
		}
		value, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			continue
		}
		return uint16(value)
	}
	return 0
}
