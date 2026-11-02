package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func epicDisjointCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	base := npcDisjointQuantityCatalog(t)
	source := base.source.(bridgePVFSource)
	source["equipment/equipment.lst"] += "5002 `epic.equ`\n5003 `epic2.equ`\n5004 `unmapped.equ`\n"
	for _, path := range []string{"epic.equ", "epic2.equ", "unmapped.equ"} {
		source["equipment/"+path] = "[equipment type] `[coat]`\n[rarity] 4\n[minimum level] 90\n[value] 1000\n"
	}
	source["stackable/stackable.lst"] += "10158124 `epic_soul.stk`\n10300000 `piece.stk`\n10300001 `piece2.stk`\n"
	for _, path := range []string{"epic_soul.stk", "piece.stk", "piece2.stk"} {
		source["stackable/"+path] = "[stackable type] `[material]`\n[stack limit] 9999\n"
	}
	// Include a non-epic output in the catalog to check the rarity gate.
	source[currentEpicPieceInfoPath] = "[piece list] 5002 10300000 5003 10300001 5001 10300002\n[/piece list]\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func seedEpicDisjointBalance(t *testing.T, repos r.Group, amount uint32) {
	t.Helper()
	a, found, err := repos.Account.Load(context.Background(), "account-1")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	a.Metadata[r.AccountEpicPieceMetadataKey(10300000)] = r.FormatAccountEpicPieceBalance(amount)
	if err := repos.Account.Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

func epicDisjointWire(t *testing.T, s *Service, wire []byte) ([]byte, map[uint32]uint32, int) {
	t.Helper()
	var acks []byte
	balances := map[uint32]uint32{}
	updates := 0
	for len(wire) > 0 {
		packet, rest := splitGameServerUpperPacketWithHeader(t, wire, s.gameUpperHeaderSize())
		wire = rest
		if packet.Header.MsgID == 26 {
			acks = append(acks, packet.Body[0])
			if packet.Body[0] == 1 {
				for _, row := range parseCurrentDisjointRewardRows(t, packet.Body) {
					if row.ItemID >= 10300000 && row.ItemID <= 10300002 {
						t.Fatal("ledger piece put into bag ACK")
					}
				}
			}
		}
		if packet.Header.MsgID == currentEpicPieceBalanceSyncOpcode {
			if len(acks) == 0 || packet.Header.Classification != 0 || len(packet.Body) != 13 || packet.Body[0] != 1 || binary.LittleEndian.Uint32(packet.Body[1:5]) != 1 {
				t.Fatal("invalid piece update order/body")
			}
			balances[binary.LittleEndian.Uint32(packet.Body[5:9])] = binary.LittleEndian.Uint32(packet.Body[9:13])
			updates++
		}
	}
	return acks, balances, updates
}

func TestNPCDisjointEpicPiecesSingleAndCompactBatch(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			slots := map[string]r.ItemStack{"0:60": {ItemID: 5002, Count: 1}}
			body := currentDisjointItemTestBody(60, 0, -1, 317)
			want := map[uint32]uint32{10300000: 130}
			if batch {
				slots["0:61"] = r.ItemStack{ItemID: 5002, Count: 1}
				slots["0:62"] = r.ItemStack{ItemID: 5003, Count: 1}
				body = currentDisjointBatchBody([]int16{60, 61, 62}, -1, 317)
				want = map[uint32]uint32{10300000: 160, 10300001: 30}
			}
			repos := aikenDisjointTestRepositories(t, slots)
			seedEpicDisjointBalance(t, repos, 100)
			s := currentDisjointTestService(repos, epicDisjointCatalog(t))
			s.options.epicPiecePush = epicPiecePushAlways
			conn := &bufferConn{}
			p := &gameSession{conn: conn, selectedCharacterID: 19}
			if err := s.handleCurrentDisjointItem(p, body); err != nil {
				t.Fatal(err)
			}
			acks, shown, updates := epicDisjointWire(t, s, conn.write.Bytes())
			if !reflect.DeepEqual(acks, []byte{1}) || !reflect.DeepEqual(shown, want) || updates != len(want) {
				t.Fatal(acks, shown, updates)
			}
			a, _, _ := repos.Account.Load(context.Background(), "account-1")
			if !reflect.DeepEqual(r.AccountEpicPieceBalances(a), want) || a.Metadata["account_cera"] != "22" {
				t.Fatal(a.Metadata)
			}
			bag, _, _ := repos.Inventory.Load(context.Background(), "19")
			for _, stack := range bag.Slots {
				if stack.ItemID == 5002 || stack.ItemID == 5003 || stack.ItemID == 10300000 || stack.ItemID == 10300001 {
					t.Fatal("source/piece left in bag", stack)
				}
			}
			conn.write.Reset()
			if err := s.handleCurrentDisjointItem(p, body); err != nil {
				t.Fatal(err)
			}
			acks, _, updates = epicDisjointWire(t, s, conn.write.Bytes())
			after, _, _ := repos.Account.Load(context.Background(), "account-1")
			if !reflect.DeepEqual(acks, []byte{0}) || updates != 0 || !reflect.DeepEqual(a, after) {
				t.Fatal("replayed piece credit")
			}
		})
	}
}

func TestNPCDisjointEpicPiecesNativeBatchRollbackAndDuplicate(t *testing.T) {
	for _, failure := range []string{"", "账号账本", "背包", "共享仓库", "提交"} {
		t.Run(failure, func(t *testing.T) {
			s, p, repos, trace, packets := nativeNPCBatchFixture(t, 3)
			s.pvfItemCatalog = epicDisjointCatalog(t)
			s.options.epicPiecePush = epicPiecePushAlways
			ctx := context.Background()
			bag, _, _ := repos.Inventory.Load(ctx, "19")
			for i, id := range []int64{5002, 5002, 5003} {
				bag.Slots[fmt.Sprintf("0:%d", 10+i)] = r.ItemStack{ItemID: id, Count: 1}
			}
			if err := repos.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			seedEpicDisjointBalance(t, repos, 100)
			before, _, _ := repos.Account.Load(ctx, "account-1")
			warehouse, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			trace.failure = failure
			packets = append(packets, packets[0])
			if handled, err := s.handleNativeNPCDisjointBatch(p, packets); !handled || err != nil {
				t.Fatal(handled, err)
			}
			if err := s.finishNativeResultDelivery(p, "epic regression"); err != nil {
				t.Fatal(err)
			}
			conn := p.conn.(*channelSceneWriteConn)
			acks, shown, updates := epicDisjointWire(t, s, conn.write.Bytes())
			after, _, _ := repos.Account.Load(ctx, "account-1")
			afterBag, _, _ := repos.Inventory.Load(ctx, "19")
			afterWarehouse, _, _ := repos.AccountInventory.Load(ctx, "account-1")
			if failure != "" {
				if !reflect.DeepEqual(acks, []byte{0, 0, 0, 0}) || updates != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bag, afterBag) || !reflect.DeepEqual(warehouse, afterWarehouse) {
					t.Fatal("failed batch leaked assets or success", acks, shown)
				}
			} else {
				want := map[uint32]uint32{10300000: 160, 10300001: 30}
				if !reflect.DeepEqual(acks, []byte{1, 1, 1, 0}) || !reflect.DeepEqual(shown, want) || !reflect.DeepEqual(r.AccountEpicPieceBalances(after), want) {
					t.Fatal(acks, shown, after.Metadata)
				}
			}
			if trace.transactions != 1 || len(conn.writes) != 1 {
				t.Fatal("batch fragmented", trace.transactions, len(conn.writes))
			}
		})
	}
}

func TestNPCDisjointEpicPiecesMappingAndCap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      int64
		table   string
		balance uint32
		success bool
		want    uint32
	}{
		{"cap", 5002, "", r.AccountEpicPieceBalanceMax - 10, true, r.AccountEpicPieceBalanceMax},
		{"unmapped-epic", 5004, "", 100, true, 100},
		{"mapped-non-epic", 5001, "", 100, true, 100},
		{"missing-table", 5002, "missing", 100, false, 100},
		{"malformed-table", 5002, "[piece list] 5002\n[/piece list]", 100, false, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := epicDisjointCatalog(t)
			if tc.table == "missing" {
				delete(catalog.source.(bridgePVFSource), currentEpicPieceInfoPath)
			} else if tc.table != "" {
				catalog.source.(bridgePVFSource)[currentEpicPieceInfoPath] = tc.table
			}
			repos := aikenDisjointTestRepositories(t, map[string]r.ItemStack{"0:60": {ItemID: tc.id, Count: 1}})
			seedEpicDisjointBalance(t, repos, tc.balance)
			s := currentDisjointTestService(repos, catalog)
			conn := &bufferConn{}
			if err := s.handleCurrentDisjointItem(&gameSession{conn: conn, selectedCharacterID: 19}, currentDisjointItemTestBody(60, 0, -1, 317)); err != nil {
				t.Fatal(err)
			}
			acks, _, _ := epicDisjointWire(t, s, conn.write.Bytes())
			a, _, _ := repos.Account.Load(context.Background(), "account-1")
			bag, _, _ := repos.Inventory.Load(context.Background(), "19")
			if (acks[0] == 1) != tc.success || r.ParseAccountEpicPieceBalance(a.Metadata[r.AccountEpicPieceMetadataKey(10300000)]) != tc.want || len(r.AccountEpicPieceBalances(a)) != 1 {
				t.Fatal(acks, a.Metadata)
			}
			if !tc.success && bag.Slots["0:60"].ItemID != tc.id {
				t.Fatal("bad mapping consumed equipment")
			}
		})
	}
}

type epicDisjointFailedWriteConn struct{ bufferConn }

func (*epicDisjointFailedWriteConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestNPCDisjointEpicPiecesCommittedWriteFailureDoesNotRecredit(t *testing.T) {
	repos := aikenDisjointTestRepositories(t, map[string]r.ItemStack{"0:60": {ItemID: 5002, Count: 1}})
	s := currentDisjointTestService(repos, epicDisjointCatalog(t))
	s.options.epicPiecePush = epicPiecePushAlways
	p := &gameSession{conn: &epicDisjointFailedWriteConn{}, selectedCharacterID: 19}
	body := currentDisjointItemTestBody(60, 0, -1, 317)
	if err := s.handleCurrentDisjointItem(p, body); err == nil {
		t.Fatal("write error hidden")
	}
	a, _, _ := repos.Account.Load(context.Background(), "account-1")
	if r.ParseAccountEpicPieceBalance(a.Metadata[r.AccountEpicPieceMetadataKey(10300000)]) != 30 {
		t.Fatal("committed credit lost")
	}
	p.conn = &bufferConn{}
	if err := s.handleCurrentDisjointItem(p, body); err != nil {
		t.Fatal(err)
	}
	after, _, _ := repos.Account.Load(context.Background(), "account-1")
	if !reflect.DeepEqual(a, after) {
		t.Fatal("retry credited twice")
	}
}
