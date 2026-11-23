package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	mem "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type dimensionSource map[string]string

func (s dimensionSource) ReadText(p string) (string, error) {
	v, ok := s[p]
	if !ok {
		return "", errors.New("missing " + p)
	}
	return v, nil
}
func dimensionTestSource() dimensionSource {
	return dimensionSource{
		"monster/monster.lst":             "",
		"equipment/equipment.lst":         "100352735 `a.equ`\n100344405 `b.equ`\n",
		"equipment/a.equ":                 "[equipment type]\n`[magic stone]`\n[grade]\n85\n[durability]\n40\n",
		"equipment/b.equ":                 "[equipment type]\n`[support]`\n[grade]\n85\n[durability]\n40\n",
		"stackable/stackable.lst":         "10158122 `10158001/10158122.stk`\n",
		"stackable/10158001/10158122.stk": "[stackable type]\n`[waste]` 0\n[action type]\n`[item change]`\n[/action type]\n[transformation input]\n1 100352735 100344405\n[/transformation input]\n[transformation output]\n1 100352735 1000 100344405 1000\n[/transformation output]\n",
	}
}
func TestDimensionTransformNativeRequest(t *testing.T) {
	b := []byte{0, 9, 0, 0, 0, 0, 0, 69, 0}
	binary.LittleEndian.PutUint32(b[3:7], 100352735)
	r, e := parseCurrentDimensionTransformRequest(b)
	if e != nil || r.Slot != 9 || r.SourceSlot != 69 || r.ItemID != 100352735 {
		t.Fatalf("%+v %v", r, e)
	}
	for _, bad := range [][]byte{nil, b[:7], append(append([]byte{}, b...), 0), {1, 9, 0, 1, 0, 0, 0, 69, 0}, {0, 2, 0, 1, 0, 0, 0, 69, 0}} {
		if _, e := parseCurrentDimensionTransformRequest(bad); e == nil {
			t.Fatalf("admitted %x", bad)
		}
	}
}
func dimensionFixture(t *testing.T) (*Service, *gameSession, repo.Group) {
	t.Helper()
	g := mem.NewMemoryGroup()
	ctx := context.Background()
	if e := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90}); e != nil {
		t.Fatal(e)
	}
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw, 9)
	binary.LittleEndian.PutUint32(raw[2:], 100352735)
	raw[10] = 12
	inv := repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 100352735, Count: 1, RawEntry: raw, Extra: map[string]string{"reinforce_level": "12", "enchant": "999"}}, "0:69": {ItemID: 10158122, Count: 1}}}
	if e := g.Inventory.Save(ctx, inv); e != nil {
		t.Fatal(e)
	}
	items, e := newPVFDungeonDropCatalog(dimensionTestSource())
	if e != nil {
		t.Fatal(e)
	}
	s := &Service{options: options{accountID: "account-1"}, pvfItemCatalog: items, repositoryProvider: func() (repo.Group, bool) { return g, true }}
	return s, &gameSession{selectedCharacterID: 19, itemListMigrationsDone: true}, g
}
func TestDimensionTransformAtomicResetAndReplay(t *testing.T) {
	s, se, g := dimensionFixture(t)
	ctx := context.Background()
	r := currentDimensionTransformRequest{Slot: 9, ItemID: 100352735, SourceSlot: 69}
	if e := s.commitCurrentDimensionTransform(ctx, se, r); e != nil {
		t.Fatal(e)
	}
	after, _, e := g.Inventory.Load(ctx, "19")
	if e != nil {
		t.Fatal(e)
	}
	a := after.Slots["0:9"]
	if a.ItemID != 100352735 && a.ItemID != 100344405 {
		t.Fatalf("invalid output %+v", a)
	}
	if a.RawEntry[10] != 0 || a.Extra["reinforce_level"] != "" || a.Extra["enchant"] != "" {
		t.Fatalf("inherited upgrades %+v", a)
	}
	if _, ok := after.Slots["0:69"]; ok {
		t.Fatal("book not consumed")
	}
	if e := s.commitCurrentDimensionTransform(ctx, se, r); e == nil {
		t.Fatal("replay admitted")
	}
	replay, _, _ := g.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(replay, after) {
		t.Fatal("replay mutated assets")
	}
}

type dimensionFailSettlement struct {
	base repo.CharacterSettlementUnitOfWork
}

func (f dimensionFailSettlement) WithinCharacterSettlement(ctx context.Context, id string, fn func(repo.Group) error) error {
	return f.base.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		if e := fn(tx); e != nil {
			return e
		}
		return errors.New("commit failure")
	})
}
func TestDimensionTransformRejectionsAndRollback(t *testing.T) {
	for _, kind := range []string{"locked", "id", "expired", "owner", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			s, se, g := dimensionFixture(t)
			ctx := context.Background()
			r := currentDimensionTransformRequest{Slot: 9, ItemID: 100352735, SourceSlot: 69}
			inv, _, _ := g.Inventory.Load(ctx, "19")
			switch kind {
			case "locked":
				a := inv.Slots["0:9"]
				a.Extra["equipment_lock_state"] = "1"
				inv.Slots["0:9"] = a
				g.Inventory.Save(ctx, inv)
			case "id":
				r.ItemID++
			case "expired":
				a := inv.Slots["0:69"]
				a.Extra = map[string]string{"expire_time": "1"}
				inv.Slots["0:69"] = a
				g.Inventory.Save(ctx, inv)
			case "owner":
				s.options.accountID = "other"
			case "rollback":
				g.CharacterSettlement = dimensionFailSettlement{g.CharacterSettlement}
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			}
			before, _, _ := g.Inventory.Load(ctx, "19")
			if e := s.commitCurrentDimensionTransform(ctx, se, r); e == nil {
				t.Fatal("admitted invalid request")
			}
			after, _, _ := g.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed transform changed inventory")
			}
		})
	}
}
func TestRealPVFDimensionTransformRule(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, e := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	items, e := newPVFDungeonDropCatalog(a)
	if e != nil {
		t.Fatal(e)
	}
	rule, e := loadCurrentDimensionTransformRule(items)
	if e != nil {
		t.Fatal(e)
	}
	if len(rule.Input) != 272 || len(rule.Output) != 272 || rule.Total != 272000 {
		t.Fatalf("rule size %d/%d total %d", len(rule.Input), len(rule.Output), rule.Total)
	}
}
func TestDimensionTransformNativeRoutes(t *testing.T) {
	m := equipmentRecipeCompoundGameplayModule()
	if m.UpperHandlers[880] == nil || m.LegacyHandlers[880] == nil {
		t.Fatal("native op880 routes missing")
	}
	if _, e := newGameplayModuleRegistry(m); e != nil {
		t.Fatal(e)
	}
}
func TestDimensionTransformStaticDeadlineAndFutureInstance(t *testing.T) {
	s, se, g := dimensionFixture(t)
	ctx := context.Background()
	source := dimensionTestSource()
	source["stackable/10158001/10158122.stk"] += "[expiration date]\n`2018-08-16 06:00:00`\n"
	items, e := newPVFDungeonDropCatalog(source)
	if e != nil {
		t.Fatal(e)
	}
	s.pvfItemCatalog = items
	r := currentDimensionTransformRequest{Slot: 9, ItemID: 100352735, SourceSlot: 69}
	before, _, _ := g.Inventory.Load(ctx, "19")
	if e = s.commitCurrentDimensionTransform(ctx, se, r); e == nil {
		t.Fatal("expired static book admitted")
	}
	after, _, _ := g.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("expiration refusal mutated inventory")
	}
	book := after.Slots["0:69"]
	book.ExpireAt = time.Now().Add(time.Hour)
	book.Extra = map[string]string{"expire_time": fmt.Sprint(book.ExpireAt.Unix())}
	after.Slots["0:69"] = book
	if e = g.Inventory.Save(ctx, after); e != nil {
		t.Fatal(e)
	}
	if e = s.commitCurrentDimensionTransform(ctx, se, r); e != nil {
		t.Fatal(e)
	}
}
func TestDimensionTransformNativeHandlerRefreshesBeforeSuccess(t *testing.T) {
	fixture, _, g := dimensionFixture(t)
	s, se, c := newGameplayDomainBridgeFixture(g)
	s.pvfItemCatalog = fixture.pvfItemCatalog
	body := []byte{0, 9, 0, 0, 0, 0, 0, 69, 0}
	binary.LittleEndian.PutUint32(body[3:], 100352735)
	if e := s.handleCurrentDimensionTransform(se, body); e != nil {
		t.Fatal(e)
	}
	packets := drainUpperPackets(t, c)
	if len(packets) < 2 {
		t.Fatalf("missing inventory refresh/ack %+v", packets)
	}
	last := packets[len(packets)-1]
	if last.msgID != 880 || !reflect.DeepEqual(last.body, []byte{1, 0, 9, 0}) {
		t.Fatalf("native success=%+v", last)
	}
	refresh := false
	for _, p := range packets[:len(packets)-1] {
		if p.msgID == 13 {
			refresh = true
		}
	}
	if !refresh {
		t.Fatal("success precedes the updated equipment list")
	}
	before, _, _ := g.Inventory.Load(context.Background(), "19")
	if e := s.handleCurrentDimensionTransform(se, body[:7]); e != nil {
		t.Fatal(e)
	}
	failed := drainUpperPackets(t, c)
	if len(failed) != 1 || failed[0].msgID != 880 || failed[0].body[0] != 0 {
		t.Fatalf("native refusal=%+v", failed)
	}
	after, _, _ := g.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("bad wire request changed assets")
	}
}
