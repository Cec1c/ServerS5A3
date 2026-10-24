package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFEquipmentEmancipateOwnedConversion(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		book, source, output uint32
		typ                  int64
		piece                int64
	}{{10164871, 100050700, 100050762, 20, 10164869}, {10149529, 100050285, 100050653, 17, 10149518}, {10149523, 100080172, 100080461, 11, 10149512}} {
		t.Run(fmt.Sprint(tc.typ), func(t *testing.T) {
			_, bookDoc, err := items.currentEmancipateDocument(tc.book)
			if err != nil {
				t.Fatal(err)
			}
			_, sourceDoc, err := items.currentEmancipateDocument(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			rule, err := parseCurrentEquipmentEmancipateRule(bookDoc, sourceDoc)
			if err != nil || rule.OutputID != tc.output || rule.TicketType != tc.typ {
				t.Fatalf("rule=%+v err=%v", rule, err)
			}
			piece := tc.piece
			want := map[int64]int64{piece: 10, 10099773: 12, 10100116: 24, 10100115: 36}
			got := map[int64]int64{}
			for _, m := range rule.Materials {
				got[m.ItemID] = m.Count
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("real conversion materials=%v", got)
			}
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			raw := make([]byte, currentItemListEntryWireSize)
			binary.LittleEndian.PutUint32(raw[2:], tc.source)
			raw[0x0a], raw[0x13], raw[0x57] = 12, 131, 8
			binary.LittleEndian.PutUint32(raw[0x0e:], 123456)
			inv := dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{"0:30": {ItemID: int64(tc.source), Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment"}}, "0:69": {ItemID: int64(tc.book), Count: 1}, "0:121": {ItemID: piece, Count: 20}}}
			account := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{}}
			for _, m := range rule.Materials {
				if fixed, ok := dnfrepo.AccountWarehouseSlotForItem(m.ItemID); ok {
					account.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)] = dnfrepo.ItemStack{ItemID: m.ItemID, Count: m.Count * 2}
				}
			}
			for _, err := range []error{repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 12345}}), repos.Inventory.Save(ctx, inv), repos.AccountInventory.Save(ctx, account)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{options: options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: items, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			body := emancipationBody()
			binary.LittleEndian.PutUint32(body[2:], tc.book)
			binary.LittleEndian.PutUint32(body[8:], tc.source)
			conn := &bufferConn{}
			session := &gameSession{selectedCharacterID: 2, conn: conn}
			handled, err := currentGameplayModules.DispatchUpper(s, session, 271, 1, body)
			if !handled || err != nil {
				t.Fatalf("dispatch %t %v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 271 || len(ack.Body) != 38 || ack.Body[0] != 1 || ack.Body[1] != 1 || ack.Body[2] != 5 {
				t.Fatalf("real ack %x", ack.Body)
			}
			cleared, rest := splitGameServerUpperPacket(t, rest)
			updated, tail := splitGameServerUpperPacket(t, rest)
			if cleared.Header.MsgID != 14 || updated.Header.MsgID != 14 || len(tail) != 0 || binary.LittleEndian.Uint32(cleared.Body[5:]) != math.MaxUint32 {
				t.Fatal("real target replacement order")
			}
			after, _, _ := repos.Inventory.Load(ctx, "2")
			afterA, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			c, _, _ := repos.Character.Load(ctx, "2")
			out := after.Slots["0:30"]
			row := currentItemListEntryFromStack(0, 30, out)
			if out.ItemID != int64(tc.output) || !out.Bind || row.data[0x0a] != 0 || row.data[0x13] != 0 || row.data[0x57] != 0 || binary.LittleEndian.Uint32(row.data[0x0e:]) != 0 || row.data[0x0d] != 0 {
				t.Fatalf("real output state %+v row=%x", out, row.data)
			}
			if _, exists := after.Slots["0:69"]; exists {
				t.Fatal("book not consumed")
			}
			if after.Slots["0:121"].Count != 10 || c.Stats["gold"] != 12345 {
				t.Fatal("piece/gold mismatch")
			}
			for id, n := range want {
				if fixed, ok := dnfrepo.AccountWarehouseSlotForItem(id); ok && afterA.Slots[dnfrepo.AccountSharedInventorySlotKey(fixed)].Count != n {
					t.Fatalf("soul=%d wrong remaining", id)
				}
			}
			r, _ := parseCurrentEquipmentEmancipateRequest(body)
			if _, err := s.commitCurrentEquipmentEmancipate(ctx, session, r); err == nil {
				t.Fatal("real replay accepted")
			}
			t.Logf("real rule type=%d source=%d output=%d materials=%v; transaction and 271/14/14 verified, no native client", tc.typ, tc.source, tc.output, want)
		})
	}
}
