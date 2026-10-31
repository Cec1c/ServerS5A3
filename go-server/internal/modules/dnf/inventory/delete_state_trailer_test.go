package inventory

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Reuse the existing valid material-delete payload with a selected synthetic
// state trailer: one list-0 entry for shared slot 358 (item 3037, count 1).
func currentDeleteStateFixtureBody(state byte) []byte {
	body := []byte{
		0x10, 0x00, 0x00, 0x00,
		0x10, 0x00,
		0x1A, 0x0A,
		0x08, 0x02,
		0x10, 0xE6, 0x02,
		0x18, 0xDD, 0x17,
		0x20, 0x01,
		0x20, 0x00,
		0x02,
	}
	body[len(body)-1] = state
	return body
}

func TestDecodeCurrentDeleteRequestAcceptsState2Trailer(t *testing.T) {
	got, err := DecodeDeleteRequest(currentDeleteStateFixtureBody(2))
	if err != nil {
		t.Fatalf("DecodeDeleteRequest error = %v", err)
	}
	if !got.Extended || got.ListType != listTypeMain || len(got.Entries) != 1 {
		t.Fatalf("decoded delete request = %+v", got)
	}
	entry := got.Entries[0]
	if entry.OpType != 2 || entry.SlotIndex != 358 || entry.ItemID != 3037 || entry.DeleteCount != 1 {
		t.Fatalf("decoded state-2 material entry = %+v", entry)
	}
}

func TestDecodeCurrentDeleteRequestRejectsUnsupportedStateTrailerEnvelopes(t *testing.T) {
	tests := []struct {
		name string
		body func() []byte
	}{
		{
			name: "unsupported state 3",
			body: func() []byte { return currentDeleteStateFixtureBody(3) },
		},
		{
			name: "unsupported state 255",
			body: func() []byte { return currentDeleteStateFixtureBody(255) },
		},
		{
			name: "wrong declared length",
			body: func() []byte {
				body := currentDeleteStateFixtureBody(2)
				binary.LittleEndian.PutUint32(body, 15)
				return body
			},
		},
		{
			name: "multiple trailers",
			body: func() []byte {
				return append(currentDeleteStateFixtureBody(2), 2)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeDeleteRequest(test.body()); err == nil {
				t.Fatal("malformed state-trailer body unexpectedly accepted")
			}
		})
	}
}

func TestDeleteStateTrailerCompatibilityStaysOp18Only(t *testing.T) {
	// A valid sort protobuf with the op18 state-2 tail must retain the generic
	// protobuf envelope rejection used by other commands.
	body := []byte{0x06, 0, 0, 0, 0x10, 0, 0x18, 2, 0x20, 0, 2}
	if _, err := DecodeSortItemRequest(body); err == nil {
		t.Fatal("non-zero trailing state unexpectedly accepted for op13")
	}
}

func TestHandlerDeleteState2ConsumesActualMaterialAndReturnsAck(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 121): {ItemID: 3021, Count: 1},
		},
	})
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "dnf:1",
		Slots: map[string]dnfrepo.ItemStack{
			dnfrepo.AccountSharedInventorySlotKey(358): {ItemID: 3037, Count: 602},
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketDeleteItem),
		Body:                currentDeleteStateFixtureBody(2),
		AccountID:           "dnf:1",
		SelectedCharacterID: 77,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || got.Operation != "delete_item" || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(got.Reason, "inventory owner applied") || !strings.Contains(got.Reason, "0x0012") {
		t.Fatalf("reason = %q", got.Reason)
	}
	ack := got.UpperResponses[0]
	if ack.MsgID != uint16(dnfenum.CmdPacketDeleteItem) || ack.Classification != dnfproto.DefaultChannelClassification || !ack.AllowCodec {
		t.Fatalf("ack = %+v", ack)
	}
	wantAck := []byte{
		1, 13, 0, 0, 0,
		8, 0,
		0x12, 7,
		8, 0xE6, 2,
		0x10, 1,
		0x18, 0,
		0x18, 0,
	}
	if string(ack.Body) != string(wantAck) {
		t.Fatalf("ack body = % X, want % X", ack.Body, wantAck)
	}

	account, found, err := repos.AccountInventory.Load(ctx, "dnf:1")
	if err != nil || !found {
		t.Fatalf("account inventory found=%t err=%v", found, err)
	}
	if stack := account.Slots[dnfrepo.AccountSharedInventorySlotKey(358)]; stack.ItemID != 3037 || stack.Count != 601 {
		t.Fatalf("account material slot = %+v, want 3037x601", stack)
	}
	character := loadTestInventory(t, ctx, repos, "77")
	if _, exists := character.Slots[dnfrepo.AccountSharedInventorySlotKey(358)]; exists {
		t.Fatalf("account material was written into character inventory: %+v", character.Slots)
	}
	if stack := character.Slots[slotKey(listTypeMain, 121)]; stack.ItemID != 3021 || stack.Count != 1 {
		t.Fatalf("ordinary character material changed: %+v", stack)
	}
}

func TestHandlerDeleteState2RejectsWrongMaterialOrCountWithoutMutation(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func([]byte)
		wantReason string
	}{
		{
			name: "wrong item id",
			mutate: func(body []byte) {
				body[14] = 0xDE // item 3037 -> 3038 while keeping a valid protobuf body.
			},
			wantReason: "inventory item mismatch",
		},
		{
			name: "zero delete count",
			mutate: func(body []byte) {
				body[17] = 0
			},
			wantReason: "delete entry count 0 is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots: map[string]dnfrepo.ItemStack{
					slotKey(listTypeMain, 121): {ItemID: 3021, Count: 1},
				},
			})
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
				AccountID: "dnf:1",
				Slots: map[string]dnfrepo.ItemStack{
					dnfrepo.AccountSharedInventorySlotKey(358): {ItemID: 3037, Count: 602},
				},
			}); err != nil {
				t.Fatal(err)
			}

			body := currentDeleteStateFixtureBody(2)
			test.mutate(body)
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
			if test.name == "wrong item id" {
				requireDeleteFailureAck(t, got, listTypeMain, test.wantReason)
			} else if !got.Handled || got.ResponseAllowed || got.Operation != "delete_item" || len(got.UpperResponses) != 0 {
				t.Fatalf("result = %+v", got)
			}
			if !strings.Contains(got.Reason, test.wantReason) {
				t.Fatalf("reason = %q, want %q", got.Reason, test.wantReason)
			}

			account, found, err := repos.AccountInventory.Load(ctx, "dnf:1")
			if err != nil || !found {
				t.Fatalf("account inventory found=%t err=%v", found, err)
			}
			if stack := account.Slots[dnfrepo.AccountSharedInventorySlotKey(358)]; stack.ItemID != 3037 || stack.Count != 602 {
				t.Fatalf("account material slot = %+v, want unchanged 3037x602", stack)
			}
			character := loadTestInventory(t, ctx, repos, "77")
			if stack := character.Slots[slotKey(listTypeMain, 121)]; stack.ItemID != 3021 || stack.Count != 1 {
				t.Fatalf("ordinary character material changed: %+v", stack)
			}
		})
	}
}
