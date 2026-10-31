package mail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func mailboxWireTypeTestResolver(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
	switch itemID {
	case 1001:
		return alignedcmd.MailboxItemResolution{
			Kind: "stackable", SlotStart: 65, SlotEnd: 120, StackLimit: 99,
			PreferQuickSlot: true, AllowQuickSlots: true,
		}, nil
	case 25:
		return alignedcmd.MailboxItemResolution{
			Kind: "stackable", EquipmentType: "[creature]",
			TargetListType: 7, SlotStart: 189, SlotEnd: 238, StackLimit: 99,
		}, nil
	case 40734:
		return alignedcmd.MailboxItemResolution{
			Kind: "equipment", EquipmentType: "[Coat Avatar]",
			TargetListType: 1, SlotStart: 0, SlotEnd: 500, StackLimit: 1,
		}, nil
	default:
		return alignedcmd.MailboxItemResolution{}, errors.New("unexpected item")
	}
}

func TestMailboxOpenAndPartialClaimNormalizeMultiAttachmentWireTypes(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77"})
	now := time.Now().UTC()
	mail := invMail("123", func(m *dnfrepo.MailRecord) {
		m.CreatedAt = now
		m.Attachments = []dnfrepo.MailAttachment{
			{ItemID: 1001, Count: 2},
			{
				ItemID: 25, Count: 1,
				Extra: map[string]string{"mailbox_equipment_type": "[material]"},
			},
		}
	})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"123": mail}}); err != nil {
		t.Fatal(err)
	}

	h := NewHandler()
	opened, err := h.Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketMailboxOpen), SelectedCharacterID: 77,
		Repositories: repos, MailboxItemResolver: mailboxWireTypeTestResolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.UpperResponses) < 2 || opened.UpperResponses[1].MsgID != mailboxListNotificationMessageID {
		t.Fatalf("open responses = %+v", opened.UpperResponses)
	}
	walk := invWalkMailboxList(t, opened.UpperResponses[1].Body, []bool{false, true})
	if len(walk.summaries) != 2 {
		t.Fatalf("open summaries = %d, want 2", len(walk.summaries))
	}
	firstClaimID := binary.LittleEndian.Uint32(opened.UpperResponses[1].Body[walk.summaries[0].start:])

	claimed, err := h.Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 77,
		Repositories: repos, MailboxItemResolver: mailboxWireTypeTestResolver,
		Body: currentMailIDList(firstClaimID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed.UpperResponses) != 1 || len(claimed.UpperResponses[0].Body) != 17 {
		t.Fatalf("partial claim responses = %+v", claimed.UpperResponses)
	}
	reopened, err := h.Handle(ctx, alignedcmd.Request{
		Opcode: 96, SelectedCharacterID: 77, Repositories: repos, MailboxItemResolver: mailboxWireTypeTestResolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	var remaining []byte
	for _, response := range reopened.UpperResponses[1:] {
		if response.MsgID == mailboxListNotificationMessageID && response.Classification == 0 {
			remaining = response.Body
			break
		}
	}
	if len(remaining) == 0 {
		t.Fatal("explicit reopen did not return the remaining attachment")
	}
	invWalkMailboxList(t, remaining, []bool{true})

	stored, _, err := repos.Mailbox.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Mails["123"].Attachments[0].Extra["mailbox_equipment_type"]; got != "[material]" {
		t.Fatalf("send-only normalization mutated durable Extra: %q", got)
	}
}

func TestMailboxAvatarTypeNormalizationMatchesExplicitWireControl(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	mail := invMail("9", func(m *dnfrepo.MailRecord) {
		m.CreatedAt = now
		m.Attachments = []dnfrepo.MailAttachment{{
			ItemID: 40734, Count: 1,
			Extra: map[string]string{
				"avatar_socket_data": "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d",
				"avatar_color_data":  "01020304",
			},
		}}
	})
	got, err := buildMailboxListNotificationWithTypeResolver(
		[]dnfrepo.MailRecord{mail}, 0, now, mailboxWireTypeTestResolver,
	)
	if err != nil {
		t.Fatal(err)
	}
	controlMail := cloneMailboxMail(mail)
	controlMail.Attachments[0].Extra["mailbox_pvf_kind"] = "equipment"
	controlMail.Attachments[0].Extra["mailbox_equipment_type"] = "coat avatar"
	want, err := buildMailboxListNotification([]dnfrepo.MailRecord{controlMail}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("normalized avatar body differs: got=%x want=%x", got, want)
	}
}

func TestMailboxTypeNormalizationFailsClosedOnResolverError(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	mail := invMail("10", func(m *dnfrepo.MailRecord) {
		m.CreatedAt = now
		m.Attachments = []dnfrepo.MailAttachment{{ItemID: 25, Count: 1}}
	})
	_, err := buildMailboxListNotificationsWithTypeResolver(
		[]dnfrepo.MailRecord{mail}, 0, now,
		func(uint32) (alignedcmd.MailboxItemResolution, error) {
			return alignedcmd.MailboxItemResolution{}, errors.New("catalog unavailable")
		},
	)
	if err == nil {
		t.Fatal("unresolved type was emitted instead of failing closed")
	}
}

func TestMailboxTypeNormalizationPreservesNilResolverCompatibility(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	mail := invMail("11", func(m *dnfrepo.MailRecord) {
		m.CreatedAt = now
		m.Attachments = []dnfrepo.MailAttachment{invCreatureAttachment(25)}
	})
	got, err := buildMailboxListNotificationWithTypeResolver(
		[]dnfrepo.MailRecord{mail}, 0, now, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want, err := buildMailboxListNotification([]dnfrepo.MailRecord{mail}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("nil-resolver compatibility changed: got=%x want=%x", got, want)
	}
}
