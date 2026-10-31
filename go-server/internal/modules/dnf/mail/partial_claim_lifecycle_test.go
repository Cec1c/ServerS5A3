package mail

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMailboxSingleAttachmentClaimKeepsNativeParentUntilLastClaim(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	mustSaveCharacter(t, g, repo.CharacterRecord{CharacterID: "77"})
	_, err := repo.AppendSystemMail(ctx, g.Mailbox, repo.SystemMailDelivery{
		RecipientCharacterID: "77", Source: "magic_box_reward_inventory_full", CreatedAt: time.Now(),
		Attachments: []repo.MailAttachment{{ItemID: 1001, Count: 1}, {ItemID: 1002, Count: 2}, {ItemID: 1003, Count: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := alignedcmd.Request{Opcode: 96, SelectedCharacterID: 77, Repositories: g, MailboxItemResolver: mailboxClaimTestResolver(65, 120, 99)}
	h := NewHandler()
	opened, err := h.Handle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	ids := multiAttachmentIDs(t, opened)
	if len(ids) != 3 {
		t.Fatalf("claim identities=%v", ids)
	}
	for index, id := range ids {
		req.Opcode, req.Body = 95, currentMailIDList(id)
		got, err := h.Handle(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		ack := got.UpperResponses[0]
		if ack.MsgID != 95 || ack.Body[0] != 1 || len(ack.Body) != 17 || binary.LittleEndian.Uint32(ack.Body[9:13]) != id {
			t.Fatalf("attachment %d ACK=%+v", index, ack)
		}
		pages := 0
		for _, response := range got.UpperResponses {
			if response.Classification == 0 && response.MsgID == 97 {
				pages++
			}
		}
		if pages != 0 {
			t.Fatalf("claim %d rebuilt native managers with %d clear-first pages", index, pages)
		}
		box, _, _ := g.Mailbox.Load(ctx, "77")
		if len(box.Mails["1"].Attachments) != 2-index {
			t.Fatalf("wrong remaining attachments: %+v", box.Mails["1"])
		}
	}
	inv, _, _ := g.Inventory.Load(ctx, "77")
	counts := map[int64]int64{}
	for _, stack := range inv.Slots {
		counts[stack.ItemID] += stack.Count
	}
	if counts[1001] != 1 || counts[1002] != 2 || counts[1003] != 1 {
		t.Fatalf("claimed assets=%v", counts)
	}
	req.Opcode, req.Body = 96, nil
	reopened, err := h.Handle(ctx, req)
	if err != nil || len(reopened.UpperResponses) != 2 {
		t.Fatalf("reopen=%+v err=%v", reopened, err)
	}
	walk := invWalkMailboxList(t, reopened.UpperResponses[1].Body, nil)
	if walk.summaryCount != 0 || walk.totalMails != 0 {
		t.Fatalf("settled mail reappeared: %+v", walk)
	}
}
