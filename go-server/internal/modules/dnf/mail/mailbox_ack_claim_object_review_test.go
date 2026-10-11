package mail

import (
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

// Current82e0 native summary/apply/retire proof requires ACK secondu32 to match
// the summary claim object. Commit/rejection tests below preserve asset ownership.
func TestMailboxClaimACKMatchesSentSummaryObjectAndPreservesOwner(t *testing.T) {
	for _, name := range []string{"single", "multiple_attachments", "other_selected_owner", "resolver_failure"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			for _, id := range []string{"77", "78"} {
				mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: id, AccountID: "account-" + id, Stats: map[string]int64{"gold": 10}})
			}
			attachments := []dnfrepo.MailAttachment{{ItemID: 1001, Count: 2}}
			if name == "multiple_attachments" {
				attachments = append(attachments, dnfrepo.MailAttachment{ItemID: 1002, Count: 3})
			}
			mail := dnfrepo.MailRecord{MailID: "123", RecipientCharacterID: "77", Gold: 50, Attachments: attachments}
			if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"123": mail}}); err != nil {
				t.Fatal(err)
			}
			selected := uint16(77)
			if name == "other_selected_owner" {
				selected = 78
			}
			resolver := mailboxClaimTestResolver(65, 120, 99)
			if name == "resolver_failure" {
				resolver = func(uint32) (alignedcmd.MailboxItemResolution, error) {
					return alignedcmd.MailboxItemResolution{}, errors.New("independent resource unavailable")
				}
			}
			req := alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: selected, Repositories: repos, MailboxItemResolver: resolver, Body: currentMailIDList(123)}
			h := NewHandler()
			open, err := h.Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 77, Repositories: repos})
			if err != nil {
				t.Fatal(err)
			}
			var claim uint32
			for _, p := range open.UpperResponses {
				if p.MsgID == 97 {
					claim = binary.LittleEndian.Uint32(p.Body[2:6])
				}
			}
			if claim != 123 {
				t.Fatal("preclaim summary claim object", claim)
			}
			result, err := h.Handle(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			success := name == "single" || name == "multiple_attachments"
			if len(result.UpperResponses) == 0 {
				t.Fatal("missing handler ACK")
			}
			ack := result.UpperResponses[0].Body
			owner, _, _ := repos.Character.Load(ctx, "77")
			inventory, _, _ := repos.Inventory.Load(ctx, "77")
			counts := map[int64]int64{}
			for _, stack := range inventory.Slots {
				counts[stack.ItemID] += stack.Count
			}
			box, _, _ := repos.Mailbox.Load(ctx, "77")
			if !success {
				if ack[0] != 0 || owner.Stats["gold"] != 10 || counts[1001] != 0 || box.Mails["123"].Claimed {
					t.Fatal("rejected request changed owner assets", ack, owner.Stats, counts, box)
				}
				return
			}
			if len(ack) != 17 || ack[0] != 1 || binary.LittleEndian.Uint32(ack[1:5]) != 1 || binary.LittleEndian.Uint32(ack[5:9]) != 123 || binary.LittleEndian.Uint32(ack[9:13]) != claim || binary.LittleEndian.Uint32(ack[13:17]) != 0 {
				t.Fatalf("committed ACK does not match sent current-native summary object: summary=%d ack=%x", claim, ack)
			}
			if owner.Stats["gold"] != 60 || counts[1001] != 2 || !box.Mails["123"].Claimed || !box.Mails["123"].Deleted {
				t.Fatal("successful claim not durable", owner.Stats, counts, box)
			}
			if name == "multiple_attachments" && counts[1002] != 3 {
				t.Fatal("second attachment missing", counts)
			}
			duplicate, err := h.Handle(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if duplicate.UpperResponses[0].Body[0] != 0 {
				t.Fatal("repeat claim succeeded")
			}
			again, _, _ := repos.Character.Load(ctx, "77")
			inv2, _, _ := repos.Inventory.Load(ctx, "77")
			counts2 := map[int64]int64{}
			for _, stack := range inv2.Slots {
				counts2[stack.ItemID] += stack.Count
			}
			if again.Stats["gold"] != 60 || counts2[1001] != counts[1001] || counts2[1002] != counts[1002] {
				t.Fatal("duplicate reward", again.Stats, counts2)
			}
		})
	}
}
