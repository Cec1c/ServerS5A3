package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/epicbook"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func epicBookRealFixtureRound9(t *testing.T, output uint32) (*Service, *gameSession, *bufferConn, repo.Group, epicbook.Recipe) {
	t.Helper()
	s, session, conn := newCurrentEpicProductionRealPVFService(t, epicProductionSessionAccount, epicProductionAccountScopeSession, epicProductionInfoPushAlways)
	s.options.currentClientVersion = "official-24.9"
	s.options.epicPiecePush = epicPiecePushAlways
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	c, err := epicbook.LoadCatalog(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := c.Lookup(output)
	if !ok {
		t.Fatal(output)
	}
	a := repo.AccountRecord{AccountID: session.accountID, Metadata: map[string]string{"unrelated": "keep"}}
	shared := repo.AccountInventoryRecord{AccountID: session.accountID, Slots: map[string]repo.ItemStack{}}
	for _, m := range r.Materials {
		if m.ItemID == r.PieceID {
			a.Metadata[repo.AccountEpicPieceMetadataKey(m.ItemID)] = "1000"
			continue
		}
		slot, ok := repo.AccountWarehouseSlotForItem(int64(m.ItemID))
		if !ok {
			t.Fatal(m)
		}
		shared.Slots[repo.AccountSharedInventorySlotKey(slot)] = repo.ItemStack{ItemID: int64(m.ItemID), Count: int64(m.Count) + 5}
	}
	for _, err := range []error{g.Account.Save(ctx, a), g.AccountInventory.Save(ctx, shared), g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "3", Slots: map[string]repo.ItemStack{}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s, session, conn, g, r
}

type epicBookAbortUOW struct {
	repo.AccountCharacterAssetUnitOfWork
}

func (u epicBookAbortUOW) WithinEpicBookAssets(ctx context.Context, a, c string, apply func(repo.EpicBookPieceDebitor, repo.CharacterRecord, repo.AccountInventoryRepository, repo.InventoryRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.(repo.EpicBookAssetUnitOfWork).WithinEpicBookAssets(ctx, a, c, func(d repo.EpicBookPieceDebitor, ch repo.CharacterRecord, s repo.AccountInventoryRepository, i repo.InventoryRepository) error {
		if e := apply(d, ch, s, i); e != nil {
			return e
		}
		return errors.New("late abort")
	})
}

func TestRealPVFEpicBookCraftActualHandlerRound9(t *testing.T) {
	for _, scenario := range []string{"low", "middle", "high", "exact_souls", "login_only", "leftover_pieces", "piece_short", "legendary_short", "epic_short", "locked", "overflow_stack", "foreign", "unknown", "piece_as_output", "short_body", "extra_body", "full_bag", "late_abort", "no_uow", "legacy", "push_off", "vendor_empty", "book_off", "wrong_class"} {
		t.Run(scenario, func(t *testing.T) {
			output := uint32(450004)
			if scenario == "middle" {
				output = 100050050
			}
			if scenario == "high" {
				output = 100050696
			}
			s, session, conn, g, r := epicBookRealFixtureRound9(t, output)
			ctx := context.Background()
			a, _, _ := g.Account.Load(ctx, session.accountID)
			shared, _, _ := g.AccountInventory.Load(ctx, session.accountID)
			inv, _, _ := g.Inventory.Load(ctx, "3")
			ch, _, _ := g.Character.Load(ctx, "3")
			body := make([]byte, 4)
			binary.LittleEndian.PutUint32(body, output)
			switch scenario {
			case "exact_souls":
				for key, stack := range shared.Slots {
					stack.Count -= 5
					shared.Slots[key] = stack
				}
			case "login_only":
				s.options.epicPiecePush = epicPiecePushLoginOnly
			case "leftover_pieces":
				a.Metadata[repo.AccountEpicPieceMetadataKey(r.PieceID)] = "2005"
				a.Metadata[repo.AccountEpicPieceMetadataKey(10300001)] = "17"
			case "piece_short":
				a.Metadata[repo.AccountEpicPieceMetadataKey(r.PieceID)] = "999"
			case "legendary_short", "epic_short", "locked", "overflow_stack":
				slot := int16(364)
				if scenario == "epic_short" {
					slot = 365
				}
				key := repo.AccountSharedInventorySlotKey(slot)
				stack := shared.Slots[key]
				if scenario == "locked" {
					stack.Extra = map[string]string{"equipment_lock_state": "locked"}
				} else if scenario == "overflow_stack" {
					stack.Count = 2147483648
				} else {
					stack.Count = 1
				}
				shared.Slots[key] = stack
			case "foreign":
				session.accountID = "other"
			case "unknown":
				binary.LittleEndian.PutUint32(body, 42)
			case "piece_as_output":
				binary.LittleEndian.PutUint32(body, r.PieceID)
			case "short_body":
				body = body[:3]
			case "extra_body":
				body = append(body, 0)
			case "full_bag":
				inv.Slots = map[string]repo.ItemStack{}
				for slot := int16(3); slot <= 64; slot++ {
					inv.Slots[currentCeraShopInventorySlotKey(0, slot)] = repo.ItemStack{ItemID: 450004, Count: 1}
				}
			case "late_abort":
				modified := g
				modified.AccountAssets = epicBookAbortUOW{g.AccountAssets}
				s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
			case "no_uow":
				modified := g
				modified.AccountAssets = nil
				s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
			case "legacy":
				s.options.currentClientVersion = "legacy"
			case "push_off":
				s.options.epicPiecePush = epicPiecePushOff
			case "vendor_empty":
				s.options.epicPiecePush = epicPiecePushVendorEmpty
			case "book_off":
				s.options.epicPieceBook = epicPieceBookOff
			}
			for _, e := range []error{g.Account.Save(ctx, a), g.AccountInventory.Save(ctx, shared), g.Inventory.Save(ctx, inv)} {
				if e != nil {
					t.Fatal(e)
				}
			}
			class := byte(1)
			if scenario == "wrong_class" {
				class = 0
			}
			handled, e := currentGameplayModules.DispatchUpper(s, session, 788, class, body)
			if e != nil || !handled {
				t.Fatal(e)
			}
			if scenario == "wrong_class" {
				aa, _, _ := g.Account.Load(ctx, a.AccountID)
				ss, _, _ := g.AccountInventory.Load(ctx, shared.AccountID)
				ii, _, _ := g.Inventory.Load(ctx, "3")
				if conn.write.Len() != 0 || !reflect.DeepEqual(aa, a) || !reflect.DeepEqual(ss, shared) || !reflect.DeepEqual(ii, inv) {
					t.Fatal("wrong class mutated assets")
				}
				return
			}
			packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
			if packet.Header.MsgID != 788 || packet.Header.Classification != 1 {
				t.Fatal(packet.Header)
			}
			afterA, _, _ := g.Account.Load(ctx, a.AccountID)
			afterS, _, _ := g.AccountInventory.Load(ctx, shared.AccountID)
			afterI, _, _ := g.Inventory.Load(ctx, "3")
			afterCh, _, _ := g.Character.Load(ctx, "3")
			if !reflect.DeepEqual(ch, afterCh) {
				t.Fatal("craft changed production/progression state")
			}
			success := scenario == "low" || scenario == "middle" || scenario == "high" || scenario == "exact_souls" || scenario == "login_only" || scenario == "leftover_pieces"
			if !success {
				if len(packet.Body) != 2 || packet.Body[0] != 0 || len(rest) != 0 || !reflect.DeepEqual(a, afterA) || !reflect.DeepEqual(shared, afterS) || !reflect.DeepEqual(inv, afterI) {
					t.Fatalf("rejected action changed assets or reply %x", packet.Body)
				}
				return
			}
			wantBalance := "0"
			if scenario == "leftover_pieces" {
				wantBalance = "1005"
			}
			if !bytes.Equal(packet.Body, []byte{1}) || afterA.Metadata[repo.AccountEpicPieceMetadataKey(r.PieceID)] != wantBalance || afterA.Metadata["unrelated"] != "keep" {
				t.Fatal("wrong commit", packet.Body, afterA)
			}
			if len(afterI.Slots) != 1 {
				t.Fatal(afterI)
			}
			for _, stack := range afterI.Slots {
				if stack.ItemID != int64(output) || stack.Count != 1 || len(stack.RawEntry) == 0 || stack.Extra["quality_seed"] == "" {
					t.Fatalf("invalid equipment grant %+v", stack)
				}
			}
			for _, stack := range afterS.Slots {
				if stack.Count != 5 {
					t.Fatal("soul cost", stack)
				}
			}
			if scenario == "exact_souls" && len(afterS.Slots) != 0 {
				t.Fatal("exhausted soul rows retained")
			}
			update, tail := splitGameServerUpperPacketWithHeader(t, rest, 16)
			if update.Header.MsgID != 14 {
				t.Fatal(update.Header)
			}
			balance, trailing := splitGameServerUpperPacketWithHeader(t, tail, 16)
			if balance.Header.MsgID != 1149 || len(trailing) != 0 {
				t.Fatal("missing balance sync", balance.Header)
			}
			if scenario == "leftover_pieces" {
				if len(balance.Body) != 21 || balance.Body[0] != 0 || binary.LittleEndian.Uint32(balance.Body[1:5]) != 2 || binary.LittleEndian.Uint32(balance.Body[5:9]) != r.PieceID || binary.LittleEndian.Uint32(balance.Body[9:13]) != 1005 || binary.LittleEndian.Uint32(balance.Body[13:17]) != 10300001 || binary.LittleEndian.Uint32(balance.Body[17:21]) != 17 {
					t.Fatalf("wrong full ledger %x", balance.Body)
				}
			} else if !bytes.Equal(balance.Body, []byte{0, 0, 0, 0, 0}) {
				t.Fatalf("consumed final pieces did not clear native map: %x", balance.Body)
			}
		})
	}
}

func TestRealPVFEpicBookRepeatedLegitimateCraftRound9(t *testing.T) {
	s, session, conn, g, r := epicBookRealFixtureRound9(t, 450004)
	ctx := context.Background()
	a, _, _ := g.Account.Load(ctx, session.accountID)
	a.Metadata[repo.AccountEpicPieceMetadataKey(r.PieceID)] = "2000"
	shared, _, _ := g.AccountInventory.Load(ctx, session.accountID)
	for _, m := range r.Materials {
		if m.ItemID == r.PieceID {
			continue
		}
		slot, _ := repo.AccountWarehouseSlotForItem(int64(m.ItemID))
		key := repo.AccountSharedInventorySlotKey(slot)
		stack := shared.Slots[key]
		stack.Count = int64(2 * m.Count)
		shared.Slots[key] = stack
	}
	if e := g.Account.Save(ctx, a); e != nil {
		t.Fatal(e)
	}
	if e := g.AccountInventory.Save(ctx, shared); e != nil {
		t.Fatal(e)
	}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, 450004)
	for n := 0; n < 2; n++ {
		conn.write.Reset()
		if e := s.handleCurrentEpicBookCraft(session, body); e != nil {
			t.Fatal(e)
		}
		p, _ := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
		if !bytes.Equal(p.Body, []byte{1}) {
			t.Fatalf("legitimate craft %d refused: %x", n, p.Body)
		}
	}
	inv, _, _ := g.Inventory.Load(ctx, "3")
	if len(inv.Slots) != 2 {
		t.Fatal("two paid crafts must grant twice", inv)
	}
	conn.write.Reset()
	if e := s.handleCurrentEpicBookCraft(session, body); e != nil {
		t.Fatal(e)
	}
	p, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if !bytes.Equal(p.Body, []byte{0, 22}) || len(rest) != 0 {
		t.Fatal("third unpaid craft accepted", p.Body)
	}
}

func TestRealPVFEpicBookConcurrentCharactersOneGrantRound9(t *testing.T) {
	s, session, _, g, r := epicBookRealFixtureRound9(t, 450004)
	ctx := context.Background()
	if e := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "4", AccountID: session.accountID, Level: 90}); e != nil {
		t.Fatal(e)
	}
	if e := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "4", Slots: map[string]repo.ItemStack{}}); e != nil {
		t.Fatal(e)
	}
	c, e := epicbook.LoadCatalog(s.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	items, e := s.currentPVFItemCatalog()
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []uint16{3, 4} {
		wg.Add(1)
		go func(id uint16) {
			defer wg.Done()
			_, e := s.commitCurrentEpicBookCraft(ctx, &gameSession{selectedCharacterID: id, accountID: session.accountID}, c, items, 450004, time.Now())
			errs <- e
		}(id)
	}
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		} else if !errors.Is(e, repo.ErrEpicBookPieceInsufficient) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("same 1000 pieces granted twice", success)
	}
	a, _, _ := g.Account.Load(ctx, session.accountID)
	if a.Metadata[repo.AccountEpicPieceMetadataKey(r.PieceID)] != "0" {
		t.Fatal(a)
	}
	one, _, _ := g.Inventory.Load(ctx, "3")
	two, _, _ := g.Inventory.Load(ctx, "4")
	if len(one.Slots)+len(two.Slots) != 1 {
		t.Fatal("grant total wrong")
	}
}
