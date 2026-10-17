package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Native 919: taking one item out of account cargo clears displayed gold and
// changes an unrelated coat's quality to Count=1. Reselecting the character
// restores both. Exercise the real owner/handler and bridge wire together.
func TestCargoAndSortRefreshPreserveWalletQualityPair919(t *testing.T) {
	for _, tc := range []struct {
		name             string
		src, dst         byte
		srcSlot, dstSlot int16
		sort             bool
	}{
		{name: "account_stack", src: 0, dst: 12, srcSlot: 121, dstSlot: 0},
		{name: "account_auto_stack", src: 0, dst: 12, srcSlot: 121, dstSlot: 1},
		{name: "account_withdraw_split", src: 12, dst: 0, srcSlot: 0, dstSlot: 122},
		{name: "personal_auto_stack", src: 0, dst: 2, srcSlot: 121, dstSlot: 1},
		{name: "main_sort", sort: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newRefreshCostFixture(t)
			f.warmUp(t)
			pair914SeedRows(t, f)
			inv, _, err := f.group.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			coat := inv.Slots["0:14"]
			material := dnfrepo.ItemStack{ItemID: 3227, Count: 2, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/material/solvent_magicpower.stk", "stack_limit": "999"}}
			inv.Slots = map[string]dnfrepo.ItemStack{"0:14": coat, "0:121": material}
			if tc.src == 12 {
				delete(inv.Slots, "0:121")
			}
			inv.Warehouse = map[string]dnfrepo.ItemStack{"2:0": material}
			if err = f.group.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			acct, _, err := f.group.Account.Load(ctx, "dnf:1")
			if err != nil {
				t.Fatal(err)
			}
			if acct.Metadata == nil {
				acct.Metadata = map[string]string{}
			}
			acct.Metadata["account_cargo_created"] = "true"
			acct.Metadata["account_cargo_level"] = "8"
			if err = f.group.Account.Save(ctx, acct); err != nil {
				t.Fatal(err)
			}
			cargo, _, err := f.group.AccountInventory.Load(ctx, "dnf:1")
			if err != nil {
				t.Fatal(err)
			}
			cargo.Slots["12:0"] = material
			if err = f.group.AccountInventory.Save(ctx, cargo); err != nil {
				t.Fatal(err)
			}
			opcode := uint16(dnfenum.CmdPacketMoveItemspace)
			body := make([]byte, 28)
			body[0] = tc.src
			binary.LittleEndian.PutUint16(body[1:3], uint16(tc.srcSlot))
			binary.LittleEndian.PutUint32(body[7:11], 1)
			body[11] = tc.dst
			binary.LittleEndian.PutUint16(body[12:14], uint16(tc.dstSlot))
			binary.LittleEndian.PutUint32(body[22:26], ^uint32(0))
			if tc.sort {
				opcode = uint16(dnfenum.CmdPacketSortItem)
				body = []byte{0, 2}
			}
			result, err := dnfinventory.NewHandler().Handle(ctx, alignedcmd.Request{Opcode: opcode, Body: body, AccountID: "dnf:1", SelectedCharacterID: 19, Repositories: f.group})
			if err != nil || !result.ResponseAllowed || len(result.UpperResponses) < 2 {
				t.Fatalf("owner result=%+v err=%v", result, err)
			}
			f.buffer.write.Reset()
			if err = f.service.sendAlignedUpperResponses(f.session, result); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacketWithHeader(t, f.buffer.write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
			if ack.Header.MsgID != opcode || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, result.UpperResponses[0].Body) {
				t.Fatalf("ACK changed or reordered: %+v", ack)
			}
			refresh, rest := splitGameServerUpperPacketWithHeader(t, rest, dnfproto.GameServerUpperHeaderSize16)
			if refresh.Header.MsgID != 13 || refresh.Header.Classification != 0 || refresh.Body[0] != 0 {
				t.Fatalf("not main refresh: %+v", refresh)
			}
			for len(rest) > 0 {
				other, tail := splitGameServerUpperPacketWithHeader(t, rest, dnfproto.GameServerUpperHeaderSize16)
				if other.Header.MsgID != 13 || other.Header.Classification != 0 || len(other.Body) == 0 || (other.Body[0] != 2 && other.Body[0] != 12) {
					t.Fatalf("unexpected extra frame: %+v", other)
				}
				rest = tail
			}
			wallet, found := pair914Row(t, refresh.Body, 0)
			if !found || binary.LittleEndian.Uint32(wallet[6:10]) != 123456 {
				t.Errorf("wallet missing/wrong: found=%t row=%x", found, wallet)
			}
			crystal, found := pair914Row(t, refresh.Body, 359)
			if !found || binary.LittleEndian.Uint32(crystal[6:10]) != 5000 {
				t.Errorf("shared crystal missing/wrong: found=%t row=%x", found, crystal)
			}
			row, found := pair914Row(t, refresh.Body, 14)
			if !found {
				t.Fatal("unrelated coat disappeared")
			}
			if seed := binary.LittleEndian.Uint32(row[6:10]); seed != 999999998 {
				t.Errorf("quality seed=%d, want999999998", seed)
			}
			if row[10] != 6 || binary.LittleEndian.Uint32(row[14:18]) != 10007563 || !bytes.Equal(row[0x3c:0x45], []byte{2, 255, 255, 255, 255, 255, 255, 255, 255}) {
				t.Errorf("coat attributes changed: %x", row)
			}
			saved, _, err := f.group.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if saved.Slots["0:14"].Extra["quality_seed"] != "999999998" {
				t.Fatal("durable coat quality changed")
			}
		})
	}
}
