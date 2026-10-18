package mail

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMailAttachmentQualityProjection(t *testing.T) {
	for _, seed := range []uint32{85180097, itemquality.TopSeed} {
		for _, evidence := range []string{"metadata", "raw", "both", "mailbox_kind"} {
			t.Run(fmt.Sprintf("%d_%s", seed, evidence), func(t *testing.T) {
				a := dnfrepo.MailAttachment{ItemID: 9001, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}
				if evidence != "raw" && evidence != "mailbox_kind" {
					a.Extra["quality_seed"] = fmt.Sprint(seed)
				}
				if evidence != "metadata" {
					a.RawEntry = make([]byte, mailboxItemRawSize)
					binary.LittleEndian.PutUint32(a.RawEntry[2:6], 9001)
					binary.LittleEndian.PutUint32(a.RawEntry[6:10], seed)
					if evidence == "both" {
						binary.LittleEndian.PutUint32(a.RawEntry[6:10], 1)
					}
					a.RawEntry[10], a.RawEntry[13] = 12, 1
					binary.LittleEndian.PutUint16(a.RawEntry[11:13], 28)
				}
				if evidence == "mailbox_kind" {
					a.Extra = map[string]string{"mailbox_pvf_kind": "equipment"}
				}
				before := append([]byte(nil), a.RawEntry...)
				raw := mailboxAttachmentRaw(a)
				if got := binary.LittleEndian.Uint32(raw[6:10]); got != seed {
					t.Errorf("邮件品级%d，期望%d", got, seed)
				}
				if len(a.RawEntry) > 0 && !bytes.Equal(raw[10:], a.RawEntry[10:]) {
					t.Fatal("邮件投影改变了其它装备属性")
				}
				assertMailAttachmentQualitySummary(t, a, seed)
				if !bytes.Equal(a.RawEntry, before) || a.Count != 1 {
					t.Fatal("邮件显示修改了持久化附件")
				}
			})
		}
	}
}

func TestMailAttachmentQualityKeepsNonEquipmentQuantity(t *testing.T) {
	for _, a := range []dnfrepo.MailAttachment{
		{ItemID: 3227, Count: 23, Extra: map[string]string{"item_kind": "stackable", "amount_or_count": "99"}},
		{ItemID: 40734, Count: 1, Extra: map[string]string{"item_kind": "avatar", "mailbox_equipment_type": "[coat avatar]", "quality_seed": "999999998"}},
		{ItemID: 5001, Count: 1, Extra: map[string]string{"mailbox_equipment_type": "[creature]", "quality_seed": "999999998"}},
		{ItemID: 9001, Count: 1, RawEntry: make([]byte, mailboxItemRawSize)},
	} {
		raw := mailboxAttachmentRaw(a)
		if got := binary.LittleEndian.Uint32(raw[6:10]); got != uint32(a.Count) {
			t.Fatalf("非普通装备数量变成%d：%+v", got, a)
		}
	}
}

func assertMailAttachmentQualitySummary(t *testing.T, a dnfrepo.MailAttachment, seed uint32) {
	t.Helper()
	now := time.Now().UTC()
	m := invMail("1", func(m *dnfrepo.MailRecord) { m.Attachments = []dnfrepo.MailAttachment{a} })
	body, err := buildMailboxListNotification([]dnfrepo.MailRecord{m}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	invWalkMailboxList(t, body, []bool{false})
	c := invCursor{data: body, off: 2}
	c.need(t, 8, "邮件与发送者编号")
	c.dstring(t, "发送者")
	c.need(t, 9, "金币、物品编号与标志")
	if got := c.u32(t, "摘要品级"); got != seed {
		t.Errorf("摘要品级%d，期望%d", got, seed)
	}
	c.need(t, 11, "摘要其它装备字段")
	raw := c.need(t, mailboxItemRawSize, "附件实例行")
	if got := binary.LittleEndian.Uint32(raw[6:10]); got != seed {
		t.Errorf("附件行品级%d，期望%d", got, seed)
	}
}

func TestMailAttachmentQualitySendOpenClaimRoundTrip(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "local-account", Name: "sender", Stats: map[string]int64{"gold": 2000}})
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "88", AccountID: "local-account", Name: "receiver", Slot: 1})
	raw := make([]byte, mailboxItemRawSize)
	binary.LittleEndian.PutUint16(raw[:2], 9)
	binary.LittleEndian.PutUint32(raw[2:6], 9001)
	binary.LittleEndian.PutUint32(raw[6:10], itemquality.TopSeed)
	raw[10], raw[13] = 12, 1
	binary.LittleEndian.PutUint16(raw[11:13], 28)
	stack := dnfrepo.ItemStack{ItemID: 9001, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "quality_seed": "999999998", "durability": "28", "max_durability": "57", "enchant_card": "777"}}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{"0:9": stack}}); err != nil {
		t.Fatal(err)
	}
	resolver := func(uint32) (alignedcmd.MailboxItemResolution, error) {
		return alignedcmd.MailboxItemResolution{Kind: "equipment", Price: 100, SlotStart: 9, SlotEnd: 64, StackLimit: 1, DefaultExtra: map[string]string{"quality_seed": "1", "durability": "57"}}, nil
	}
	send, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMultiMailboxSend), SelectedCharacterID: 77, Repositories: repos, MailboxItemResolver: resolver, Body: currentSendBody("receiver", 0, []SendAttachment{{ListType: 0, SlotIndex: 9, ItemID: 9001, Count: 1}}, "装备品级往返", 0, false, true)})
	if err != nil || len(send.UpperResponses) == 0 || send.UpperResponses[0].Body[0] != 1 {
		t.Fatalf("发送失败：%+v %v", send, err)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "88")
	a := box.Mails["1"].Attachments[0]
	assertMailAttachmentQualitySummary(t, a, itemquality.TopSeed)
	opened, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 88, Repositories: repos})
	if err != nil || len(opened.UpperResponses) != 2 {
		t.Fatalf("打开邮箱失败：%+v %v", opened, err)
	}
	if !bytes.Contains(opened.UpperResponses[1].Body, mailboxAttachmentRaw(a)) {
		t.Fatal("邮箱没有发送已验证的装备行")
	}
	claim, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 88, Repositories: repos, MailboxItemResolver: resolver, Body: currentMailIDList(1)})
	if err != nil || len(claim.UpperResponses) == 0 || claim.UpperResponses[0].Body[0] != 1 {
		t.Fatalf("领取失败：%+v %v", claim, err)
	}
	inv, _, _ := repos.Inventory.Load(ctx, "88")
	found := 0
	for _, got := range inv.Slots {
		if got.ItemID != 9001 {
			continue
		}
		found++
		if got.Count != 1 || itemquality.FromStack(got) != itemquality.TopSeed || !bytes.Equal(got.RawEntry, raw) || got.Extra["enchant_card"] != stack.Extra["enchant_card"] || !reflect.DeepEqual(got.RawEntry[10:], raw[10:]) {
			t.Fatalf("领取改变装备实例：%+v", got)
		}
	}
	if found != 1 {
		t.Fatalf("领取得到%d件装备", found)
	}
}
