package mail

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	multiAttachmentIncidentFirstItem  int64 = 490702725
	multiAttachmentIncidentSecondItem int64 = 490702727
)

// Rebuilds the observed two-attachment shape. The
// character and account identifiers are intentionally synthetic; the public
// fixture carries only the PVF item identities, expirations and the occupied
// main-inventory ranges needed to reproduce the transaction.
func newMultiAttachmentIncidentGroup(t *testing.T, fullConsumableTab bool) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "mail-incident-account", Name: "receiver",
		Stats: map[string]int64{"gold": 0},
	})

	slots := map[string]dnfrepo.ItemStack{
		// Present in the incident snapshot and deliberately outside the two
		// consumable pages used by the attachments.
		"0:141": {ItemID: 7454, Count: 4},
		"0:174": {ItemID: 490702726, Count: 5},
		"0:175": {ItemID: 490702729, Count: 10},
	}
	for slot := int16(3); slot <= 8; slot++ {
		slots["0:"+strconv.Itoa(int(slot))] = dnfrepo.ItemStack{ItemID: 700000 + int64(slot), Count: 1}
	}
	if fullConsumableTab {
		for slot := int16(65); slot <= 120; slot++ {
			slots["0:"+strconv.Itoa(int(slot))] = dnfrepo.ItemStack{ItemID: 800000 + int64(slot), Count: 1}
		}
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
		t.Fatalf("save incident inventory: %v", err)
	}

	createdAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	incident := dnfrepo.MailRecord{
		MailID: "1", SenderCharacterID: "0", SenderName: "系统",
		Title: "多附件奖励", Body: "附件领取测试", Read: true, CreatedAt: createdAt,
		Attachments: []dnfrepo.MailAttachment{
			{ItemID: multiAttachmentIncidentFirstItem, Count: 1, ExpireAt: time.Date(2031, 12, 30, 22, 0, 0, 0, time.FixedZone("CST", 8*60*60))},
			{ItemID: multiAttachmentIncidentSecondItem, Count: 1, ExpireAt: time.Date(2031, 8, 15, 22, 0, 0, 0, time.FixedZone("CST", 8*60*60))},
		},
		Metadata: map[string]string{"source": "multi_attachment_incident_fixture", "system_mail": "true"},
	}
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"1": incident},
	}); err != nil {
		t.Fatalf("save incident mailbox: %v", err)
	}
	return repos
}

func openMultiAttachmentIncident(t *testing.T, repos dnfrepo.Group) (alignedcmd.Result, []uint32) {
	t.Helper()
	opened, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 77,
		Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(65, 120, 100),
	})
	if err != nil {
		t.Fatalf("open incident mailbox: %v", err)
	}
	ids := multiAttachmentIDs(t, opened)
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("incident attachment identities = %v, want two distinct ids", ids)
	}
	return opened, ids
}

func claimMultiAttachmentIncident(t *testing.T, repos dnfrepo.Group, ids ...uint32) alignedcmd.Result {
	t.Helper()
	got, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 77,
		Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(65, 120, 100),
		Body: currentMailIDList(ids...),
	})
	if err != nil {
		t.Fatalf("claim incident attachments %v: %v", ids, err)
	}
	assertNoMailboxManagerRebuild(t, got, "claim")
	return got
}

func assertNoMailboxManagerRebuild(t *testing.T, got alignedcmd.Result, operation string) {
	t.Helper()
	if !got.Handled || !got.ResponseAllowed {
		t.Fatalf("%s should be handled with a response: %+v", operation, got)
	}
	for _, response := range got.UpperResponses {
		if response.Classification == 0 && response.MsgID == mailboxListNotificationMessageID {
			t.Fatalf("%s rebuilt the complete mailbox manager: %+v", operation, response)
		}
	}
}

func assertIncidentClaimAck(t *testing.T, got alignedcmd.Result, claimIDs ...uint32) {
	t.Helper()
	if len(got.UpperResponses) != 1 {
		t.Fatalf("claim response count = %d, want only op95 ACK", len(got.UpperResponses))
	}
	ack := []byte{1}
	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(claimIDs)))
	ack = append(ack, count[:]...)
	for _, claimID := range claimIDs {
		var row [12]byte
		binary.LittleEndian.PutUint32(row[0:4], 1)
		binary.LittleEndian.PutUint32(row[4:8], claimID)
		ack = append(ack, row[:]...)
	}
	assertMailboxResponse(t, got.UpperResponses[0], uint16(dnfenum.CmdPacketMailboxExtractItem), dnfproto.DefaultChannelClassification, ack)
	actual := got.UpperResponses[0].Body
	if len(actual) != 5+12*len(claimIDs) || actual[0] != 1 || binary.LittleEndian.Uint32(actual[1:5]) != uint32(len(claimIDs)) {
		t.Fatalf("claim ACK body=% X ids=%v", actual, claimIDs)
	}
	for i, claimID := range claimIDs {
		row := actual[5+i*12 : 17+i*12]
		if binary.LittleEndian.Uint32(row[:4]) != 1 || binary.LittleEndian.Uint32(row[4:8]) != claimID || binary.LittleEndian.Uint32(row[8:]) != 0 {
			t.Fatalf("claim ACK row %d=% X id=%d", i, row, claimID)
		}
	}
}

func incidentInventoryCount(slots map[string]dnfrepo.ItemStack, itemID int64) int64 {
	var total int64
	for _, stack := range slots {
		if stack.ItemID == itemID {
			total += stack.Count
		}
	}
	return total
}

func TestMailboxTwoAttachmentIncidentPartialAndAllAccept(t *testing.T) {
	t.Setenv(MailboxClaimedLetterEnv, string(ClaimedLetterFinish))
	ctx := context.Background()

	t.Run("partial_then_explicit_reopen", func(t *testing.T) {
		repos := newMultiAttachmentIncidentGroup(t, false)
		_, ids := openMultiAttachmentIncident(t, repos)

		first := claimMultiAttachmentIncident(t, repos, ids[0])
		assertIncidentClaimAck(t, first, ids[0])
		inventory, found, err := repos.Inventory.Load(ctx, "77")
		if err != nil || !found {
			t.Fatalf("load inventory found=%t err=%v", found, err)
		}
		if got := incidentInventoryCount(inventory.Slots, multiAttachmentIncidentFirstItem); got != 1 {
			t.Fatalf("first attachment count=%d, want 1", got)
		}
		if got := incidentInventoryCount(inventory.Slots, multiAttachmentIncidentSecondItem); got != 0 {
			t.Fatalf("second attachment was not left for its own ACK: %d", got)
		}
		box, _, _ := repos.Mailbox.Load(ctx, "77")
		mail := box.Mails["1"]
		if mail.Claimed || len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != multiAttachmentIncidentSecondItem {
			t.Fatalf("partial claim state=%+v", mail)
		}

		// The consumed claim id cannot deliver the sibling or mutate inventory again.
		replay := claimMultiAttachmentIncident(t, repos, ids[0])
		if len(replay.UpperResponses) != 1 || len(replay.UpperResponses[0].Body) < 2 || replay.UpperResponses[0].Body[0] != 0 {
			t.Fatalf("repeated claim response=%+v", replay.UpperResponses)
		}
		afterReplay, _, _ := repos.Inventory.Load(ctx, "77")
		if incidentInventoryCount(afterReplay.Slots, multiAttachmentIncidentFirstItem) != 1 {
			t.Fatal("repeated claim delivered the first attachment again")
		}

		second := claimMultiAttachmentIncident(t, repos, ids[1])
		assertIncidentClaimAck(t, second, ids[1])
		finalInventory, _, _ := repos.Inventory.Load(ctx, "77")
		if incidentInventoryCount(finalInventory.Slots, multiAttachmentIncidentFirstItem) != 1 || incidentInventoryCount(finalInventory.Slots, multiAttachmentIncidentSecondItem) != 1 {
			t.Fatalf("final incident inventory=%+v", finalInventory.Slots)
		}

		reopened, err := NewHandler().Handle(ctx, alignedcmd.Request{
			Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 77,
			Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(65, 120, 100),
		})
		if err != nil || len(reopened.UpperResponses) != 2 {
			t.Fatalf("explicit reopen=%+v err=%v", reopened, err)
		}
		page := reopened.UpperResponses[1]
		walked := invWalkMailboxList(t, page.Body, nil)
		if page.MsgID != mailboxListNotificationMessageID || walked.summaryCount != 0 || walked.totalMails != 0 {
			t.Fatalf("settled incident remained after reopen: msg=%d summary=%d total=%d", page.MsgID, walked.summaryCount, walked.totalMails)
		}
	})

	t.Run("all_accept_is_one_ack", func(t *testing.T) {
		repos := newMultiAttachmentIncidentGroup(t, false)
		_, ids := openMultiAttachmentIncident(t, repos)
		got := claimMultiAttachmentIncident(t, repos, ids...)
		assertIncidentClaimAck(t, got, ids...)
		inventory, _, _ := repos.Inventory.Load(ctx, "77")
		if incidentInventoryCount(inventory.Slots, multiAttachmentIncidentFirstItem) != 1 || incidentInventoryCount(inventory.Slots, multiAttachmentIncidentSecondItem) != 1 {
			t.Fatalf("all-accept inventory=%+v", inventory.Slots)
		}
	})
}

func TestMailboxTwoAttachmentIncidentKeepsBothWhenConsumablePagesAreFull(t *testing.T) {
	t.Setenv(MailboxClaimedLetterEnv, string(ClaimedLetterFinish))
	repos := newMultiAttachmentIncidentGroup(t, true)
	_, ids := openMultiAttachmentIncident(t, repos)

	got := claimMultiAttachmentIncident(t, repos, ids...)
	if len(got.UpperResponses) != 1 {
		t.Fatalf("full-bag response count=%d, want one error ACK", len(got.UpperResponses))
	}
	ack := got.UpperResponses[0].Body
	if len(ack) != 2 || ack[0] != 0 || ack[1] != 4 {
		t.Fatalf("full-bag ACK=% X, want [00 04]", ack)
	}

	box, _, _ := repos.Mailbox.Load(context.Background(), "77")
	mail := box.Mails["1"]
	if mail.Claimed || mail.Deleted || len(mail.Attachments) != 2 || mail.Attachments[0].ItemID != multiAttachmentIncidentFirstItem || mail.Attachments[1].ItemID != multiAttachmentIncidentSecondItem {
		t.Fatalf("full-bag claim mutated the incident mail: %+v", mail)
	}
	inventory, _, _ := repos.Inventory.Load(context.Background(), "77")
	if incidentInventoryCount(inventory.Slots, multiAttachmentIncidentFirstItem) != 0 || incidentInventoryCount(inventory.Slots, multiAttachmentIncidentSecondItem) != 0 {
		t.Fatalf("full-bag claim delivered an attachment: %+v", inventory.Slots)
	}
	if incidentInventoryCount(inventory.Slots, 7454) != 4 {
		t.Fatalf("full-bag claim changed the unrelated coin stack: %+v", inventory.Slots["0:141"])
	}
}

type rejectMailboxAssetTransaction struct {
	dnfrepo.MailboxAssetUnitOfWork
}

func (rejectMailboxAssetTransaction) WithinMailboxAssets(context.Context, string, string, func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.MailboxRepository) error) error {
	return errors.New("injected mailbox asset failure")
}

func TestMailboxTwoAttachmentIncidentTransactionFailureDoesNotRebuildOrMutate(t *testing.T) {
	repos := newMultiAttachmentIncidentGroup(t, false)
	_, ids := openMultiAttachmentIncident(t, repos)
	before, _, _ := repos.Mailbox.Load(context.Background(), "77")
	repos.MailboxAssets = rejectMailboxAssetTransaction{MailboxAssetUnitOfWork: repos.MailboxAssets}

	got := claimMultiAttachmentIncident(t, repos, ids[0])
	if len(got.UpperResponses) != 1 || len(got.UpperResponses[0].Body) < 2 || got.UpperResponses[0].Body[0] != 0 {
		t.Fatalf("failed transaction response=%+v", got.UpperResponses)
	}
	after, _, _ := repos.Mailbox.Load(context.Background(), "77")
	if len(after.Mails["1"].Attachments) != len(before.Mails["1"].Attachments) || after.Mails["1"].Claimed {
		t.Fatalf("failed transaction mutated mail: before=%+v after=%+v", before.Mails["1"], after.Mails["1"])
	}
}
