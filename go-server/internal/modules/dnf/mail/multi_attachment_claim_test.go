package mail

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestMailboxMultipleAttachmentNativeIdentitiesAndPartialClaim(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 10}})
	original := invMail("123", func(m *dnfrepo.MailRecord) {
		m.CreatedAt = time.Now()
		m.Gold = 50
		m.Attachments = []dnfrepo.MailAttachment{{ItemID: 1001, Count: 2}, {ItemID: 1002, Count: 3}}
	})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"123": original}}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler()
	open, err := h.Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 77, Repositories: repos})
	if err != nil {
		t.Fatal(err)
	}
	body := open.UpperResponses[1].Body
	exportCurrentMailboxNativeFixture(t, "multi_attachment_open", body, []bool{false, false})
	walk := invWalkMailboxList(t, body, []bool{false, false})
	ids := make([]uint32, 2)
	for i, span := range walk.summaries {
		ids[i] = binary.LittleEndian.Uint32(body[span.start:])
		parent := binary.LittleEndian.Uint32(body[span.end-5:])
		if parent != 123 {
			t.Fatalf("附件应在同一封信中，parent=%d", parent)
		}
	}
	if ids[0] == ids[1] || ids[0] == 0 || ids[1] == 0 {
		t.Fatalf("原生去重会丢弃第二附件：ids=%v", ids)
	}
	req := alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 77, Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99), Body: currentMailIDList(ids[1])}
	claimed, err := h.Handle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	ack := claimed.UpperResponses[0].Body
	exportMailboxClaimFixture(t, "partial", ack)
	if len(ack) != 17 || ack[0] != 1 || binary.LittleEndian.Uint32(ack[5:]) != 123 || binary.LittleEndian.Uint32(ack[9:]) != ids[1] {
		t.Fatalf("父信/附件领取对象不匹配：%x", ack)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	m := box.Mails["123"]
	ch, _, _ := repos.Character.Load(ctx, "77")
	if m.Claimed || m.Deleted || len(m.Attachments) != 1 || m.Attachments[0].ItemID != 1001 || m.Gold != 50 || ch.Stats["gold"] != 10 {
		t.Fatalf("单领第二附件误领其它资产：mail=%+v gold=%d", m, ch.Stats["gold"])
	}
	inv, _, _ := repos.Inventory.Load(ctx, "77")
	counts := map[int64]int64{}
	for _, s := range inv.Slots {
		counts[s.ItemID] += s.Count
	}
	if counts[1001] != 0 || counts[1002] != 3 {
		t.Fatal(counts)
	}
	req.Body = currentMailIDList(ids[0])
	_, err = h.Handle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	box, _, _ = repos.Mailbox.Load(ctx, "77")
	ch, _, _ = repos.Character.Load(ctx, "77")
	if !box.Mails["123"].Claimed || !box.Mails["123"].Deleted || ch.Stats["gold"] != 60 {
		t.Fatal("剩余资产领取失败", box, ch)
	}
}

func TestMailboxFullInventoryUsesNativePopupCode(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77"})
	slots := map[string]dnfrepo.ItemStack{}
	for slot := int16(3); slot <= 8; slot++ {
		slots[mailboxSlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 9999, Count: 1}
	}
	slots["0:65"] = dnfrepo.ItemStack{ItemID: 9999, Count: 1}
	if e := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); e != nil {
		t.Fatal(e)
	}
	if e := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"1": {MailID: "1", Gold: 50, Attachments: []dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}}}}}); e != nil {
		t.Fatal(e)
	}
	got, e := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 77, Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(65, 65, 1), Body: currentMailIDList(1)})
	if e != nil {
		t.Fatal(e)
	}
	if b := got.UpperResponses[0].Body; len(b) != 2 || b[0] != 0 || b[1] != 4 {
		t.Fatalf("现用原生错误4应显示物品栏空间不足：%x", b)
	}
	exportMailboxClaimFixture(t, "full-inventory", got.UpperResponses[0].Body)
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	if box.Mails["1"].Claimed || box.Mails["1"].Gold != 50 || len(box.Mails["1"].Attachments) != 1 {
		t.Fatal("拒绝后邮件资产改变", box)
	}
}
