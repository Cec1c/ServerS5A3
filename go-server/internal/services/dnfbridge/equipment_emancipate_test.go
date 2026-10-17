package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func emancipationFixtureSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "1000 `ticket.stk`\n2000 `piece.stk`\n10099773 `soul.stk`\n",
		"equipment/equipment.lst": "3000 `source.equ`\n4000 `output.equ`\n",
		"stackable/ticket.stk":    "[stackable type] `[etc]` 1\n[emancipate ticket] 20\n[need material] 5555 20\n[/need material]\n",
		"stackable/piece.stk":     "[stackable type] `[material]` 0\n[stack limit] 1000\n",
		"stackable/soul.stk":      "[stackable type] `[material]` 0\n[stack limit] 1000\n",
		"equipment/source.equ":    "[equipment type] `[coat]` 19\n[durability] 28\n[emancipate]\n[input] 2000 10 10099773 12\n[/input]\n[output] 4000 1\n[/output]\n[emancipate type] 20\n[/emancipate]\n",
		"equipment/output.equ":    "[equipment type] `[coat]` 19\n[rarity] 3\n[durability] 28\n[attach type] `[sealing]`\n",
	}
}

func emancipationBody() []byte {
	body := make([]byte, 19)
	binary.LittleEndian.PutUint16(body, 69)
	binary.LittleEndian.PutUint32(body[2:], 1000)
	binary.LittleEndian.PutUint16(body[6:], 30)
	binary.LittleEndian.PutUint32(body[8:], 3000)
	binary.LittleEndian.PutUint16(body[13:], math.MaxUint16)
	binary.LittleEndian.PutUint32(body[15:], math.MaxUint16)
	return body
}

func emancipationFixture(t *testing.T) (*Service, *gameSession, *bufferConn, dnfrepo.Group) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint32(raw[2:], 3000)
	raw[0x0a], raw[0x13], raw[0x57] = 15, 131, 8
	binary.LittleEndian.PutUint32(raw[0x0e:], 99999)
	inv := dnfrepo.InventoryRecord{CharacterID: "2", Slots: map[string]dnfrepo.ItemStack{
		"0:30":  {ItemID: 3000, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[coat]", "ext_data0": "15", "value_a": "99999", "byte_13": "131", "byte_57": "8"}},
		"0:69":  {ItemID: 1000, Count: 2},
		"0:70":  {ItemID: 1000, Count: 9},
		"0:121": {ItemID: 2000, Count: 20},
		// Stale character copy must never fund an account-owned ingredient.
		"0:130": {ItemID: 10099773, Count: 999},
	}}
	account := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{"0:362": {ItemID: 10099773, Count: 24}}}
	for _, err := range []error{g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 10000}}), g.Inventory.Save(ctx, inv), g.AccountInventory.Save(ctx, account)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	s := equipmentRecipeCompoundTestService(t, emancipationFixtureSource(), g)
	conn := &bufferConn{}
	return s, &gameSession{conn: conn, selectedCharacterID: 2}, conn, g
}

func TestEquipmentEmancipateCommitsOwnedAssetsAndCurrentResponse(t *testing.T) {
	s, session, conn, g := emancipationFixture(t)
	handled, err := currentGameplayModules.DispatchUpper(s, session, 271, 1, emancipationBody())
	if !handled || err != nil {
		t.Fatalf("dispatch=%t %v", handled, err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	wantAck := []byte{1, 1, 3, 0, 69, 0, 1, 0, 0, 0, 0, 121, 0, 10, 0, 0, 0, 0, 106, 1, 12, 0, 0, 0}
	if ack.Header.MsgID != 271 || !bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("ack=%d %x", ack.Header.MsgID, ack.Body)
	}
	empty, rest := splitGameServerUpperPacket(t, rest)
	if empty.Header.MsgID != 14 || !bytes.Equal(empty.Body, buildCurrentItemUpdateBody(0, []currentItemListEntry{func() currentItemListEntry { var e currentItemListEntry; e.patchCore(30, math.MaxUint32, 0); return e }()})) {
		t.Fatalf("old target not invalidated: %x", empty.Body)
	}
	update, rest := splitGameServerUpperPacket(t, rest)
	if update.Header.MsgID != 14 || len(rest) != 0 {
		t.Fatalf("updates=%d trailing=%d", update.Header.MsgID, len(rest))
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "2")
	account, _, _ := g.AccountInventory.Load(context.Background(), "account-1")
	c, _, _ := g.Character.Load(context.Background(), "2")
	if inv.Slots["0:69"].Count != 1 || inv.Slots["0:70"].Count != 9 || inv.Slots["0:121"].Count != 10 || inv.Slots["0:130"].Count != 999 || account.Slots["0:362"].Count != 12 || c.Stats["gold"] != 10000 {
		t.Fatalf("incorrect settlement inv=%+v account=%+v", inv, account)
	}
	out := inv.Slots["0:30"]
	entry := currentItemListEntryFromStack(0, 30, out)
	if out.ItemID != 4000 || out.Count != 1 || !out.Bind || entry.data[0x0d] != 0 || entry.data[0x0a] != 0 || entry.data[0x13] != 0 || entry.data[0x57] != 0 || binary.LittleEndian.Uint32(entry.data[0x0e:]) != 0 {
		t.Fatalf("output attributes/binding: %+v row=%x", out, entry.data[:])
	}
	if !bytes.Contains(update.Body, entry.data[:]) {
		t.Fatal("authoritative target absent from refresh")
	}
	before := dnfrepo.CloneInventory(inv)
	r, _ := parseCurrentEquipmentEmancipateRequest(emancipationBody())
	if _, err := s.commitCurrentEquipmentEmancipate(context.Background(), session, r); err == nil {
		t.Fatal("stale target replay accepted")
	}
	after, _, _ := g.Inventory.Load(context.Background(), "2")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("replay changed inventory")
	}
}

func TestEquipmentEmancipateFailuresRollBackBothOwners(t *testing.T) {
	for _, name := range []string{"book_missing", "book_locked", "target_locked", "target_expired", "materials_short", "account_short", "account_missing", "account_locked", "bad_hint", "wrong_owner", "unsupported_book", "source_type", "output_missing", "foreign_input", "ambiguous_block"} {
		t.Run(name, func(t *testing.T) {
			s, session, _, g := emancipationFixture(t)
			ctx := context.Background()
			inv, _, _ := g.Inventory.Load(ctx, "2")
			account, _, _ := g.AccountInventory.Load(ctx, "account-1")
			c, _, _ := g.Character.Load(ctx, "2")
			r, _ := parseCurrentEquipmentEmancipateRequest(emancipationBody())
			source := emancipationFixtureSource()
			setLocked := func(key string) {
				stack := inv.Slots[key]
				if stack.Extra == nil {
					stack.Extra = map[string]string{}
				}
				stack.Extra["equipment_lock_state"] = "1"
				inv.Slots[key] = stack
			}
			switch name {
			case "book_missing":
				delete(inv.Slots, "0:69")
			case "book_locked":
				setLocked("0:69")
			case "target_locked":
				setLocked("0:30")
			case "target_expired":
				stack := inv.Slots["0:30"]
				stack.ExpireAt = time.Unix(1, 0)
				inv.Slots["0:30"] = stack
			case "materials_short":
				stack := inv.Slots["0:121"]
				stack.Count = 9
				inv.Slots["0:121"] = stack
			case "account_short":
				account.Slots["0:362"] = dnfrepo.ItemStack{ItemID: 10099773, Count: 11}
			case "account_missing":
				delete(account.Slots, "0:362")
			case "account_locked":
				account.Slots["0:362"] = dnfrepo.ItemStack{ItemID: 10099773, Count: 24, Extra: map[string]string{"equipment_lock_state": "1"}}
			case "bad_hint":
				r.MaterialSlot, r.MaterialID = 121, 9999
			case "wrong_owner":
				c.AccountID = "another-account"
			case "unsupported_book":
				source["stackable/ticket.stk"] = "[stackable type] `[etc]` 1\n[emancipate ticket] 18\n"
			case "source_type":
				source["equipment/source.equ"] = "[equipment type] `[coat]` 19\n[durability] 28\n[emancipate]\n[input] 2000 10\n[/input]\n[output] 4000 1\n[/output]\n[emancipate type] 17\n[/emancipate]\n"
			case "output_missing":
				delete(source, "equipment/output.equ")
			case "foreign_input":
				source["equipment/source.equ"] = "[equipment type] `[coat]` 19\n[durability] 28\n[input] 2000 10\n[/input]\n[emancipate]\n[output] 4000 1\n[/output]\n[emancipate type] 20\n[/emancipate]\n"
			case "ambiguous_block":
				source["equipment/source.equ"] += "[emancipate]\n[/emancipate]\n"
			}
			for _, err := range []error{g.Inventory.Save(ctx, inv), g.AccountInventory.Save(ctx, account), g.Character.Save(ctx, c)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			s.pvfItemCatalog, _ = newPVFDungeonDropCatalog(source)
			before, _, _ := g.Inventory.Load(ctx, "2")
			beforeA, _, _ := g.AccountInventory.Load(ctx, "account-1")
			beforeC, _, _ := g.Character.Load(ctx, "2")
			if _, err := s.commitCurrentEquipmentEmancipate(ctx, session, r); err == nil {
				t.Fatal("invalid conversion accepted")
			}
			after, _, _ := g.Inventory.Load(ctx, "2")
			afterA, _, _ := g.AccountInventory.Load(ctx, "account-1")
			afterC, _, _ := g.Character.Load(ctx, "2")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeA, afterA) || !reflect.DeepEqual(beforeC, afterC) {
				t.Fatal("failed conversion mutated assets")
			}
		})
	}
}

type emancipationFailConn struct{ bufferConn }

func (*emancipationFailConn) Write([]byte) (int, error) {
	return 0, errors.New("injected conversion ack failure")
}

func TestEquipmentEmancipateSendFailureKeepsSingleCommit(t *testing.T) {
	s, session, _, g := emancipationFixture(t)
	conn := &emancipationFailConn{}
	session.conn = conn
	if err := s.handleCurrentEquipmentEmancipate(session, emancipationBody()); err == nil {
		t.Fatal("send error swallowed")
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "2")
	a, _, _ := g.AccountInventory.Load(context.Background(), "account-1")
	if inv.Slots["0:30"].ItemID != 4000 || inv.Slots["0:69"].Count != 1 || a.Slots["0:362"].Count != 12 {
		t.Fatal("send failure reverted transaction")
	}
	r, _ := parseCurrentEquipmentEmancipateRequest(emancipationBody())
	if _, err := s.commitCurrentEquipmentEmancipate(context.Background(), session, r); err == nil {
		t.Fatal("replay after send error committed")
	}
}

func TestEquipmentEmancipateMaterialHintAndSealedSource(t *testing.T) {
	s, session, _, g := emancipationFixture(t)
	inv, _, _ := g.Inventory.Load(context.Background(), "2")
	target := inv.Slots["0:30"]
	target.Extra["seal_flag"] = "1"
	inv.Slots["0:30"] = target
	if err := g.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	r, _ := parseCurrentEquipmentEmancipateRequest(emancipationBody())
	r.MaterialSlot = 362
	r.MaterialID = 10099773
	if _, err := s.commitCurrentEquipmentEmancipate(context.Background(), session, r); err != nil {
		t.Fatal(err)
	}
	after, _, _ := g.Inventory.Load(context.Background(), "2")
	if entry := currentItemListEntryFromStack(0, 30, after.Slots["0:30"]); entry.data[0x0d] != 1 {
		t.Fatal(fmt.Sprintf("seal lost: %x", entry.data))
	}
}

type emancipationFailAccountSave struct {
	dnfrepo.AccountInventoryRepository
}

func (emancipationFailAccountSave) Save(context.Context, dnfrepo.AccountInventoryRecord) error {
	return errors.New("injected account inventory save failure")
}

type emancipationFailSettlement struct {
	dnfrepo.CharacterSettlementUnitOfWork
}

func (w emancipationFailSettlement) WithinCharacterSettlement(ctx context.Context, id string, project func(dnfrepo.Group) error) error {
	return w.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		tx.AccountInventory = emancipationFailAccountSave{tx.AccountInventory}
		return project(tx)
	})
}
func TestEquipmentEmancipateAccountSaveFailureRollsBackEarlierInventorySave(t *testing.T) {
	s, session, _, g := emancipationFixture(t)
	ctx := context.Background()
	before, _, _ := g.Inventory.Load(ctx, "2")
	beforeA, _, _ := g.AccountInventory.Load(ctx, "account-1")
	wrapped := g
	wrapped.CharacterSettlement = emancipationFailSettlement{g.CharacterSettlement}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return wrapped, true }
	r, _ := parseCurrentEquipmentEmancipateRequest(emancipationBody())
	if _, err := s.commitCurrentEquipmentEmancipate(ctx, session, r); err == nil {
		t.Fatal("account save failure swallowed")
	}
	after, _, _ := g.Inventory.Load(ctx, "2")
	afterA, _, _ := g.AccountInventory.Load(ctx, "account-1")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeA, afterA) {
		t.Fatal("transaction committed an earlier aggregate before failed account save")
	}
}
