package mail

import (
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 当前客户端对 [creature] 附件走 type-26 分支：在固定 0x77 行之前多读一个 dword
// 和一个 flag。判据必须容得下 PVF 的原始写法（[creature]、大小写、反引号），
// 否则附件少写 5 个字节，客户端按错误长度解析整封邮件列表 —— 线上实测表现为
// "点开邮箱客户端卡死"（GM 面板发的邮件没有任何 Extra，最容易踩到）。
func TestMailboxAttachmentRequiresClass26AcceptsPVFTokens(t *testing.T) {
	for token, want := range map[string]bool{
		"creature":     true,
		"[creature]":   true,
		"[Creature]":   true,
		" [creature] ": true,
		"`[creature]`": true,
		"[feed]":       false,
		"":             false,
		"[creature]x":  false,
	} {
		attachment := dnfrepo.MailAttachment{ItemID: 25, Count: 1, Extra: map[string]string{"mailbox_equipment_type": token}}
		if got := mailboxAttachmentRequiresClass26(attachment); got != want {
			t.Fatalf("token %q -> %t, want %t", token, got, want)
		}
	}
	if mailboxAttachmentRequiresClass26(dnfrepo.MailAttachment{ItemID: 25, Count: 1}) {
		t.Fatal("an attachment without any Extra must not take the type-26 branch")
	}
}

// 同一条邮件列表里，[creature] 附件必须比普通附件多 5 个字节（dword + flag）。
func TestMailboxListBodyGivesCreatureAttachmentItsType26Prefix(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	base := dnfrepo.MailRecord{
		MailID:      "4",
		SenderName:  "GM",
		Title:       "t",
		CreatedAt:   now.Add(-time.Hour),
		Attachments: nil,
		Metadata:    map[string]string{},
	}
	creature := base
	creature.Attachments = []dnfrepo.MailAttachment{{
		ItemID: 25, Count: 1,
		Extra: map[string]string{"mailbox_equipment_type": "[creature]"},
	}}
	plain := base
	plain.Attachments = []dnfrepo.MailAttachment{{
		ItemID: 490701730, Count: 5,
		Extra: map[string]string{"mailbox_equipment_type": "[random reward item]"},
	}}

	creatureBody, err := buildMailboxListNotification([]dnfrepo.MailRecord{creature}, 0, now)
	if err != nil {
		t.Fatalf("creature list body: %v", err)
	}
	plainBody, err := buildMailboxListNotification([]dnfrepo.MailRecord{plain}, 0, now)
	if err != nil {
		t.Fatalf("plain list body: %v", err)
	}
	if got := len(creatureBody) - len(plainBody); got != 5 {
		t.Fatalf("type-26 prefix size = %d bytes, want 5 (creature=%d plain=%d)",
			got, len(creatureBody), len(plainBody))
	}
}
