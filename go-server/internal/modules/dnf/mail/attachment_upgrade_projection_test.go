package mail

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMailboxAttachmentUpgradeProjectionSendOpenClaim(t *testing.T) {
	const itemID = 910001

	testCases := make([]struct {
		name       string
		raw        []byte
		extra      map[string]string
		wantPacked byte
		wantType   byte
		wantMarker uint16
	}, 0, 7)
	for _, amplifyType := range []byte{1, 2, 3, 4} {
		testCases = append(testCases, struct {
			name       string
			raw        []byte
			extra      map[string]string
			wantPacked byte
			wantType   byte
			wantMarker uint16
		}{
			name: "extra_only_amplify_" + strconv.Itoa(int(amplifyType)),
			extra: map[string]string{
				"ext_data0":        "10",
				"packed_flag_byte": "10",
				"byte_13":          strconv.Itoa(int(amplifyType)),
				"marker_16":        "123",
			},
			wantPacked: 10,
			wantType:   amplifyType,
			wantMarker: 123,
		})
	}
	testCases = append(testCases,
		struct {
			name       string
			raw        []byte
			extra      map[string]string
			wantPacked byte
			wantType   byte
			wantMarker uint16
		}{
			name: "extra_values_override_raw",
			raw:  attachmentUpgradeProjectionRaw(itemID, 0xE4, 1, 0x0456),
			extra: map[string]string{
				"ext_data0":        "106",
				"packed_flag_byte": "106",
				"byte_13":          "3",
				"marker_16":        "123",
			},
			wantPacked: 0x6A,
			wantType:   3,
			wantMarker: 123,
		},
		struct {
			name       string
			raw        []byte
			extra      map[string]string
			wantPacked byte
			wantType   byte
			wantMarker uint16
		}{
			name: "explicit_zero_overrides_raw",
			raw:  attachmentUpgradeProjectionRaw(itemID, 0xE4, 4, 0x0456),
			extra: map[string]string{
				"ext_data0":        "0",
				"packed_flag_byte": "0",
				"byte_13":          "0",
				"marker_16":        "0",
			},
			wantPacked: 0,
			wantType:   0,
			wantMarker: 0,
		},
		struct {
			name       string
			raw        []byte
			extra      map[string]string
			wantPacked byte
			wantType   byte
			wantMarker uint16
		}{
			name:       "missing_projection_extra_keeps_raw",
			raw:        attachmentUpgradeProjectionRaw(itemID, 0xE5, 4, 0x0456),
			wantPacked: 0xE5,
			wantType:   4,
			wantMarker: 0x0456,
		},
	)

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			attachmentUpgradeProjectionRoundTrip(t, uint32(itemID), tc.raw, tc.extra, tc.wantPacked, tc.wantType, tc.wantMarker)
		})
	}
}

func attachmentUpgradeProjectionRoundTrip(
	t *testing.T,
	itemID uint32,
	sourceRaw []byte,
	projectionExtra map[string]string,
	wantPacked byte,
	wantType byte,
	wantMarker uint16,
) {
	t.Helper()
	if wantType > 4 {
		t.Fatalf("invalid amplification type %d", wantType)
	}

	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "1771",
		AccountID:   "upgrade-projection-fixture",
		Name:        "projection-sender",
		Stats:       map[string]int64{"gold": 2000},
	})
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "1772",
		AccountID:   "upgrade-projection-fixture",
		Name:        "projection-receiver",
		Slot:        1,
	})

	sourceRaw = append([]byte(nil), sourceRaw...)
	sourceExtra := map[string]string{
		"item_kind":      "equipment",
		"quality_seed":   "280027066",
		"durability":     "28",
		"max_durability": "57",
	}
	for key, value := range projectionExtra {
		sourceExtra[key] = value
	}
	originalRaw := append([]byte(nil), sourceRaw...)
	originalExtra := cloneStringMap(sourceExtra)
	sourceStack := dnfrepo.ItemStack{
		ItemID:   int64(itemID),
		Count:    1,
		RawEntry: sourceRaw,
		Extra:    sourceExtra,
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "1771",
		Slots:       map[string]dnfrepo.ItemStack{"0:9": sourceStack},
	}); err != nil {
		t.Fatalf("save source equipment: %v", err)
	}

	resolver := func(resolvedItemID uint32) (alignedcmd.MailboxItemResolution, error) {
		if resolvedItemID != itemID {
			return alignedcmd.MailboxItemResolution{}, fmt.Errorf("unexpected item %d", resolvedItemID)
		}
		return alignedcmd.MailboxItemResolution{
			Kind:          "equipment",
			Price:         100,
			SlotStart:     9,
			SlotEnd:       64,
			StackLimit:    1,
			EquipmentType: "[weapon]",
		}, nil
	}

	send, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMultiMailboxSend),
		SelectedCharacterID: 1771,
		Repositories:        repos,
		MailboxItemResolver: resolver,
		Body: currentSendBody(
			"projection-receiver",
			0,
			[]SendAttachment{{ListType: 0, SlotIndex: 9, ItemID: itemID, Count: 1}},
			"upgrade projection fixture",
			0,
			false,
			true,
		),
	})
	if err != nil {
		t.Fatalf("send equipment: %v", err)
	}
	assertOneMailboxResponse(t, send, dnfenum.CmdPacketMultiMailboxSend, []byte{1})
	if !bytes.Equal(sourceStack.RawEntry, originalRaw) || !reflect.DeepEqual(sourceStack.Extra, originalExtra) {
		t.Fatal("send mutated the source equipment input")
	}

	box, found, err := repos.Mailbox.Load(ctx, "1772")
	if err != nil || !found {
		t.Fatalf("load recipient mailbox found=%t err=%v", found, err)
	}
	stored := box.Mails["1"].Attachments
	if len(stored) != 1 {
		t.Fatalf("stored attachment count=%d, want 1", len(stored))
	}
	storedRaw := append([]byte(nil), stored[0].RawEntry...)
	storedExtra := cloneStringMap(stored[0].Extra)
	if !bytes.Equal(storedRaw, originalRaw) {
		t.Fatal("send did not preserve the raw attachment input")
	}
	for key, want := range originalExtra {
		if storedExtra[key] != want {
			t.Fatalf("send changed attachment Extra[%s]=%q, want %q", key, storedExtra[key], want)
		}
	}

	opened, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxOpen),
		SelectedCharacterID: 1772,
		Repositories:        repos,
	})
	if err != nil || len(opened.UpperResponses) != 2 {
		t.Fatalf("open mailbox result=%+v err=%v", opened, err)
	}
	afterOpen, found, err := repos.Mailbox.Load(ctx, "1772")
	if err != nil || !found {
		t.Fatalf("reload opened mailbox found=%t err=%v", found, err)
	}
	afterOpenAttachment := afterOpen.Mails["1"].Attachments[0]
	if !bytes.Equal(afterOpenAttachment.RawEntry, storedRaw) || !reflect.DeepEqual(afterOpenAttachment.Extra, storedExtra) {
		t.Fatal("mail list projection mutated the attachment input")
	}

	claim, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 1772,
		Repositories:        repos,
		MailboxItemResolver: resolver,
		Body:                currentMailIDList(1),
	})
	if err != nil {
		t.Fatalf("claim equipment: %v", err)
	}
	assertOneMailboxResponse(t, claim, dnfenum.CmdPacketMailboxExtractItem, currentExtractAck(1))

	row := attachmentUpgradeProjectionOpenRow(t, opened.UpperResponses[1].Body)
	if got := row[0x0A]; got != wantPacked {
		t.Errorf("0x77 offset10=%#x, want %#x (reseal=%d level=%d)", got, wantPacked, wantPacked>>5, wantPacked&0x1F)
	}
	if got := row[0x13]; got != wantType {
		t.Errorf("0x77 offset19 amplification type=%d, want %d", got, wantType)
	}
	if got := binary.LittleEndian.Uint16(row[0x14:0x16]); got != wantMarker {
		t.Errorf("0x77 offset20:22 value=%d, want %d", got, wantMarker)
	}
	if len(originalRaw) == mailboxItemRawSize {
		if !bytes.Equal(row[0x0E:0x13], originalRaw[0x0E:0x13]) || !bytes.Equal(row[0x16:], originalRaw[0x16:]) {
			t.Error("upgrade projection changed unrelated 0x77 instance bytes")
		}
	}

	inv, found, err := repos.Inventory.Load(ctx, "1772")
	if err != nil || !found {
		t.Fatalf("load claimed inventory found=%t err=%v", found, err)
	}
	var claimed dnfrepo.ItemStack
	claimedCount := 0
	for _, stack := range inv.Slots {
		if stack.ItemID == int64(itemID) {
			claimed = stack
			claimedCount++
		}
	}
	if claimedCount != 1 {
		t.Fatalf("claimed equipment count=%d, want 1", claimedCount)
	}
	if claimed.Count != 1 || !bytes.Equal(claimed.RawEntry, originalRaw) {
		t.Fatalf("claim changed raw equipment input: %+v", claimed)
	}
	for _, key := range []string{"ext_data0", "packed_flag_byte", "byte_13", "marker_16"} {
		if want, present := projectionExtra[key]; present && claimed.Extra[key] != want {
			t.Errorf("claimed Extra[%s]=%q, want %q", key, claimed.Extra[key], want)
		}
	}
	if !bytes.Equal(sourceStack.RawEntry, originalRaw) || !reflect.DeepEqual(sourceStack.Extra, originalExtra) {
		t.Fatal("end-to-end projection mutated the original equipment input")
	}
}

func attachmentUpgradeProjectionRaw(itemID uint32, packed byte, amplifyType byte, marker uint16) []byte {
	raw := make([]byte, mailboxItemRawSize)
	binary.LittleEndian.PutUint16(raw[0:2], 9)
	binary.LittleEndian.PutUint32(raw[2:6], itemID)
	binary.LittleEndian.PutUint32(raw[6:10], 280027066)
	raw[0x0A] = packed
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], 28)
	raw[0x0D] = 1
	raw[0x13] = amplifyType
	binary.LittleEndian.PutUint16(raw[0x14:0x16], marker)
	for i := 0x16; i < len(raw); i++ {
		raw[i] = byte(i*37 + 11)
	}
	return raw
}

func attachmentUpgradeProjectionOpenRow(t *testing.T, body []byte) []byte {
	t.Helper()
	walk := invWalkMailboxList(t, body, []bool{false})
	if walk.summaryCount != 1 || walk.totalMails != 1 || walk.detailCount != 1 {
		t.Fatalf("mailbox list shape summary=%d total=%d detail=%d", walk.summaryCount, walk.totalMails, walk.detailCount)
	}
	c := invCursor{data: body, off: 2}
	c.need(t, 8, "claim and sender IDs")
	c.dstring(t, "sender name")
	c.need(t, 8, "gold and item ID")
	c.u8(t, "creation seal")
	c.need(t, 6, "amount and durability")
	c.u8(t, "summary seal")
	c.need(t, 8, "summary instance fields")
	return append([]byte(nil), c.need(t, mailboxItemRawSize, "0x77 attachment row")...)
}
