package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	inv "longheng.io/server/internal/modules/dnf/inventory"
	mail "longheng.io/server/internal/modules/dnf/mail"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	pvf "longheng.io/server/internal/platform/pvf"
)

var route510Once sync.Once
var route510Catalog *pvfDungeonDropCatalog
var route510Err error

func route510Definition(t *testing.T, id uint32) dungeonDropItemDefinition {
	t.Helper()
	// Candidate 532 edit vs the frozen 510 control: run510.py always exported
	// DNFBRIDGE_REAL_PVF_SMOKE. In a default environment the whole package should
	// skip these fixed-PVF-only controls instead of failing the joint default
	// bridge run. With the env set the behaviour is byte-for-byte the old one:
	// nothing is skipped and no assertion is relaxed.
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("TestNormalRoute510 controls require DNFBRIDGE_REAL_PVF_SMOKE (fixed Script.pvf)")
	}
	route510Once.Do(func() {
		p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
		if p == "" {
			route510Err = errors.New("fixed PVF required")
			return
		}
		var a *pvf.Archive
		a, route510Err = pvf.Open(p)
		if route510Err == nil {
			route510Catalog, route510Err = newPVFDungeonDropCatalog(a)
		}
	})
	if route510Err != nil {
		t.Fatal(route510Err)
	}
	d, err := route510Catalog.ResolveItem(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func route510Setup(t *testing.T) (repo.Group, *inv.Owner) {
	t.Helper()
	r := memory.NewMemoryGroup()
	ctx := context.Background()
	for _, err := range []error{
		r.Account.Save(ctx, repo.AccountRecord{AccountID: "synthetic-normal510", Metadata: map[string]string{"account_cargo_created": "true", "account_cargo_level": "8"}}),
		r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "synthetic-normal510", Level: 90}),
		r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	o, err := inv.NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, o
}
func route510NormalRows(t *testing.T, r repo.Group, id uint32) []int16 {
	t.Helper()
	v, ok, err := r.Inventory.Load(context.Background(), "77")
	if err != nil || !ok {
		t.Fatal("missing inventory", err)
	}
	var slots []int16
	for key, s := range v.Slots {
		if s.ItemID != int64(id) {
			continue
		}
		l, p, ok := strings.Cut(key, ":")
		n, err := strconv.Atoi(p)
		if !ok || l != "0" || err != nil || !((n >= 3 && n <= 8) || (n >= int(route510Definition(t, id).SlotStart) && n <= int(route510Definition(t, id).SlotEnd))) {
			t.Fatalf("normal producer placed item %d outside actual PVF quickbar/page: %s", id, key)
		}
		if repo.IsAccountSharedInventorySlot(0, int16(n)) {
			t.Fatal("417 must not enter shared warehouse")
		}
		slots = append(slots, int16(n))
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	return slots
}
func route510Mail(t *testing.T, r repo.Group, id uint32, count int64, legacy bool, extra map[string]string) []int16 {
	t.Helper()
	ctx := context.Background()
	err := r.Mailbox.Save(ctx, repo.MailboxRecord{CharacterID: "77", Mails: map[string]repo.MailRecord{"1": {MailID: "1", Attachments: []repo.MailAttachment{{ItemID: int64(id), Count: count, Extra: extra}}}}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{pvfItemCatalog: route510Catalog, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	resolver, err := s.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		normal := resolver
		// Compatibility fixture reproduces the BEFORE439 mail identity shape; new
		// mail remains unmodified. No historical/live row is claimed observed.
		resolver = func(id uint32) (alignedcmd.MailboxItemResolution, error) {
			x, err := normal(id)
			if err == nil {
				delete(x.DefaultExtra, "stack_limit")
			}
			return x, err
		}
	}
	if _, err := mail.NewOwner(r, resolver).Claim(ctx, "77", mail.ExtractRequest{MailIDs: []uint32{1}}); err != nil {
		t.Fatal(err)
	}
	return route510NormalRows(t, r, id)
}
func route510Drop(t *testing.T, r repo.Group, d dungeonDropItemDefinition, count uint32) int16 {
	t.Helper()
	slot, _, err := grantCurrentDungeonPickupItem(context.Background(), r.CharacterItems, "77", d, count, time.Unix(1900000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	rows := route510NormalRows(t, r, d.ItemID)
	found := false
	for _, x := range rows {
		if x == int16(slot) {
			found = true
		}
	}
	if !found {
		t.Fatal("drop transaction not persisted in normal consumable page")
	}
	return int16(slot)
}
func route510Deposit(t *testing.T, o *inv.Owner, slot, dst int16, count int32) inv.MoveResult {
	t.Helper()
	if !((slot >= 3 && slot <= 8) || (slot >= 65 && slot <= 176)) || dst < 0 || dst >= 8 {
		t.Fatal("test endpoint is not a normal produced inventory/account-cargo slot")
	}
	x, err := o.Move(context.Background(), inv.Command{AccountID: "synthetic-normal510", SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: slot, MoveCount: count, DestinationListType: 12, DestinationSlotIndex: dst})
	if err != nil {
		t.Fatal(err)
	}
	return x
}
func route510Cargo(t *testing.T, r repo.Group) repo.AccountInventoryRecord {
	t.Helper()
	c, found, err := r.AccountInventory.Load(context.Background(), "synthetic-normal510")
	if err != nil || !found {
		t.Fatal("cargo missing", err)
	}
	for key := range c.Slots {
		l, s, ok := strings.Cut(key, ":")
		n, err := strconv.Atoi(s)
		if !ok || l != "12" || err != nil || n < 0 || n >= 8 {
			t.Fatal("illegal cargo row", key)
		}
	}
	return c
}
func route510Bound(t *testing.T, r repo.Group, total int64, wantCap bool) {
	t.Helper()
	c := route510Cargo(t, r)
	var got int64
	for _, s := range c.Slots {
		if s.ItemID != 417 {
			t.Fatal("wrong actual template")
		}
		got += s.Count
		if s.Count <= 0 || s.Count > 1000 {
			t.Fatalf("actual 417 count exceeds cap: %d", s.Count)
		}
		if wantCap && s.Extra["stack_limit"] != "1000" {
			t.Fatalf("adopted/producer cap missing: count=%d", s.Count)
		}
	}
	if got != total {
		t.Fatalf("quantity lost: got%d want%d", got, total)
	}
}
func TestNormalRoute510ResourceAndMailSplit(t *testing.T) {
	d := route510Definition(t, 417)
	if d.StackLimit != 1000 || d.SlotStart != 65 || d.SlotEnd != 120 || d.Kind != dungeonDropItemStackable || !d.ExpirationDate.IsZero() {
		t.Fatalf("real definition changed: %+v", d)
	}
	if _, mapped := repo.AccountWarehouseSlotForItem(417); mapped {
		t.Fatal("417 wrongly mapped into fixed shared warehouse")
	}
	t.Logf("REAL_PVF417 path=%s type=%s range=%d..%d cap=%d", d.PVFPath, d.StackableType, d.SlotStart, d.SlotEnd, d.StackLimit)
	r, o := route510Setup(t)
	rows := route510Mail(t, r, 417, 2007, false, nil)
	if len(rows) != 3 {
		t.Fatalf("normal mail did not split into three rows: %v", rows)
	}
	v, _, _ := r.Inventory.Load(context.Background(), "77")
	counts := []int64{}
	for n, slot := range rows {
		counts = append(counts, v.Slots[fmt.Sprintf("0:%d", slot)].Count)
		route510Deposit(t, o, slot, int16(n), 0)
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i] < counts[j] })
	if !reflect.DeepEqual(counts, []int64{7, 1000, 1000}) {
		t.Fatal("mail allocator did not obey actual1000 cap", counts)
	}
	route510Bound(t, r, 2007, true)
}
func TestNormalRoute510MailDropDeposit(t *testing.T) {
	d := route510Definition(t, 417)
	r, o := route510Setup(t)
	rows := route510Mail(t, r, 417, 7, false, nil)
	route510Deposit(t, o, rows[0], 0, 0)
	slot := route510Drop(t, r, d, 5)
	x := route510Deposit(t, o, slot, 1, 0)
	c := route510Cargo(t, r)
	if x.Mode != "auto_stack" || len(c.Slots) != 1 || c.Slots["12:0"].Count != 12 {
		t.Fatalf("actual mail/drop normal-route not merged: %s rows%d", x.Mode, len(c.Slots))
	}
	route510Bound(t, r, 12, true)
}
func TestNormalRoute510LegacyCapPersistsAfterColdOwner(t *testing.T) {
	d := route510Definition(t, 417)
	r, o := route510Setup(t)
	rows := route510Mail(t, r, 417, 993, true, nil)
	route510Deposit(t, o, rows[0], 0, 0)
	slot := route510Drop(t, r, d, 5)
	x := route510Deposit(t, o, slot, 1, 0)
	c := route510Cargo(t, r)
	if x.Mode != "auto_stack" || len(c.Slots) != 1 || c.Slots["12:0"].Count != 998 || c.Slots["12:0"].Extra["stack_limit"] != "1000" {
		t.Fatalf("accepted cap not durable: mode%s count%d cap=%q", x.Mode, c.Slots["12:0"].Count, c.Slots["12:0"].Extra["stack_limit"])
	}
	o, err := inv.NewOwner(r)
	if err != nil {
		t.Fatal(err)
	} // memory reload; not SQL/cold client proof.
	rows = route510Mail(t, r, 417, 5, true, nil)
	route510Deposit(t, o, rows[0], 2, 0)
	route510Bound(t, r, 1003, false)
	if len(route510Cargo(t, r).Slots) != 2 {
		t.Fatal("cap guard should retain separate tail")
	}
}
func TestNormalRoute510LegacySequenceCannotOverstack(t *testing.T) {
	d := route510Definition(t, 417)
	r, o := route510Setup(t)
	rows := route510Mail(t, r, 417, 993, true, nil)
	route510Deposit(t, o, rows[0], 0, 0)
	route510Deposit(t, o, route510Drop(t, r, d, 5), 1, 0)
	rows = route510Mail(t, r, 417, 5, true, nil)
	route510Deposit(t, o, rows[0], 2, 0)
	route510Bound(t, r, 1003, false) // limit persistence separately checked above.
}
func TestNormalRoute510OrdinaryMetadata(t *testing.T) {
	d := route510Definition(t, 417)
	t.Run("different_attribution_source_can_merge", func(t *testing.T) {
		r, o := route510Setup(t)
		rows := route510Mail(t, r, 417, 7, false, map[string]string{"source": "mail-normal510", "last_grant_source": "public-test-attribution"})
		route510Deposit(t, o, rows[0], 0, 0)
		x := route510Deposit(t, o, route510Drop(t, r, d, 5), 1, 0)
		c := route510Cargo(t, r)
		if x.Mode != "auto_stack" || len(c.Slots) != 1 || c.Slots["12:0"].Count != 12 || c.Slots["12:0"].Extra["source"] != "mail-normal510" || c.Slots["12:0"].Extra["last_grant_source"] != "public-test-attribution" {
			t.Fatal("attribution metadata lost or blocked normal merge")
		}
	})
	for _, key := range []string{"stackable_type", "expire_time", "expire_unix"} {
		t.Run("explicit_identity_conflict_"+key, func(t *testing.T) {
			r, o := route510Setup(t)
			// Faulted/legacy attachment identity controls, not genuine PVF417 variants.
			// Mail's production constructor preserves explicit attachment Extra;
			// repository's declared identity keys must not gain permission through cap adoption.
			rows := route510Mail(t, r, 417, 7, false, map[string]string{key: "public-identity-A"})
			route510Deposit(t, o, rows[0], 0, 0)
			rows = route510Mail(t, r, 417, 5, false, map[string]string{key: "public-identity-B"})
			x := route510Deposit(t, o, rows[0], 1, 0)
			c := route510Cargo(t, r)
			if x.Mode == "auto_stack" || x.Mode == "stack" || len(c.Slots) != 2 {
				t.Fatalf("explicit business identity conflict merged: key%s mode%s rows%d", key, x.Mode, len(c.Slots))
			}
		})
	}
}
func TestNormalRoute510OwnerBoundaries(t *testing.T) {
	d := route510Definition(t, 417)
	for _, name := range []string{"capacity_slot8", "different_bind", "different_expiry"} {
		t.Run(name, func(t *testing.T) {
			r, o := route510Setup(t)
			rows := route510Mail(t, r, 417, 7, false, nil)
			route510Deposit(t, o, rows[0], 0, 0)
			slot := route510Drop(t, r, d, 5)
			v, _, _ := r.Inventory.Load(context.Background(), "77")
			key := fmt.Sprintf("0:%d", slot)
			s := v.Slots[key]
			switch name {
			case "different_bind":
				s.Bind = !s.Bind
			case "different_expiry":
				s.ExpireAt = time.Unix(1900000000, 0).UTC()
			}
			v.Slots[key] = s
			if err := r.Inventory.Save(context.Background(), v); err != nil {
				t.Fatal(err)
			}
			beforeI, _, _ := r.Inventory.Load(context.Background(), "77")
			beforeC := route510Cargo(t, r)
			if name == "capacity_slot8" {
				_, err := o.Move(context.Background(), inv.Command{AccountID: "synthetic-normal510", SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: slot, MoveCount: 5, DestinationListType: 12, DestinationSlotIndex: 8})
				if !errors.Is(err, inv.ErrAccountCargoSlotOutOfRange) {
					t.Fatal("illegal account-cargo slot not rejected", err)
				}
				afterI, _, _ := r.Inventory.Load(context.Background(), "77")
				afterC := route510Cargo(t, r)
				if !reflect.DeepEqual(beforeI, afterI) || !reflect.DeepEqual(beforeC, afterC) {
					t.Fatal("illegal destination mutated state")
				}
				return
			}
			x := route510Deposit(t, o, slot, 1, 0)
			if x.Mode == "auto_stack" || len(route510Cargo(t, r).Slots) != 2 {
				t.Fatal("different bind/expiry merged")
			}
		})
	}
}
func TestNormalRoute510LegalSharedCrystalsAreUncapped(t *testing.T) {
	_ = route510Definition(t, 417)
	for slot := repo.CrystalWarehouseFirstSlot; slot <= repo.SoulWarehouseLastSlot; slot++ {
		id, found := repo.AccountWarehouseItemForSlot(slot)
		if !found {
			t.Fatal("mapping missing")
		}
		d := route510Definition(t, uint32(id))
		if d.StackLimit != 0 {
			t.Fatalf("legal shared occupant capped: id%d cap%d", id, d.StackLimit)
		}
	}
	// Genuine fixed-slot mail routing and UOW creditor, not417 stuffed into354.
	r, _ := route510Setup(t)
	s := &Service{pvfItemCatalog: route510Catalog, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	resolver, err := s.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver(3033)
	if err != nil || resolution.AccountWarehouseSlot != 354 {
		t.Fatal("normal composition did not enable shared crystal routing", err, resolution.AccountWarehouseSlot)
	}
	if err := r.Mailbox.Save(context.Background(), repo.MailboxRecord{CharacterID: "77", Mails: map[string]repo.MailRecord{"1": {MailID: "1", Attachments: []repo.MailAttachment{{ItemID: 3033, Count: 2400}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := mail.NewOwner(r, resolver).WithWarehouseCreditor(s.alignedWarehouseCreditorForCommand(dnfenum.CmdPacketMailboxExtractItem, "synthetic-normal510")).Claim(context.Background(), "77", mail.ExtractRequest{MailIDs: []uint32{1}}); err != nil {
		t.Fatal(err)
	}
	c, _, _ := r.AccountInventory.Load(context.Background(), "synthetic-normal510")
	if c.Slots["0:354"].Count != 2400 || c.Slots["0:354"].Extra["stack_limit"] != "" {
		t.Fatal("real uncapped crystal route invented cap or lost amount")
	}
}

// A real material page control uses an actual material, never relabels417.
func TestNormalRoute510MaterialPageWithActualMaterial(t *testing.T) {
	d := route510Definition(t, 3277)
	if d.SlotStart != 121 || d.SlotEnd != 176 || (d.StackLimit > 0 && d.StackLimit < 12) {
		t.Fatalf("actual material contract changed: %+v", d)
	}
	t.Logf("REAL_PVF3277 path=%s type=%s range=%d..%d cap=%d", d.PVFPath, d.StackableType, d.SlotStart, d.SlotEnd, d.StackLimit)
	r, o := route510Setup(t)
	// Real capped mail fills all six quickbar slots; no fabricated occupants.
	if len(route510Mail(t, r, 417, 6000, false, nil)) != 6 {
		t.Fatal("real quickbar setup failed")
	}
	rows := route510Mail(t, r, 3277, 7, false, nil)
	if len(rows) != 1 || rows[0] < 121 || rows[0] > 176 {
		t.Fatal("actual material page not reached", rows)
	}
	route510Deposit(t, o, rows[0], 0, 0)
	slot := route510Drop(t, r, d, 5)
	if slot < 121 || slot > 176 {
		t.Fatal("normal material drop not placed on material page")
	}
	x := route510Deposit(t, o, slot, 1, 0)
	c := route510Cargo(t, r)
	if x.Mode != "auto_stack" || len(c.Slots) != 1 || c.Slots["12:0"].ItemID != 3277 || c.Slots["12:0"].Count != 12 {
		t.Fatal("real material quantity lost or incorrectly separated")
	}
}
