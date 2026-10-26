package mail

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMailboxOpenBuildsCurrentCountAck(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1"},
			"2": {MailID: "2", Deleted: true},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxOpen),
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if len(got.UpperResponses) != 2 {
		t.Fatalf("response count = %d, want open ACK plus class0/0x61 snapshot", len(got.UpperResponses))
	}
	assertMailboxResponse(t, got.UpperResponses[0], uint16(dnfenum.CmdPacketMailboxOpen), dnfproto.DefaultChannelClassification, []byte{0x01, 0x00, 0x00})
	wantSnapshot := []byte{0x01, 0x00}
	wantSnapshot = appendU32(wantSnapshot, 1) // summary claim object / mail ID
	wantSnapshot = appendU32(wantSnapshot, 0) // sender character
	wantSnapshot = appendU32(wantSnapshot, 0) // sender DSTR
	wantSnapshot = appendU32(wantSnapshot, 0) // gold
	wantSnapshot = appendU32(wantSnapshot, 0) // item ID
	wantSnapshot = append(wantSnapshot, 0)    // has item
	wantSnapshot = appendU32(wantSnapshot, 0)
	wantSnapshot = append(wantSnapshot, 0, 0, 0) // u16 + u8
	wantSnapshot = appendU32(wantSnapshot, 0)
	wantSnapshot = append(wantSnapshot, 0, 0, 0, 0) // two u8 + u16
	wantSnapshot = append(wantSnapshot, make([]byte, mailboxItemRawSize)...)
	wantSnapshot = appendU32(wantSnapshot, 0) // remaining seconds
	wantSnapshot = appendU32(wantSnapshot, 0) // text-only summary seed
	wantSnapshot = append(wantSnapshot, 0)    // mail type
	wantSnapshot = append(wantSnapshot, 0, 0) // deferred page count
	wantSnapshot = append(wantSnapshot, 1, 0) // one detail row
	wantSnapshot = appendU32(wantSnapshot, 1)
	wantSnapshot = appendU32(wantSnapshot, 0)
	wantSnapshot = appendU32(wantSnapshot, 0) // sender DSTR
	wantSnapshot = appendU32(wantSnapshot, 0) // body DSTR
	wantSnapshot = appendU32(wantSnapshot, 0) // creation Unix time
	wantSnapshot = append(wantSnapshot, 1, 0, 0)
	assertMailboxResponse(t, got.UpperResponses[1], mailboxListNotificationMessageID, 0, wantSnapshot)
	exportCurrentMailboxNativeFixture(t, "actual mailbox open handler", got.UpperResponses[1].Body, []bool{false})
}

func TestMailboxRecipientCharacterListUsesOtherRolesFromSameAccount(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	for _, character := range []dnfrepo.CharacterRecord{
		{CharacterID: "77", AccountID: "local-account", Name: "发送者", Slot: 0},
		{CharacterID: "88", AccountID: "local-account", Name: "来来来", Slot: 1},
		{CharacterID: "99", AccountID: "other-account", Name: "别的账号", Slot: 0},
	} {
		mustSaveCharacter(t, repos, character)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketRequestServerCharacterList),
		Body:                []byte{3},
		AccountID:           "local-account",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	nameRaw, err := encodeMailboxText("来来来", mailboxRecipientListNameMaxBytes)
	if err != nil {
		t.Fatalf("encode expected role name: %v", err)
	}
	want := []byte{3, 1}
	want = appendU32(want, 88)
	want = appendU32(want, uint32(len(nameRaw)))
	want = append(want, nameRaw...)
	want = append(want, 0)
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("mailbox account-role result = %+v, want one response", got)
	}
	assertMailboxResponse(t, got.UpperResponses[0], mailboxRecipientListMessageID, 0, want)
}

func TestMailboxRecipientCharacterListRejectsMalformedRequestWithoutReply(t *testing.T) {
	got, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketRequestServerCharacterList),
		Body:   nil,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || got.ResponseAllowed || len(got.UpperResponses) != 0 {
		t.Fatalf("malformed mailbox account-role result = %+v, want handled without a reply", got)
	}
}

func TestMailboxSendTransfersGoldAndStoresRecipientMail(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc-a",
		Name:        "sender",
		Stats:       map[string]int64{"gold": 200},
	})
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "88", AccountID: "acc-b", Name: "receiver", Slot: 1})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxSend),
		SelectedCharacterID: 77,
		Repositories:        repos,
		Body: currentSendBody(
			"receiver",
			50,
			[]SendAttachment{{}},
			"body",
			0,
			false,
			false,
		),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketMailboxSend, []byte{0x01})
	if got.MailboxAlarmRecipientID != 88 {
		t.Fatalf("online mailbox alarm recipient = %d, want 88", got.MailboxAlarmRecipientID)
	}

	sender, ok, err := repos.Character.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load sender ok=%v error=%v", ok, err)
	}
	if sender.Stats["gold"] != 48 {
		t.Fatalf("sender gold = %d, want 48 after 50 transfer + 102 postage", sender.Stats["gold"])
	}
	box, ok, err := repos.Mailbox.Load(ctx, "88")
	if err != nil || !ok {
		t.Fatalf("recipient mailbox ok=%v error=%v", ok, err)
	}
	if len(box.Mails) != 1 {
		t.Fatalf("mail count = %d, want 1: %+v", len(box.Mails), box.Mails)
	}
	for id, mail := range box.Mails {
		if id != "1" || mail.MailID != "1" {
			t.Fatalf("wire mail id = %q/%q, want uint32-compatible 1", id, mail.MailID)
		}
		if mail.SenderCharacterID != "77" || mail.RecipientCharacterID != "88" ||
			mail.Title != "" || mail.Body != "body" || mail.Gold != 50 {
			t.Fatalf("unexpected mail: %+v", mail)
		}
	}
}

func TestMailboxSendAllowsDurablePVFItemWithZeroPrice(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "1",
		AccountID:   "local-account",
		Name:        "sender",
		Stats:       map[string]int64{"gold": 2000},
	})
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   "local-account",
		Name:        "receiver",
		Slot:        2,
	})
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "1",
		Slots: map[string]dnfrepo.ItemStack{
			"0:125": {ItemID: 3166, Count: 3},
		},
	}); err != nil {
		t.Fatalf("save sender inventory: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxSend),
		SelectedCharacterID: 1,
		Repositories:        repos,
		MailboxItemResolver: func(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
			if itemID != 3166 {
				t.Fatalf("resolver item = %d, want 3166", itemID)
			}
			return alignedcmd.MailboxItemResolution{
				Price:      0,
				PVFPath:    "stackable/reproduced-item.stk",
				Kind:       "stackable",
				SlotStart:  121,
				SlotEnd:    176,
				StackLimit: 1000,
			}, nil
		},
		Body: currentSendBody("receiver", 0, []SendAttachment{{
			ListType:  0,
			SlotIndex: 125,
			ItemID:    3166,
			Count:     3,
		}}, " ", 0, false, false),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketMailboxSend, []byte{0x01})

	sender, found, err := repos.Character.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load sender found=%v err=%v", found, err)
	}
	if got, want := sender.Stats["gold"], int64(900); got != want {
		t.Fatalf("sender gold = %d, want %d after fixed one-attachment postage", got, want)
	}
	inventory, found, err := repos.Inventory.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load sender inventory found=%v err=%v", found, err)
	}
	if _, exists := inventory.Slots["0:125"]; exists {
		t.Fatalf("sent zero-value attachment remains in sender inventory: %+v", inventory.Slots["0:125"])
	}
	box, found, err := repos.Mailbox.Load(ctx, "3")
	if err != nil || !found {
		t.Fatalf("load recipient mailbox found=%v err=%v", found, err)
	}
	mail := box.Mails["1"]
	if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != 3166 || mail.Attachments[0].Count != 3 {
		t.Fatalf("recipient attachment = %+v, want item 3166 x3", mail.Attachments)
	}
}

func TestMailboxSendThenRecipientOpenAndClaimCompletesSameAccountTransfer(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "local-account",
		Name:        "sender",
		Stats:       map[string]int64{"gold": 2000},
	})
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "88",
		AccountID:   "local-account",
		Name:        "receiver",
		Slot:        1,
		Stats:       map[string]int64{"gold": 10},
	})
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:65": {ItemID: 1001, Count: 2, Bind: true},
		},
	}); err != nil {
		t.Fatalf("save sender inventory: %v", err)
	}

	send, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMultiMailboxSend),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: func(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
			if itemID != 1001 {
				t.Fatalf("sender resolver item = %d, want 1001", itemID)
			}
			return alignedcmd.MailboxItemResolution{Price: 100, SlotStart: 65, SlotEnd: 120, StackLimit: 99}, nil
		},
		Body: currentSendBody("receiver", 50, []SendAttachment{{ListType: 0, SlotIndex: 65, ItemID: 1001, Count: 2}}, "same-account transfer", 0, false, true),
	})
	if err != nil {
		t.Fatalf("send mail: %v", err)
	}
	assertOneMailboxResponse(t, send, dnfenum.CmdPacketMultiMailboxSend, []byte{0x01})

	opened, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxOpen),
		SelectedCharacterID: 88,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("open recipient mailbox: %v", err)
	}
	if len(opened.UpperResponses) != 2 {
		t.Fatalf("recipient open responses = %d, want ACK plus snapshot", len(opened.UpperResponses))
	}
	assertMailboxResponse(t, opened.UpperResponses[0], uint16(dnfenum.CmdPacketMailboxOpen), dnfproto.DefaultChannelClassification, []byte{0x01, 0x00, 0x00})

	claim, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 88,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99),
		Body:                currentMailIDList(1),
	})
	if err != nil {
		t.Fatalf("claim recipient mail: %v", err)
	}
	// 领完即结清（claimed_letter.go 默认档）：补发的 0x61 页里已经没有这封信。
	assertMailboxExtractSuccessWithEmptyPage(t, claim, currentExtractAck(1))

	receiver, found, err := repos.Character.Load(ctx, "88")
	if err != nil || !found {
		t.Fatalf("load receiver found=%v err=%v", found, err)
	}
	if got, want := receiver.Stats["gold"], int64(60); got != want {
		t.Fatalf("receiver gold = %d, want %d", got, want)
	}
	inventory, found, err := repos.Inventory.Load(ctx, "88")
	if err != nil || !found {
		t.Fatalf("load recipient inventory found=%v err=%v", found, err)
	}
	if got := inventory.Slots["0:3"]; got.ItemID != 1001 || got.Count != 2 || !got.Bind {
		t.Fatalf("recipient claimed item = %+v, want bound item 1001 x2", got)
	}
	mailbox, found, err := repos.Mailbox.Load(ctx, "88")
	if err != nil || !found {
		t.Fatalf("load recipient mailbox found=%v err=%v", found, err)
	}
	if mail := mailbox.Mails["1"]; !mail.Claimed || !mail.Deleted || !mail.Read || mail.Gold != 0 || len(mail.Attachments) != 0 {
		t.Fatalf("claimed mail did not finish (claimed+read+deleted, no durable assets): %+v", mail)
	}
}

func TestMailboxMultiAttachmentSendUsesDurableSlots(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc-a",
		Name:        "sender",
		Stats:       map[string]int64{"gold": 5000},
	})
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "88", AccountID: "acc-b", Name: "receiver"})
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"0:3": {ItemID: 1001, Count: 5, Bind: true},
			"1:4": {ItemID: 2002, Count: 1, RawEntry: []byte{1, 2, 3}},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	attachments := []SendAttachment{
		{ListType: 0, SlotIndex: 3, ItemID: 1001, Count: 2},
		{ListType: 1, SlotIndex: 4, ItemID: 2002, Count: 1},
	}
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMultiMailboxSend),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: func(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
			return alignedcmd.MailboxItemResolution{Price: map[uint32]int64{1001: 100, 2002: 200}[itemID]}, nil
		},
		Body: currentSendBody("receiver", 0, attachments, "two", 0, false, true),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketMultiMailboxSend, []byte{0x01})

	inventory, ok, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load sender inventory ok=%v error=%v", ok, err)
	}
	if stack := inventory.Slots["0:3"]; stack.Count != 3 || stack.ItemID != 1001 {
		t.Fatalf("remaining stack = %+v, want item 1001 x3", stack)
	}
	if _, exists := inventory.Slots["1:4"]; exists {
		t.Fatalf("fully attached slot should be removed: %+v", inventory.Slots["1:4"])
	}
	sender, _, _ := repos.Character.Load(ctx, "77")
	if sender.Stats["gold"] != 2880 {
		t.Fatalf("sender gold = %d, want 2880 after 2120 attachment postage", sender.Stats["gold"])
	}
	box, ok, err := repos.Mailbox.Load(ctx, "88")
	if err != nil || !ok {
		t.Fatalf("load recipient mailbox ok=%v error=%v", ok, err)
	}
	mail := box.Mails["1"]
	if len(mail.Attachments) != 2 ||
		mail.Attachments[0].ItemID != 1001 || mail.Attachments[0].Count != 2 ||
		mail.Attachments[1].ItemID != 2002 || !bytes.Equal(mail.Attachments[1].RawEntry, []byte{1, 2, 3}) {
		t.Fatalf("mail attachments = %+v", mail.Attachments)
	}
}

func TestMailboxSendRejectsForgedAttachmentWithoutMutation(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "77",
		Name:        "sender",
		Stats:       map[string]int64{"gold": 100},
	})
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "88", Name: "receiver"})
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{"0:3": {ItemID: 1001, Count: 2}},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxSend),
		SelectedCharacterID: 77,
		Repositories:        repos,
		Body: currentSendBody(
			"receiver",
			50,
			[]SendAttachment{{ListType: 0, SlotIndex: 3, ItemID: 9999, Count: 1}},
			"",
			0,
			false,
			false,
		),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketMailboxSend, []byte{0x00, 0x07})
	sender, _, _ := repos.Character.Load(ctx, "77")
	if sender.Stats["gold"] != 100 {
		t.Fatalf("rejected send changed gold to %d", sender.Stats["gold"])
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "77")
	if inventory.Slots["0:3"].Count != 2 {
		t.Fatalf("rejected send changed inventory: %+v", inventory.Slots["0:3"])
	}
	if _, found, loadErr := repos.Mailbox.Load(ctx, "88"); loadErr != nil || found {
		t.Fatalf("rejected send created mailbox found=%v err=%v", found, loadErr)
	}
}

// TestMailboxExtractClaimsGoldAndItemAtomicallyThenRejectsRepeat 钉住"重复 op95
// 不二次发货"。旧用例名是 ...ThenRejectsDuplicate，本次只把"信留在收件箱"改成
// claimed_letter.go 默认档的"领完即结清"（信已移出收件箱），0x04 与"资产只发一次"
// 两条钉子都保持原样。
func TestMailboxExtractClaimsGoldAndItemAtomicallyThenRejectsRepeat(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc-a",
		Name:        "receiver",
		Stats:       map[string]int64{"gold": 10},
	})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"123": {
				MailID: "123",
				Gold:   50,
				Attachments: []dnfrepo.MailAttachment{
					{ItemID: 1001, Count: 2, Bind: true},
				},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	request := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99),
		Body:                currentMailIDList(123),
	}
	got, err := NewHandler().Handle(ctx, request)
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	// 领完即结清（claimed_letter.go 默认档）：这封信已经不在补发的 0x61 页里。
	assertMailboxExtractSuccessWithEmptyPage(t, got, currentExtractAck(123))
	if len(got.PostActions) != 0 {
		t.Fatalf("post actions = %v, want no full container refresh", got.PostActions)
	}
	if len(got.ItemSlotRefreshes) != 1 || got.ItemSlotRefreshes[0] != (alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: 3}) {
		t.Fatalf("item slot refreshes = %+v, want claimed quick slot 0:3", got.ItemSlotRefreshes)
	}

	character, ok, err := repos.Character.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load character ok=%v error=%v", ok, err)
	}
	if character.Stats["gold"] != 60 {
		t.Fatalf("gold = %d, want 60", character.Stats["gold"])
	}
	inventory, ok, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load inventory ok=%v error=%v", ok, err)
	}
	stack := inventory.Slots["0:3"]
	if stack.ItemID != 1001 || stack.Count != 2 || !stack.Bind {
		t.Fatalf("slot 0:3 = %+v, want claimed attachment", stack)
	}
	box, ok, err := repos.Mailbox.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load mailbox ok=%v error=%v", ok, err)
	}
	if !box.Mails["123"].Claimed || !box.Mails["123"].Deleted || !box.Mails["123"].Read || box.Mails["123"].Gold != 0 || len(box.Mails["123"].Attachments) != 0 {
		t.Fatalf("mail should be claimed/read and finished: %+v", box.Mails["123"])
	}

	// 重复 op95：这封信已经被结清（默认档：领完就从收件箱移走），所以第二次领取
	// 一封都没选中，答复仍是改动前的 0x04「已领取」—— 双击不会二次发货，也不会
	// 变成"邮件不存在"。旧用例名
	// TestMailboxExtractClaimsGoldAndItemAtomicallyThenRejectsDuplicate 钉的正是这条
	// 0x04，本次只把"信留在收件箱"换成"信已结清"。
	duplicate, err := NewHandler().Handle(ctx, request)
	if err != nil {
		t.Fatalf("duplicate Handle error = %v", err)
	}
	assertOneMailboxResponse(t, duplicate, dnfenum.CmdPacketMailboxExtractItem, []byte{0x00, 21})
	character, _, _ = repos.Character.Load(ctx, "77")
	inventory, _, _ = repos.Inventory.Load(ctx, "77")
	if character.Stats["gold"] != 60 || inventory.Slots["0:3"].Count != 2 {
		t.Fatalf("duplicate claim mutated assets: gold=%d stack=%+v", character.Stats["gold"], inventory.Slots["0:0"])
	}
	box, _, _ = repos.Mailbox.Load(ctx, "77")
	if settled := box.Mails["123"]; !settled.Claimed || !settled.Deleted || settled.Gold != 0 || len(settled.Attachments) != 0 {
		t.Fatalf("settled receipt = %+v, want it removed from the inbox", settled)
	}
}

func TestMailboxExtractUsesPVFItemTabWithInitialExpansionMarker(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Name: "receiver"})
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: dnfrepo.CharacterContainerStateScope("77"),
		Values: map[string]string{
			"main_list_param16":           "0",
			"avatar_list_param16":         "0",
			"personal_cargo_list_param16": "8",
		},
	}); err != nil {
		t.Fatalf("save initial expansion marker: %v", err)
	}
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", Attachments: []dnfrepo.MailAttachment{{ItemID: 2001, Count: 2}}},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(121, 176, 99),
		Body:                currentMailIDList(1),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertMailboxExtractSuccessWithEmptyPage(t, got, currentExtractAck(1))
	inventory, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	if stack := inventory.Slots["0:121"]; stack.ItemID != 2001 || stack.Count != 2 {
		t.Fatalf("material claim = %+v, want item 2001 x2 in material slot 0:121", stack)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	if !box.Mails["1"].Claimed || !box.Mails["1"].Deleted || len(box.Mails["1"].Attachments) != 0 {
		t.Fatalf("successful claim should mark only after the material-tab insert and then finish: %+v", box.Mails["1"])
	}
}

func TestMailboxExtractKeepsAttachmentsWhenTheirPVFTabIsFull(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Name: "receiver", Stats: map[string]int64{"gold": 10}})
	slots := make(map[string]dnfrepo.ItemStack)
	for slot := int16(3); slot <= 8; slot++ {
		slots["0:"+strconv.Itoa(int(slot))] = dnfrepo.ItemStack{ItemID: 8000 + int64(slot), Count: 1}
	}
	for slot := int16(65); slot <= 120; slot++ {
		slots["0:"+strconv.Itoa(int(slot))] = dnfrepo.ItemStack{ItemID: 9000 + int64(slot), Count: 1}
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
		t.Fatalf("save full consumable tab: %v", err)
	}
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", Gold: 5, Attachments: []dnfrepo.MailAttachment{{ItemID: 2001, Count: 1}}},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99),
		Body:                currentMailIDList(1),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketMailboxExtractItem, []byte{0x00, 0x04})
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	if box.Mails["1"].Claimed || len(box.Mails["1"].Attachments) != 1 {
		t.Fatalf("full-tab rejection must retain the attachment: %+v", box.Mails["1"])
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats["gold"] != 10 {
		t.Fatalf("full-tab rejection must roll back gold, got %d", character.Stats["gold"])
	}
}

func TestMailboxExtractBatchFillsQuickSlotsAndItemTabBeforeLeavingFullMail(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Name: "receiver", Stats: map[string]int64{"gold": 0}})
	mails := make(map[string]dnfrepo.MailRecord)
	for id := uint32(1); id <= 8; id++ {
		mails[strconv.FormatUint(uint64(id), 10)] = dnfrepo.MailRecord{
			MailID: strconv.FormatUint(uint64(id), 10),
			Gold:   1,
			Attachments: []dnfrepo.MailAttachment{{
				ItemID: int64(2000 + id),
				Count:  99,
			}},
		}
	}
	mails["9"] = dnfrepo.MailRecord{
		MailID: "9",
		Gold:   9,
		Attachments: []dnfrepo.MailAttachment{{
			ItemID: 2009,
			Count:  1,
		}},
	}
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77", Mails: mails}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(65, 66, 99),
		Body:                currentMailIDList(1, 2, 3, 4, 5, 6, 7, 8, 9),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	// 本次领取的 1..8 全部领完即结清（claimed_letter.go 默认档），补发的 0x61 页里
	// 只剩没领到的那封满信（mail 9）—— 旧断言钉的是"已领取的信留作空收据留在页里"，
	// 现改为钉住"页面里剩下的就是还能领的那一封"。
	if len(got.UpperResponses) != 3 {
		t.Fatalf("response count = %d, want op95 ACK, class0/0x61 snapshot and full-inventory popup", len(got.UpperResponses))
	}
	assertMailboxResponse(t, got.UpperResponses[2], uint16(dnfenum.CmdPacketMailboxExtractItem), dnfproto.DefaultChannelClassification, []byte{0, 4})
	assertMailboxResponse(t, got.UpperResponses[0], uint16(dnfenum.CmdPacketMailboxExtractItem), dnfproto.DefaultChannelClassification, currentExtractAck(1, 2, 3, 4, 5, 6, 7, 8))
	page := got.UpperResponses[1]
	if page.MsgID != mailboxListNotificationMessageID || page.Classification != 0 {
		t.Fatalf("extract page = msg 0x%04X class %d, want class0/0x61", page.MsgID, page.Classification)
	}
	walked := invWalkMailboxList(t, page.Body, []bool{false})
	if walked.summaryCount != 1 || walked.totalMails != 1 || !walked.summaries[0].hasItem {
		t.Fatalf("refreshed page summaries=%d total=%d hasItem=%v, want only the still-claimable full mail",
			walked.summaryCount, walked.totalMails, walked.summaries[0].hasItem)
	}
	if len(got.PostActions) != 0 {
		t.Fatalf("post actions = %v, want no full container refresh", got.PostActions)
	}
	wantRefreshes := []alignedcmd.ItemSlotRefresh{
		{ListType: 0, SlotIndex: 3}, {ListType: 0, SlotIndex: 4}, {ListType: 0, SlotIndex: 5},
		{ListType: 0, SlotIndex: 6}, {ListType: 0, SlotIndex: 7}, {ListType: 0, SlotIndex: 8},
		{ListType: 0, SlotIndex: 65}, {ListType: 0, SlotIndex: 66},
	}
	if !reflect.DeepEqual(got.ItemSlotRefreshes, wantRefreshes) {
		t.Fatalf("item slot refreshes = %+v, want %+v", got.ItemSlotRefreshes, wantRefreshes)
	}

	inventory, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	for index, slot := range []int16{3, 4, 5, 6, 7, 8, 65, 66} {
		stack := inventory.Slots["0:"+strconv.Itoa(int(slot))]
		if stack.ItemID != int64(2001+index) || stack.Count != 99 {
			t.Fatalf("slot 0:%d = %+v, want item=%d count=99", slot, stack, 2001+index)
		}
	}
	box, _, err := repos.Mailbox.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	for id := 1; id <= 8; id++ {
		mail := box.Mails[strconv.Itoa(id)]
		if !mail.Claimed || mail.Gold != 0 || len(mail.Attachments) != 0 {
			t.Fatalf("mail %d should be committed: %+v", id, mail)
		}
	}
	if mail := box.Mails["9"]; mail.Claimed || mail.Gold != 9 || len(mail.Attachments) != 1 {
		t.Fatalf("full mail should remain claimable: %+v", mail)
	}
	character, _, err := repos.Character.Load(ctx, "77")
	if err != nil || character.Stats["gold"] != 8 {
		t.Fatalf("gold = %d err=%v, want only the eight committed letters", character.Stats["gold"], err)
	}
}

// TestMailboxExtractSettlesAlreadyClaimedLetters 钉住"已领取的信必须能被结清"。
//
// 旧用例名是 TestMailboxExtractBatchSkipsAlreadyClaimedLetters，钉的是"已领取的信
// 一律跳过、答复里不出现"的旧行为。现场（2026-09-28，角色 6）证明那条规则是死胡同：
// `claimed_flag=1 AND deleted_flag=0` 的空信既领不动（0x04）也没有任何路径能移走它，
// 玩家只能看着空邮件永远留在邮箱里。默认档（DNFBRIDGE_MAILBOX_CLAIMED_LETTER=finish，
// 见 claimed_letter.go）改成：领完即结清（移出收件箱）。
//
// 这里同时钉住三件事：
//   - 旧实现遗留形状（已领取 + 附件行还在）不会第二次发货（附件不发进背包）；
//   - 同一批里还没领取的信照旧正常领取，答复仍按请求顺序带上两封信；
//   - 两封信都结清之后，补发的 0x61 页是空页（客户端不会再画出空邮件）。
func TestMailboxExtractSettlesAlreadyClaimedLetters(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Name: "receiver"})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", Claimed: true, Read: true, Attachments: []dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}}},
			"2": {MailID: "2", Attachments: []dnfrepo.MailAttachment{{ItemID: 1002, Count: 1}}},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99),
		Body:                currentMailIDList(1, 2),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertMailboxExtractSuccessWithEmptyPage(t, got, currentExtractAck(1, 2))
	inventory, found, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load inventory found=%v err=%v", found, err)
	}
	if stack := inventory.Slots["0:3"]; stack.ItemID != 1002 || stack.Count != 1 {
		t.Fatalf("pending claim stack = %+v", stack)
	}
	if extra, exists := inventory.Slots["0:4"]; exists {
		t.Fatalf("settled letter re-delivered its stale attachment: %+v", extra)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	settled := box.Mails["1"]
	if !settled.Claimed || !settled.Deleted || len(settled.Attachments) != 0 || settled.Gold != 0 {
		t.Fatalf("already claimed letter was not settled out of the inbox: %+v", settled)
	}
	claimed := box.Mails["2"]
	if !claimed.Claimed || !claimed.Deleted || len(claimed.Attachments) != 0 {
		t.Fatalf("freshly claimed letter must also finish (specifically no empty receipt left): %+v", claimed)
	}
}

func TestMailboxPresentationTranslatesExistingOverflowMailWithoutMutatingIt(t *testing.T) {
	durable := dnfrepo.MailRecord{
		SenderName: "System",
		Title:      "Inventory full: box reward",
		Body:       "Your box reward was delivered here because the inventory is full.",
		Metadata:   map[string]string{"source": "magic_box_reward_inventory_full"},
	}
	presented := mailboxPresentationMail(durable)
	if presented.SenderName != "系统" || presented.Title != "背包已满：礼盒奖励" ||
		presented.Body != "背包空间不足，礼盒奖励已通过邮件发送。请清理对应道具分页后领取。" {
		t.Fatalf("presented overflow mail = %+v", presented)
	}
	if durable.SenderName != "System" || durable.Title != "Inventory full: box reward" ||
		durable.Body != "Your box reward was delivered here because the inventory is full." {
		t.Fatalf("presentation must not rewrite durable mail: %+v", durable)
	}
}

func TestMailboxOpenRepairsAssetsLeftOnAnAlreadyClaimedLetter(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", Claimed: true, Read: true, Gold: 7, Attachments: []dnfrepo.MailAttachment{{ItemID: 1001, Count: 1}}},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	opened, err := NewOwner(repos).Open(ctx, "77")
	if err != nil {
		t.Fatalf("open mailbox: %v", err)
	}
	if len(opened.Mails) != 1 || opened.Mails[0].Gold != 0 || len(opened.Mails[0].Attachments) != 0 {
		t.Fatalf("opened claimed letter still projects assets: %+v", opened.Mails)
	}
	box, _, err := repos.Mailbox.Load(ctx, "77")
	if err != nil || box.Mails["1"].Gold != 0 || len(box.Mails["1"].Attachments) != 0 {
		t.Fatalf("legacy claimed letter was not repaired: mailbox=%+v err=%v", box, err)
	}
}

// TestMailboxConcurrentExtractCommitsExactlyOnce 钉住"并发重复 op95 只发一次货"。
//
// 旧断言要求两条里一条成功、一条回 0x04（已领取）。claimed_letter.go 的默认档把
// "领取一封已经领取过的信"改成结清那张空收据（成功 + 移出收件箱），所以两条都可能
// 是成功答复 —— 但**资产只发一次**这条不变量不变：金币仍然只加 50，信最终被结清。
func TestMailboxConcurrentExtractCommitsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "77",
		Name:        "receiver",
		Stats:       map[string]int64{"gold": 10},
	})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"123": {MailID: "123", Gold: 50},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}
	request := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		Body:                currentMailIDList(123),
	}

	var wait sync.WaitGroup
	results := make(chan alignedcmd.Result, 2)
	errors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := NewHandler().Handle(ctx, request)
			results <- result
			errors <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent Handle error = %v", err)
		}
	}
	successes := 0
	alreadyClaimed := 0
	for result := range results {
		body := result.UpperResponses[0].Body
		switch {
		case bytes.Equal(body, currentExtractAck(123)):
			successes++
		case bytes.Equal(body, []byte{0x00, 21}):
			alreadyClaimed++
		default:
			t.Fatalf("unexpected concurrent extract body: % X", body)
		}
	}
	if successes+alreadyClaimed != 2 || successes == 0 {
		t.Fatalf("successes=%d already_claimed=%d, want two answered requests with at least one grant", successes, alreadyClaimed)
	}
	character, _, _ := repos.Character.Load(ctx, "77")
	if character.Stats["gold"] != 60 {
		t.Fatalf("concurrent extract gold = %d, want exactly one grant to 60", character.Stats["gold"])
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	if settled := box.Mails["123"]; !settled.Claimed || !settled.Deleted || settled.Gold != 0 {
		t.Fatalf("concurrently claimed letter = %+v, want the settled receipt removed from the inbox", settled)
	}
}

func TestMailboxExtractRejectsMissingMailID(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc-a", Name: "receiver"})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77"}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		Body:                currentMailIDList(999),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketMailboxExtractItem, []byte{0x00, 21})
	if len(got.PostActions) != 0 {
		t.Fatalf("post actions = %v, want none on rejection", got.PostActions)
	}
}

func TestMailboxChangeStateUsesCurrentOp134ResultLayout(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Name: "receiver"})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"123": {MailID: "123", Claimed: true},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketChangeLetterStat),
		SelectedCharacterID: 77,
		Repositories:        repos,
		Body:                currentStateBody(0, 123),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if len(got.UpperResponses) != 3 {
		t.Fatalf("response count = %d, want ACK plus class0/0x62 removal plus the refreshed class0/0x61 page", len(got.UpperResponses))
	}
	assertMailboxResponse(t, got.UpperResponses[0], uint16(dnfenum.CmdPacketChangeLetterStat), dnfproto.DefaultChannelClassification, currentStateAck(0, 123))
	assertMailboxResponse(t, got.UpperResponses[1], mailboxRemoveNotificationMessageID, 0, appendU32([]byte{1, 0, 0, 0}, 123))
	// 删除之后必须补发一份 clear-first 的 0x61 列表页：当前客户端靠它把已删的信件行
	// 从界面上抹掉，只发 0x62 移除通知它不重建列表（2026-09-28 现场）。
	page := got.UpperResponses[2]
	if page.MsgID != mailboxListNotificationMessageID || page.Classification != 0 {
		t.Fatalf("delete page = msg 0x%04X class %d, want class0/0x61", page.MsgID, page.Classification)
	}
	walked := invWalkMailboxList(t, page.Body, nil)
	if walked.summaryCount != 0 || walked.totalMails != 0 || walked.detailCount != 0 {
		t.Fatalf("refreshed page still carries rows: summary=%d total=%d detail=%d, want an empty page",
			walked.summaryCount, walked.totalMails, walked.detailCount)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	if !box.Mails["123"].Deleted {
		t.Fatalf("mail state was not committed: %+v", box.Mails["123"])
	}
}

// TestMailboxChangeStateDeleteAllRefreshesPageOnce 钉住操作者报的那个场景：
// 「全部接受」会一次带上多封信（现场是 op134 ids=[26 25 24] status=0），
// 这种批量删除必须**只补发一份**列表页，且这一份里已经没有那几封信。
func TestMailboxChangeStateDeleteAllRefreshesPageOnce(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "88", Name: "receiver"})
	now := time.Now().UTC()
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "88",
		Mails: map[string]dnfrepo.MailRecord{
			"24": {MailID: "24", Claimed: true, CreatedAt: now},
			"25": {MailID: "25", Claimed: true, CreatedAt: now},
			"26": {MailID: "26", Claimed: true, CreatedAt: now},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketChangeLetterStat),
		SelectedCharacterID: 88,
		Repositories:        repos,
		Body:                currentStateBody(0, 26, 25, 24),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if len(got.UpperResponses) != 3 {
		t.Fatalf("response count = %d, want ACK + one 0x62 removal + exactly one 0x61 page", len(got.UpperResponses))
	}
	// 移除通知一份，且按请求顺序带上三个 id。
	assertMailboxResponse(t, got.UpperResponses[1], mailboxRemoveNotificationMessageID, 0,
		append(appendU32([]byte{3, 0, 0, 0}, 26), appendU32(appendU32(nil, 25), 24)...))
	page := got.UpperResponses[2]
	if page.MsgID != mailboxListNotificationMessageID || page.Classification != 0 {
		t.Fatalf("delete page = msg 0x%04X class %d, want class0/0x61", page.MsgID, page.Classification)
	}
	walked := invWalkMailboxList(t, page.Body, nil)
	if walked.summaryCount != 0 || walked.totalMails != 0 {
		t.Fatalf("refreshed page still lists the deleted mails: summary=%d total=%d", walked.summaryCount, walked.totalMails)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "88")
	for _, id := range []string{"24", "25", "26"} {
		if !box.Mails[id].Deleted {
			t.Fatalf("mail %s was not deleted: %+v", id, box.Mails[id])
		}
	}
}

func TestMailboxChangeStateMarksReadAndSavesCurrentLetterStates(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"123": {MailID: "123", CreatedAt: time.Now().UTC()},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	request := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketChangeLetterStat),
		SelectedCharacterID: 77,
		Repositories:        repos,
	}
	request.Body = currentStateBody(2, 123)
	got, err := NewHandler().Handle(ctx, request)
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketChangeLetterStat, currentStateAck(2, 123))

	request.Body = currentStateBody(3, 123)
	got, err = NewHandler().Handle(ctx, request)
	if err != nil {
		t.Fatalf("save mail: %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketChangeLetterStat, currentStateAck(3, 123))
	box, _, err := repos.Mailbox.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	mail := box.Mails["123"]
	if !mail.Read || !mailboxMailSaved(mail) || mailboxLetterStat(mail) != 3 {
		t.Fatalf("mail state = %+v, want read/saved/stat3", mail)
	}
}

func TestMailboxOpenExpiresNormalMailButKeepsSavedMail(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", CreatedAt: created},
			"2": {MailID: "2", CreatedAt: created, Metadata: map[string]string{"mailbox_saved": "true"}},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}
	owner := NewOwner(repos)
	owner.now = func() time.Time { return created.Add(mailboxNormalLifetime + time.Second) }
	result, err := owner.Open(ctx, "77")
	if err != nil {
		t.Fatalf("open mailbox: %v", err)
	}
	if result.Total != 1 || len(result.Mails) != 1 || result.Mails[0].MailID != "2" || mailboxLetterStat(result.Mails[0]) != 3 {
		t.Fatalf("open result = %+v, want only saved mail", result)
	}
}

func TestMailboxAlarmNotificationUsesExactOneWordBody(t *testing.T) {
	if got := BuildAlarmNotification(513); !bytes.Equal(got, []byte{0x01, 0x02}) {
		t.Fatalf("class0/0x63 body = % X, want 01 02", got)
	}
}

func TestMailboxSnapshotProjectsCurrentAttachmentRowAndCreatureFraming(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	raw := make([]byte, mailboxItemRawSize)
	binary.LittleEndian.PutUint16(raw[0:2], 14)
	binary.LittleEndian.PutUint32(raw[2:6], 9999)
	binary.LittleEndian.PutUint32(raw[6:10], 99)
	binary.LittleEndian.PutUint16(raw[0x0b:0x0d], 55)
	raw[0x0d] = 2
	binary.LittleEndian.PutUint32(raw[0x0e:0x12], 12345)
	raw[0x12] = 6
	raw[0x13] = 7
	binary.LittleEndian.PutUint16(raw[0x14:0x16], 8)
	raw[0x20] = 0xA5

	mail := dnfrepo.MailRecord{
		MailID:            "7",
		SenderCharacterID: "11",
		SenderName:        "发件人",
		Body:              "附件邮件",
		CreatedAt:         now.Add(-time.Hour),
		ExpireAt:          now.Add(time.Hour),
		Gold:              50,
		Metadata:          map[string]string{"mail_type": "4"},
		Attachments: []dnfrepo.MailAttachment{{
			ItemID:   9999,
			Count:    2,
			Bind:     true,
			RawEntry: raw,
			Extra:    map[string]string{"mailbox_equipment_type": "creature"},
		}},
	}
	body, err := buildMailboxListNotification([]dnfrepo.MailRecord{mail}, 3, now)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if body[0] != 1 || body[1] != 0 {
		t.Fatalf("snapshot header = % X, want one initial summary", body[:2])
	}
	offset := 2
	if got := binary.LittleEndian.Uint32(body[offset : offset+4]); got != 7 {
		t.Fatalf("summary mail ID = %d, want 7", got)
	}
	offset += 4
	if got := binary.LittleEndian.Uint32(body[offset : offset+4]); got != 11 {
		t.Fatalf("summary sender ID = %d, want 11", got)
	}
	offset += 4
	senderLength := int(binary.LittleEndian.Uint32(body[offset : offset+4]))
	offset += 4 + senderLength
	if got := binary.LittleEndian.Uint32(body[offset : offset+4]); got != 50 {
		t.Fatalf("summary gold = %d, want 50", got)
	}
	offset += 4
	if got := binary.LittleEndian.Uint32(body[offset : offset+4]); got != 9999 || body[offset+4] != 1 {
		t.Fatalf("summary item header = % X", body[offset:offset+5])
	}
	offset += 5
	if got := binary.LittleEndian.Uint32(body[offset : offset+4]); got != 2 {
		t.Fatalf("summary item count = %d, want transferred count 2", got)
	}
	offset += 4 + 2 + 1 + 4 + 1 + 1 + 2
	if !bytes.Equal(body[offset:offset+5], []byte{0, 0, 0, 0, 0}) {
		t.Fatalf("creature framing = % X, want u32 zero plus zero detail flag", body[offset:offset+5])
	}
	offset += 5
	wantRaw := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint16(wantRaw[0:2], 0)
	binary.LittleEndian.PutUint32(wantRaw[2:6], 9999)
	binary.LittleEndian.PutUint32(wantRaw[6:10], 2)
	if !bytes.Equal(body[offset:offset+mailboxItemRawSize], wantRaw) {
		t.Fatalf("projected 0x77 item row = % X, want % X", body[offset:offset+mailboxItemRawSize], wantRaw)
	}
}

func TestMailboxQueryUsesCurrentOp324Layout(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "88",
		Name:        "receiver",
		Job:         "3",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 2},
	})
	body := append(dstrings("receiver"), 0x01)
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:       uint16(dnfenum.CmdPacketQueryCharacInfoMailbox),
		Repositories: repos,
		Body:         body,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	want := []byte{0x01}
	want = append(want, dstrings("receiver")...)
	want = append(want, 0x02, 90, 0, 0x03, 0x00, 0x01, 0x00, 0x00)
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketQueryCharacInfoMailbox, want)
}

func TestMailboxQueryRejectsMissingDurableProjection(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "88",
		Name:        "receiver",
		Job:         "not-a-current-job",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 2},
	})
	body := append(dstrings("receiver"), 0x01)
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:       uint16(dnfenum.CmdPacketQueryCharacInfoMailbox),
		Repositories: repos,
		Body:         body,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertOneMailboxResponse(t, got, dnfenum.CmdPacketQueryCharacInfoMailbox, []byte{0x00, 0x01})
}

func TestMailboxMalformedCurrentBodiesAreRejectedWithoutMutation(t *testing.T) {
	for _, opcode := range []dnfenum.CmdPacket{
		dnfenum.CmdPacketMailboxSend,
		dnfenum.CmdPacketMailboxExtractItem,
		dnfenum.CmdPacketMailboxOpen,
		dnfenum.CmdPacketChangeLetterStat,
		dnfenum.CmdPacketMultiMailboxSend,
		dnfenum.CmdPacketQueryCharacInfoMailbox,
	} {
		t.Run(strconv.Itoa(int(opcode)), func(t *testing.T) {
			got, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
				Opcode: uint16(opcode),
				Body:   []byte{0xFF, 0x00, 0x01},
			})
			if err != nil {
				t.Fatalf("Handle error = %v", err)
			}
			assertOneMailboxResponse(t, got, opcode, []byte{0x00, 0x01})
		})
	}
}

func TestCurrentMailboxPostageMatchesIDAFormula(t *testing.T) {
	tests := []struct {
		name            string
		gold            int64
		attachments     int
		attachmentValue int64
		want            int64
	}{
		{name: "message only", want: 100},
		{name: "gold", gold: 50, want: 102},
		{name: "one attachment", attachments: 1, attachmentValue: 200, want: 1110},
		{name: "two attachments", attachments: 2, attachmentValue: 400, want: 2120},
		{name: "all components", gold: 1000, attachments: 2, attachmentValue: 400, want: 2170},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := currentMailboxPostage(test.gold, test.attachments, test.attachmentValue)
			if err != nil {
				t.Fatalf("currentMailboxPostage error = %v", err)
			}
			if got != test.want {
				t.Fatalf("currentMailboxPostage = %d, want %d", got, test.want)
			}
		})
	}
}

func assertOneMailboxResponse(t *testing.T, got alignedcmd.Result, opcode dnfenum.CmdPacket, body []byte) {
	t.Helper()
	if !got.Handled {
		t.Fatalf("%d should be handled", opcode)
	}
	if !got.ResponseAllowed {
		t.Fatalf("%d should allow response", opcode)
	}
	if len(got.UpperResponses) != 1 {
		t.Fatalf("response count = %d, want 1", len(got.UpperResponses))
	}
	assertMailboxResponse(t, got.UpperResponses[0], uint16(opcode), dnfproto.DefaultChannelClassification, body)
}

func assertMailboxExtractSuccessWithSnapshot(t *testing.T, got alignedcmd.Result, ack []byte) {
	t.Helper()
	if !got.Handled || !got.ResponseAllowed {
		t.Fatalf("mailbox extract should be handled with a response: %+v", got)
	}
	if len(got.UpperResponses) != 2 {
		t.Fatalf("response count = %d, want op95 ACK plus class0/0x61 snapshot", len(got.UpperResponses))
	}
	assertMailboxResponse(t, got.UpperResponses[0], uint16(dnfenum.CmdPacketMailboxExtractItem), dnfproto.DefaultChannelClassification, ack)
	snapshot := got.UpperResponses[1]
	if snapshot.MsgID != mailboxListNotificationMessageID || snapshot.Classification != 0 || len(snapshot.Body) < 2 || snapshot.Body[1] != 0 {
		t.Fatalf("mailbox extract snapshot = %+v, want class0/0x61 clear-first page", snapshot)
	}
	if snapshot.Body[0] == 0 {
		t.Fatalf("mailbox extract snapshot has no retained claimed-letter seed: % X", snapshot.Body)
	}
	// The first summary is [mail ID][sender ID][sender DSTR][gold][item ID]
	// [has item]. Its assets must be zero after the transactional claim.
	offset := 2 + 4 + 4
	if len(snapshot.Body) < offset+4 {
		t.Fatalf("short mailbox extract summary: % X", snapshot.Body)
	}
	senderBytes := int(binary.LittleEndian.Uint32(snapshot.Body[offset : offset+4]))
	offset += 4 + senderBytes
	if senderBytes < 0 || len(snapshot.Body) < offset+9 {
		t.Fatalf("invalid mailbox extract sender summary: % X", snapshot.Body)
	}
	if gold, itemID, hasItem := binary.LittleEndian.Uint32(snapshot.Body[offset:offset+4]), binary.LittleEndian.Uint32(snapshot.Body[offset+4:offset+8]), snapshot.Body[offset+8]; gold != 0 || itemID != 0 || hasItem != 0 {
		t.Fatalf("claimed letter still has wire assets: gold=%d item=%d has_item=%d body=% X", gold, itemID, hasItem, snapshot.Body)
	}
}

// assertMailboxExtractSuccessWithEmptyPage 钉住 claimed_letter.go 默认档的结清契约：
// 请求里那些"已经领取过"的信在 op95 之后必须从收件箱消失，所以 ACK 之后补发的
// clear-first 0x61 页必须**一封都不剩**。空页本身是已验证形状：op134 全删之后补发的
// 那份快照就是这个形状（TestMailboxChangeStateDeleteAllRefreshesPageOnce，现场
// 2026-09-28）。
func assertMailboxExtractSuccessWithEmptyPage(t *testing.T, got alignedcmd.Result, ack []byte) {
	t.Helper()
	if !got.Handled || !got.ResponseAllowed {
		t.Fatalf("mailbox extract should be handled with a response: %+v", got)
	}
	if len(got.UpperResponses) != 2 {
		t.Fatalf("response count = %d, want op95 ACK plus class0/0x61 snapshot", len(got.UpperResponses))
	}
	assertMailboxResponse(t, got.UpperResponses[0], uint16(dnfenum.CmdPacketMailboxExtractItem), dnfproto.DefaultChannelClassification, ack)
	snapshot := got.UpperResponses[1]
	if snapshot.MsgID != mailboxListNotificationMessageID || snapshot.Classification != 0 {
		t.Fatalf("mailbox extract snapshot = %+v, want class0/0x61 clear-first page", snapshot)
	}
	walked := invWalkMailboxList(t, snapshot.Body, nil)
	if walked.summaryCount != 0 || walked.totalMails != 0 || walked.detailCount != 0 {
		t.Fatalf("settled letters are still listed: summary=%d total=%d detail=%d, want an empty page",
			walked.summaryCount, walked.totalMails, walked.detailCount)
	}
}

func assertMailboxResponse(t *testing.T, response alignedcmd.UpperResponse, messageID uint16, classification byte, body []byte) {
	t.Helper()
	if response.MsgID != messageID {
		t.Fatalf("msgID = %d, want %d", response.MsgID, messageID)
	}
	if response.Classification != classification {
		t.Fatalf("classification = %d, want %d", response.Classification, classification)
	}
	if !response.AllowCodec {
		t.Fatalf("%d should use normal game upper codec path", messageID)
	}
	if !bytes.Equal(response.Body, body) {
		t.Fatalf("body = % X, want % X", response.Body, body)
	}
}

func mustSaveCharacter(t *testing.T, repos dnfrepo.Group, record dnfrepo.CharacterRecord) {
	t.Helper()
	if err := repos.Character.Save(context.Background(), record); err != nil {
		t.Fatalf("save character %s: %v", record.CharacterID, err)
	}
}

func mailboxClaimTestResolver(start int16, end int16, stackLimit int64) alignedcmd.MailboxItemResolver {
	return func(uint32) (alignedcmd.MailboxItemResolution, error) {
		return alignedcmd.MailboxItemResolution{
			Kind:            "stackable",
			SlotStart:       start,
			SlotEnd:         end,
			StackLimit:      stackLimit,
			PreferQuickSlot: start >= 65 && end <= 120,
			AllowQuickSlots: start >= 65 && end <= 288,
		}, nil
	}
}

func currentSendBody(
	recipient string,
	gold uint32,
	attachments []SendAttachment,
	message string,
	special uint32,
	global bool,
	multi bool,
) []byte {
	out := dstrings(recipient)
	out = appendU32(out, gold)
	if multi {
		out = append(out, byte(len(attachments)))
	}
	for _, attachment := range attachments {
		out = append(out, attachment.ListType)
		var slot [2]byte
		binary.LittleEndian.PutUint16(slot[:], attachment.SlotIndex)
		out = append(out, slot[:]...)
		out = appendU32(out, attachment.ItemID)
		out = appendU32(out, attachment.Count)
	}
	out = append(out, dstrings(message)...)
	out = appendU32(out, special)
	if global {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	return out
}

func currentMailIDList(ids ...uint32) []byte {
	out := appendU32(nil, uint32(len(ids)))
	for _, id := range ids {
		out = appendU32(out, id)
	}
	return out
}

func currentExtractAck(ids ...uint32) []byte {
	out := []byte{1}
	out = appendU32(out, uint32(len(ids)))
	for _, id := range ids {
		out = appendU32(out, id)
		out = appendU32(out, id)
		out = appendU32(out, 0)
	}
	return out
}

func currentStateBody(status uint16, ids ...uint32) []byte {
	out := currentMailIDList(ids...)
	var state [2]byte
	binary.LittleEndian.PutUint16(state[:], status)
	return append(out, state[:]...)
}

func currentStateAck(status uint16, ids ...uint32) []byte {
	out := []byte{1}
	out = appendU32(out, uint32(len(ids)))
	for _, id := range ids {
		out = appendU32(out, id)
		var state [2]byte
		binary.LittleEndian.PutUint16(state[:], status)
		out = append(out, state[:]...)
	}
	return out
}

func dstrings(values ...string) []byte {
	var out []byte
	for _, value := range values {
		out = appendU32(out, uint32(len([]byte(value))))
		out = append(out, []byte(value)...)
	}
	return out
}

func appendU32(out []byte, value uint32) []byte {
	var encoded [4]byte
	binary.LittleEndian.PutUint32(encoded[:], value)
	return append(out, encoded[:]...)
}
