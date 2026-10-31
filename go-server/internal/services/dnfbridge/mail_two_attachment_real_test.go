package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/mail"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func incidentTwoAttachmentItems() []dnfrepo.MailAttachment {
	zone := time.FixedZone("CST", 8*60*60)
	return []dnfrepo.MailAttachment{
		{ItemID: 490702725, Count: 1, ExpireAt: time.Date(2031, 12, 30, 22, 0, 0, 0, zone)},
		{ItemID: 490702727, Count: 1, ExpireAt: time.Date(2031, 8, 15, 22, 0, 0, 0, zone)},
	}
}

func incidentMailIDList(ids ...uint32) []byte {
	body := make([]byte, 4+4*len(ids))
	binary.LittleEndian.PutUint32(body, uint32(len(ids)))
	for i, id := range ids {
		binary.LittleEndian.PutUint32(body[4+4*i:], id)
	}
	return body
}

func incidentMailClaimIDs(t *testing.T, repos dnfrepo.Group) []uint32 {
	t.Helper()
	box, found, err := repos.Mailbox.Load(context.Background(), "77")
	if err != nil || !found {
		t.Fatalf("load opened mailbox found=%t err=%v", found, err)
	}
	raw := strings.TrimSpace(box.Mails["1"].Metadata["mailbox_claim_ids"])
	if raw == "" {
		t.Fatal("desktop open did not persist claim identities")
	}
	parts := strings.Split(raw, ",")
	ids := make([]uint32, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		if err != nil || value == 0 {
			t.Fatalf("claim identity %q", part)
		}
		ids = append(ids, uint32(value))
	}
	return ids
}

func newRealIncidentMailGroup(t *testing.T, resolutions []alignedcmd.MailboxItemResolution, full bool) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "mail-119-fixture", Name: "receiver", Stats: map[string]int64{"gold": 0}}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	slots := map[string]dnfrepo.ItemStack{
		"0:141": {ItemID: 7454, Count: 4},
		"0:174": {ItemID: 490702726, Count: 5},
		"0:175": {ItemID: 490702729, Count: 10},
	}
	for slot := int16(3); slot <= 8; slot++ {
		slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 700000 + int64(slot), Count: 1}
	}
	if full {
		filler := int64(800000)
		for _, resolution := range resolutions {
			for slot := resolution.SlotStart; slot <= resolution.SlotEnd; slot++ {
				filler++
				slots[fmt.Sprintf("%d:%d", resolution.TargetListType, slot)] = dnfrepo.ItemStack{ItemID: filler, Count: 1}
			}
		}
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	mailbox := dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{
		"1": {
			MailID: "1", SenderCharacterID: "0", SenderName: "系统", Title: "多附件奖励", Body: "领取测试", Read: true,
			CreatedAt:   time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
			Attachments: incidentTwoAttachmentItems(),
			Metadata:    map[string]string{"source": "mail_two_attachment_fixture", "system_mail": "true", "unlimited": "true"},
		},
	}}
	if err := repos.Mailbox.Save(ctx, mailbox); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}
	return repos
}

func realIncidentMailResolvers(t *testing.T) (*Service, alignedcmd.MailboxItemResolver, alignedcmd.MailboxItemResolver) {
	t.Helper()
	_, catalog := realPVF535(t)
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9"}, pvfItemCatalog: catalog}
	openResolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxOpen)
	if err != nil || openResolver == nil {
		t.Fatalf("open resolver=%v err=%v", openResolver, err)
	}
	claimResolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil || claimResolver == nil {
		t.Fatalf("claim resolver=%v err=%v", claimResolver, err)
	}
	return service, openResolver, claimResolver
}

func TestRealPVFTwoAttachmentMailClaimAndFullBag(t *testing.T) {
	_, openResolver, claimResolver := realIncidentMailResolvers(t)
	resolutions := make([]alignedcmd.MailboxItemResolution, 0, 2)
	for _, attachment := range incidentTwoAttachmentItems() {
		resolution, err := claimResolver(uint32(attachment.ItemID))
		if err != nil {
			t.Fatalf("resolve incident item %d: %v", attachment.ItemID, err)
		}
		if resolution.Kind == "" || resolution.SlotEnd < resolution.SlotStart {
			t.Fatalf("incident item %d resolution=%+v", attachment.ItemID, resolution)
		}
		resolutions = append(resolutions, resolution)
	}

	claim := func(repos dnfrepo.Group, ids ...uint32) alignedcmd.Result {
		t.Helper()
		result, err := mail.NewHandler().Handle(context.Background(), alignedcmd.Request{
			Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 77,
			Repositories: repos, MailboxItemResolver: claimResolver, Body: incidentMailIDList(ids...),
		})
		if err != nil {
			t.Fatalf("claim ids=%v err=%v", ids, err)
		}
		for _, response := range result.UpperResponses {
			if response.Classification == 0 && response.MsgID == 0x61 {
				t.Fatalf("op95 rebuilt the complete mailbox manager: %+v", response)
			}
		}
		return result
	}

	t.Run("claim_both_with_space", func(t *testing.T) {
		repos := newRealIncidentMailGroup(t, resolutions, false)
		opened, err := mail.NewHandler().Handle(context.Background(), alignedcmd.Request{
			Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 77,
			Repositories: repos, MailboxItemResolver: openResolver,
		})
		if err != nil || len(opened.UpperResponses) != 2 {
			t.Fatalf("open response=%+v err=%v", opened, err)
		}
		ids := incidentMailClaimIDs(t, repos)
		if len(ids) != 2 {
			t.Fatalf("claim identities=%v", ids)
		}
		claimed := claim(repos, ids...)
		if len(claimed.UpperResponses) != 1 || len(claimed.UpperResponses[0].Body) != 29 || claimed.UpperResponses[0].Body[0] != 1 {
			t.Fatalf("claim response=%+v", claimed.UpperResponses)
		}
		inventory, _, _ := repos.Inventory.Load(context.Background(), "77")
		for _, attachment := range incidentTwoAttachmentItems() {
			var count int64
			for _, stack := range inventory.Slots {
				if stack.ItemID == attachment.ItemID {
					count += stack.Count
				}
			}
			if count != 1 {
				t.Fatalf("item %d count=%d", attachment.ItemID, count)
			}
		}
	})

	t.Run("full_destination_keeps_both", func(t *testing.T) {
		repos := newRealIncidentMailGroup(t, resolutions, true)
		opened, err := mail.NewHandler().Handle(context.Background(), alignedcmd.Request{
			Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 77,
			Repositories: repos, MailboxItemResolver: openResolver,
		})
		if err != nil || len(opened.UpperResponses) != 2 {
			t.Fatalf("open response=%+v err=%v", opened, err)
		}
		ids := incidentMailClaimIDs(t, repos)
		claimed := claim(repos, ids...)
		if len(claimed.UpperResponses) != 1 || len(claimed.UpperResponses[0].Body) != 2 || claimed.UpperResponses[0].Body[0] != 0 || claimed.UpperResponses[0].Body[1] != 4 {
			t.Fatalf("full-bag response=%+v", claimed.UpperResponses)
		}
		box, _, _ := repos.Mailbox.Load(context.Background(), "77")
		if box.Mails["1"].Claimed || len(box.Mails["1"].Attachments) != 2 {
			t.Fatalf("full-bag mail=%+v", box.Mails["1"])
		}
		inventory, _, _ := repos.Inventory.Load(context.Background(), "77")
		for _, attachment := range incidentTwoAttachmentItems() {
			for _, stack := range inventory.Slots {
				if stack.ItemID == attachment.ItemID {
					t.Fatalf("full-bag claim delivered %d: %+v", attachment.ItemID, stack)
				}
			}
		}
	})
}
