package inventory

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type deleteFailureAckEntry struct {
	opType uint64
	slot   uint64
	itemID uint64
	count  uint64
}

func deleteFailureAckRequest(listType byte, entries ...deleteFailureAckEntry) []byte {
	payload := protowire.AppendTag(nil, 2, protowire.VarintType)
	payload = protowire.AppendVarint(payload, uint64(listType))
	for _, entry := range entries {
		nested := protowire.AppendTag(nil, 1, protowire.VarintType)
		nested = protowire.AppendVarint(nested, entry.opType)
		nested = protowire.AppendTag(nested, 2, protowire.VarintType)
		nested = protowire.AppendVarint(nested, entry.slot)
		nested = protowire.AppendTag(nested, 3, protowire.VarintType)
		nested = protowire.AppendVarint(nested, entry.itemID)
		nested = protowire.AppendTag(nested, 4, protowire.VarintType)
		nested = protowire.AppendVarint(nested, entry.count)
		payload = protowire.AppendTag(payload, 3, protowire.BytesType)
		payload = protowire.AppendBytes(payload, nested)
	}
	payload = protowire.AppendTag(payload, 4, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 0)

	body := make([]byte, 4, 4+len(payload))
	binary.LittleEndian.PutUint32(body, uint32(len(payload)))
	return append(body, payload...)
}

func handleDeleteFailureAckRequest(t *testing.T, ctx context.Context, repos dnfrepo.Group, body []byte) alignedcmd.Result {
	t.Helper()
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketDeleteItem),
		Body:                body,
		AccountID:           "dnf:1",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	return got
}

func requireDeleteFailureAck(t *testing.T, got alignedcmd.Result, wantList byte, wantReason string) {
	t.Helper()
	if !got.Handled || !got.ResponseAllowed || got.Operation != "delete_item" {
		t.Fatalf("result = %+v, want a handled and allowed delete rejection", got)
	}
	if !strings.Contains(got.Reason, wantReason) {
		t.Fatalf("reason = %q, want %q", got.Reason, wantReason)
	}
	if len(got.UpperResponses) != 1 {
		t.Fatalf("response count = %d, want one class1 failure ACK", len(got.UpperResponses))
	}

	ack := got.UpperResponses[0]
	if ack.MsgID != uint16(dnfenum.CmdPacketDeleteItem) || ack.Classification != dnfproto.DefaultChannelClassification || !ack.AllowCodec {
		t.Fatalf("ack = %+v", ack)
	}
	wantBody := []byte{0, 4, 0, 0, 0, 0x08, wantList, 0x18, 0x01}
	if !bytes.Equal(ack.Body, wantBody) {
		t.Fatalf("ack body = % X, want % X", ack.Body, wantBody)
	}
	if ack.Body[0] != 0 {
		t.Fatalf("failure ACK success byte = %d, want 0", ack.Body[0])
	}
	if bytes.Contains(ack.Body[5:], []byte{0x12}) {
		t.Fatalf("failure ACK contains a field-2 deletion row: % X", ack.Body)
	}
}

func requireInventoryUnchanged(t *testing.T, ctx context.Context, repos dnfrepo.Group, before dnfrepo.InventoryRecord) {
	t.Helper()
	after := loadTestInventory(t, ctx, repos, before.CharacterID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("inventory changed: before=%+v after=%+v", before, after)
	}
}

func TestHandlerDeleteFailureAckMissingSharedCrystalSlot(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 0): {ItemID: 4000, Count: 1},
		},
	})
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "dnf:1",
		Slots: map[string]dnfrepo.ItemStack{
			dnfrepo.AccountSharedInventorySlotKey(354): {ItemID: 3033, Count: 7},
		},
	}); err != nil {
		t.Fatalf("Save account inventory error = %v", err)
	}

	inventoryBefore := loadTestInventory(t, ctx, repos, "77")
	accountBefore, found, err := repos.AccountInventory.Load(ctx, "dnf:1")
	if err != nil || !found {
		t.Fatalf("Load account inventory found=%t err=%v", found, err)
	}
	got := handleDeleteFailureAckRequest(t, ctx, repos, deleteFailureAckRequest(listTypeMain, deleteFailureAckEntry{
		opType: 2,
		slot:   355,
		itemID: 3034,
		count:  1,
	}))
	requireDeleteFailureAck(t, got, listTypeMain, "inventory slot not found")

	requireInventoryUnchanged(t, ctx, repos, inventoryBefore)
	accountAfter, found, err := repos.AccountInventory.Load(ctx, "dnf:1")
	if err != nil || !found {
		t.Fatalf("Load account inventory after failure found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(accountAfter, accountBefore) {
		t.Fatalf("account inventory changed: before=%+v after=%+v", accountBefore, accountAfter)
	}
	if _, exists := accountAfter.Slots[dnfrepo.AccountSharedInventorySlotKey(355)]; exists {
		t.Fatalf("missing shared crystal slot 355 was created: %+v", accountAfter.Slots)
	}
}

func TestHandlerDeleteFailureAckLeavesInventoryUntouched(t *testing.T) {
	tests := []struct {
		name       string
		slots      map[string]dnfrepo.ItemStack
		entries    []deleteFailureAckEntry
		wantReason string
	}{
		{
			name: "item mismatch",
			slots: map[string]dnfrepo.ItemStack{
				slotKey(listTypeMain, 4): {ItemID: 100, Count: 5},
			},
			entries: []deleteFailureAckEntry{
				{opType: 2, slot: 4, itemID: 101, count: 1},
			},
			wantReason: "inventory item mismatch",
		},
		{
			name: "locked",
			slots: map[string]dnfrepo.ItemStack{
				slotKey(listTypeMain, 5): {ItemID: 100, Count: 5, Extra: map[string]string{"equipment_lock_state": "1"}},
			},
			entries: []deleteFailureAckEntry{
				{opType: 2, slot: 5, itemID: 100, count: 1},
			},
			wantReason: "inventory item is equipment locked",
		},
		{
			name: "batch second entry fails",
			slots: map[string]dnfrepo.ItemStack{
				slotKey(listTypeMain, 2): {ItemID: 100, Count: 5},
				slotKey(listTypeMain, 3): {ItemID: 200, Count: 7},
			},
			entries: []deleteFailureAckEntry{
				{opType: 2, slot: 2, itemID: 100, count: 2},
				{opType: 2, slot: 3, itemID: 999, count: 1},
			},
			wantReason: "inventory item mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots:       test.slots,
			})
			before := loadTestInventory(t, ctx, repos, "77")

			got := handleDeleteFailureAckRequest(t, ctx, repos, deleteFailureAckRequest(listTypeMain, test.entries...))
			requireDeleteFailureAck(t, got, listTypeMain, test.wantReason)
			requireInventoryUnchanged(t, ctx, repos, before)
		})
	}
}

func TestHandlerDeleteFailureAckAllowsAvatarListBeforeMissingSlot(t *testing.T) {
	// List 1 is the avatar container and is an allowed delete-request list;
	// the rejection below must come from the missing repository row, not the list gate.
	if err := checkDeleteListType(listTypeAvatar); err != nil {
		t.Fatalf("list 1 should be allowed by the delete list gate: %v", err)
	}

	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 0): {ItemID: 4000, Count: 1},
		},
	})
	before := loadTestInventory(t, ctx, repos, "77")

	got := handleDeleteFailureAckRequest(t, ctx, repos, deleteFailureAckRequest(listTypeAvatar, deleteFailureAckEntry{
		opType: 2,
		slot:   8,
		itemID: 500,
		count:  1,
	}))
	requireDeleteFailureAck(t, got, listTypeAvatar, "inventory slot not found")
	requireInventoryUnchanged(t, ctx, repos, before)
}

func TestHandlerDeleteSuccessStillReturnsSuccessAndDeductsOnce(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 6): {ItemID: 100, Count: 5},
			slotKey(listTypeMain, 7): {ItemID: 200, Count: 2},
		},
	})

	got := handleDeleteFailureAckRequest(t, ctx, repos, deleteFailureAckRequest(listTypeMain, deleteFailureAckEntry{
		opType: 2,
		slot:   6,
		itemID: 100,
		count:  1,
	}))
	if !got.Handled || !got.ResponseAllowed || got.Operation != "delete_item" || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v, want one successful delete ACK", got)
	}
	ack := got.UpperResponses[0]
	if ack.MsgID != uint16(dnfenum.CmdPacketDeleteItem) || ack.Classification != dnfproto.DefaultChannelClassification || !ack.AllowCodec {
		t.Fatalf("ack = %+v", ack)
	}
	wantAck := []byte{
		1, 12, 0, 0, 0,
		0x08, listTypeMain,
		0x12, 6, 0x08, 6, 0x10, 1, 0x18, 0,
		0x18, 0,
	}
	if !bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("success ACK body = % X, want % X", ack.Body, wantAck)
	}

	after := loadTestInventory(t, ctx, repos, "77")
	if stack := after.Slots[slotKey(listTypeMain, 6)]; stack.ItemID != 100 || stack.Count != 4 {
		t.Fatalf("deleted stack = %+v, want item 100 count 4 after exactly one deduction", stack)
	}
	if stack := after.Slots[slotKey(listTypeMain, 7)]; stack.ItemID != 200 || stack.Count != 2 {
		t.Fatalf("unrelated stack changed: %+v", stack)
	}
}

func TestHandlerDeleteDisallowedListKeepsRequestRejected(t *testing.T) {
	tests := []struct {
		name       string
		listType   byte
		wantReason string
	}{
		{name: "equipment owner required", listType: listTypeEquipment, wantReason: "equipment delete requires a typed equipment owner"},
		{name: "unsupported list", listType: listTypeAccountCargo, wantReason: "inventory list type is not supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots: map[string]dnfrepo.ItemStack{
					slotKey(listTypeMain, 0): {ItemID: 4000, Count: 1},
				},
			})
			before := loadTestInventory(t, ctx, repos, "77")

			got := handleDeleteFailureAckRequest(t, ctx, repos, deleteFailureAckRequest(test.listType, deleteFailureAckEntry{
				opType: 2,
				slot:   11,
				itemID: 700,
				count:  1,
			}))
			if !got.Handled || got.ResponseAllowed || got.Operation != "delete_item" || len(got.UpperResponses) != 0 {
				t.Fatalf("result = %+v, want a handled rejection without an ACK", got)
			}
			if !strings.Contains(got.Reason, test.wantReason) {
				t.Fatalf("reason = %q, want %q", got.Reason, test.wantReason)
			}
			requireInventoryUnchanged(t, ctx, repos, before)
		})
	}
}

func TestHandlerDeleteTruncatedRequestKeepsRequestRejected(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 9): {ItemID: 100, Count: 5},
		},
	})
	before := loadTestInventory(t, ctx, repos, "77")
	body := deleteFailureAckRequest(listTypeMain, deleteFailureAckEntry{
		opType: 2,
		slot:   9,
		itemID: 100,
		count:  1,
	})
	body = body[:len(body)-1]

	got := handleDeleteFailureAckRequest(t, ctx, repos, body)
	if !got.Handled || got.ResponseAllowed || got.Operation != "delete_item" || len(got.UpperResponses) != 0 {
		t.Fatalf("result = %+v, want a handled parse rejection without an ACK", got)
	}
	if !strings.Contains(got.Reason, "protobuf body length") {
		t.Fatalf("reason = %q, want truncation rejection", got.Reason)
	}
	requireInventoryUnchanged(t, ctx, repos, before)
}
