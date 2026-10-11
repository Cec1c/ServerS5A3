package mail

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 领取端：装备附件的耐久证据来自 dnfbridge 的运行时 PVF 解析
// （MailboxItemResolution.DefaultExtra 的 durability + max_durability）。
// 少了它，落地堆栈就是"0 耐久且修不了"—— 客户端读 Extra，修理 owner 也读 Extra。
func TestMailboxClaimedEquipmentStackKeepsPVFDurabilityEvidence(t *testing.T) {
	inventory := &dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{}}
	resolution := alignedcmd.MailboxItemResolution{
		Kind:       "equipment",
		SlotStart:  9,
		SlotEnd:    64,
		StackLimit: 1,
		DefaultExtra: map[string]string{
			"durability":     "57",
			"max_durability": "57",
			"quality_seed":   "123456789",
			"item_kind":      "equipment",
			"pvf_path":       "equipment/weapon/test_sword.equ",
		},
	}
	if _, err := addAttachmentToInventory(inventory, dnfrepo.MailAttachment{ItemID: 9001, Count: 1}, resolution); err != nil {
		t.Fatalf("claim attachment: %v", err)
	}
	stack, ok := inventory.Slots["0:9"]
	if !ok {
		t.Fatalf("claimed stack missing: %+v", inventory.Slots)
	}
	if stack.Extra["durability"] != "57" || stack.Extra["max_durability"] != "57" {
		t.Fatalf("claimed equipment durability evidence = %+v, want both keys 57", stack.Extra)
	}
	// 实例身份也要落地：没有它 op19 穿上的 raw 行重建会失败（重登脱落）。
	if stack.Extra["quality_seed"] != "123456789" {
		t.Fatalf("claimed equipment identity evidence = %+v, want the projected quality seed", stack.Extra)
	}
	// 邮件信封的类型记账键是传输证据，不许留进背包（否则会阻止与同款道具堆叠）。
	if _, framed := stack.Extra["mailbox_pvf_kind"]; framed {
		t.Fatalf("mailbox framing keys leaked into the stack: %+v", stack.Extra)
	}
}

// 附件自己带了实例耐久（玩家邮寄的旧装备）时以附件为准：不能被 PVF 的满耐久覆盖。
func TestMailboxClaimKeepsAttachmentInstanceDurability(t *testing.T) {
	inventory := &dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{}}
	resolution := alignedcmd.MailboxItemResolution{
		Kind:         "equipment",
		SlotStart:    9,
		SlotEnd:      64,
		StackLimit:   1,
		DefaultExtra: map[string]string{"durability": "57", "max_durability": "57"},
	}
	attachment := dnfrepo.MailAttachment{
		ItemID: 9001,
		Count:  1,
		Extra:  map[string]string{"durability": "12", "max_durability": "57"},
	}
	if _, err := addAttachmentToInventory(inventory, attachment, resolution); err != nil {
		t.Fatalf("claim attachment: %v", err)
	}
	stack := inventory.Slots["0:9"]
	if stack.Extra["durability"] != "12" {
		t.Fatalf("instance durability overwritten by the PVF projection: %+v", stack.Extra)
	}
}

// 邮箱列表里的附件行（固定 0x77 行）同样要显示这份耐久：0x0B..0x0D 就是客户端读的
// 耐久字段。GM 面板发的装备附件既没有 0x77 行、也没有 Extra，过去客户端在邮箱里
// 就已经看到 0 耐久。
func TestMailboxAttachmentRowCarriesDurabilityEvidence(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	mail := dnfrepo.MailRecord{
		MailID:     "4",
		SenderName: "GM",
		Title:      "GM物品发放",
		CreatedAt:  now.Add(-time.Hour),
		Metadata:   map[string]string{"source": "dnf90-gm-tool"},
		Attachments: []dnfrepo.MailAttachment{{
			ItemID: 9001,
			Count:  1,
			Extra:  map[string]string{"durability": "57", "max_durability": "57"},
		}},
	}

	raw := mailboxAttachmentRaw(mail.Attachments[0])
	if got := binary.LittleEndian.Uint16(raw[0x0b:0x0d]); got != 57 {
		t.Fatalf("attachment row durability = %d, want 57", got)
	}

	body, err := buildMailboxListNotification([]dnfrepo.MailRecord{mail}, 0, now)
	if err != nil {
		t.Fatalf("mailbox list body: %v", err)
	}
	// 下发行里 itemID(count) 之后紧跟的就是那个 u16 耐久字段。
	needle := []byte{
		0x29, 0x23, 0x00, 0x00, // itemID 9001
		0x01,                   // attached flag
		0x01, 0x00, 0x00, 0x00, // count 1
		0x39, 0x00, // durability 57
	}
	if !bytes.Contains(body, needle) {
		t.Fatalf("mailbox list body does not carry the durability evidence: % X", body)
	}

	// 没有证据时不伪造：保持 0（客户端会照旧显示 0，但绝不会因此变成"满耐久"）。
	plain := mailboxAttachmentRaw(dnfrepo.MailAttachment{ItemID: 9001, Count: 1})
	if got := binary.LittleEndian.Uint16(plain[0x0b:0x0d]); got != 0 {
		t.Fatalf("row durability invented without evidence: %d", got)
	}
}
