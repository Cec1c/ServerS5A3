package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func epicEquipmentRecipeSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst":    "",
		dungeonDropStackableList: "10163132 `catalyst.stk`\n10099774 `soul.stk`\n",
		dungeonDropEquipmentList: "700 `weapon.equ`\n",
		"stackable/catalyst.stk": "[stackable type] `[material]`\n[stack limit] 1000\n",
		"stackable/soul.stk":     "[stackable type] `[material]`\n[stack limit] 1000\n",
		"equipment/weapon.equ":   "[equipment type] `[weapon]`\n[minimum level] 85\n[rarity] 3\n[durability] 50\n[attach type] `[sealing]`\n",
	}
}

func epicEquipmentRecipeCatalog() *currentEpicProductionCatalog {
	return &currentEpicProductionCatalog{maxItemCountMaxRate: 1000000, catalysts: map[uint32]currentEpicProductionCatalyst{
		10163132: {ItemID: 10163132, GroupKey: 0, LevelLimit: 85, NeedItemType: "equipment", NeedItemParts: "weapon", NeedItemGrade: "unique", NeedItemMinLevel: 70, NeedItemMaxLevel: 85, NeedCount: 1, MustNeedItemID: 10099774, MustNeedItemCount: 1, MinMakeCount: 3, MaxMakeCount: 4, MaxItemCountRate: 300000, IsSeal: true},
	}}
}

func TestEpicEquipmentRecipePredicates(t *testing.T) {
	for _, tc := range []struct {
		name, definition string
		count            uint32
		want             bool
	}{
		{"sealed85", "[equipment type] `[weapon]`\n[minimum level] 85\n[rarity] 3\n[attach type] `[sealing]`", 1, true},
		{"sealed70", "[equipment type] `[weapon]`\n[minimum level] 70\n[rarity] 3\n[attach type] `[sealing]`", 1, true},
		{"low_level", "[equipment type] `[weapon]`\n[minimum level] 65\n[rarity] 3\n[attach type] `[sealing]`", 1, false},
		{"high_level", "[equipment type] `[weapon]`\n[minimum level] 90\n[rarity] 3\n[attach type] `[sealing]`", 1, false},
		{"wrong_rarity", "[equipment type] `[weapon]`\n[minimum level] 85\n[rarity] 4\n[attach type] `[sealing]`", 1, false},
		{"armor", "[equipment type] `[coat]`\n[minimum level] 85\n[rarity] 3\n[attach type] `[sealing]`", 1, false},
		{"untradeable", "[equipment type] `[weapon]`\n[minimum level] 85\n[rarity] 3\n[attach type] `[trade impossible]`", 1, false},
		{"missing_level", "[equipment type] `[weapon]`\n[rarity] 3\n[attach type] `[sealing]`", 1, false},
		{"missing_rarity", "[equipment type] `[weapon]`\n[minimum level] 85\n[attach type] `[sealing]`", 1, false},
		{"count_two", "[equipment type] `[weapon]`\n[minimum level] 85\n[rarity] 3\n[attach type] `[sealing]`", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := epicEquipmentRecipeSource()
			source["equipment/weapon.equ"] = tc.definition
			items, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			request := currentEpicProductionCompoundRequest{CatalystItemID: 10163132, Materials: []currentEpicProductionCompoundMaterial{{ItemID: 700, Slot: 10, Count: tc.count}}}
			plan, err := validateCurrentEpicProductionCompoundPlan(source, items, epicEquipmentRecipeCatalog(), request)
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%t want=%t err=%v", err == nil, tc.want, err)
			}
			if tc.want && plan.Contribution != 1 {
				t.Fatalf("contribution=%d", plan.Contribution)
			}
		})
	}
}

func epicEquipmentRecipeCommit(t *testing.T, source dnfpvf.Source, catalog *currentEpicProductionCatalog, inputID uint32) {
	t.Helper()
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	request := currentEpicProductionCompoundRequest{CatalystItemID: 10163132, Materials: []currentEpicProductionCompoundMaterial{{ItemID: inputID, Slot: 10, Count: 1}}}
	plan, err := validateCurrentEpicProductionCompoundPlan(source, items, catalog, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"sealed", "previously_unsealed", "batch", "missing_soul", "locked", "foreign_owner", "late_failure"} {
		t.Run(kind, func(t *testing.T) {
			currentPlan, currentRequest, units := plan, request, uint32(1)
			if kind == "sealed" {
				currentPlan.Recipe.MaxItemCountRate = currentPlan.MaxItemCountMaxRate
			}
			if kind == "previously_unsealed" {
				currentPlan.Recipe.MaxItemCountRate = 0
			}
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			character := dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "equipment-account", Level: 90}
			stack := dnfrepo.ItemStack{ItemID: int64(inputID), Count: 1, Extra: map[string]string{"seal_flag": "1"}}
			if kind == "previously_unsealed" {
				stack.Extra = map[string]string{"seal_removed_by_first_equip": "1", "trade_locked_by_first_equip": "1"}
			}
			if kind == "locked" {
				stack.Extra["equipment_lock_state"] = "locked"
			}
			inv := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:10": stack}}
			if kind == "batch" {
				units = 2
				inv.Slots["0:11"] = stack
				currentRequest.Materials = append(append([]currentEpicProductionCompoundMaterial(nil), request.Materials...), currentEpicProductionCompoundMaterial{ItemID: inputID, Slot: 11, Count: 1})
				currentPlan, err = validateCurrentEpicProductionCompoundPlan(source, items, catalog, currentRequest)
				if err != nil {
					t.Fatal(err)
				}
			}
			account := dnfrepo.AccountInventoryRecord{AccountID: character.AccountID, Slots: map[string]dnfrepo.ItemStack{dnfrepo.AccountSharedInventorySlotKey(363): {ItemID: 10099774, Count: 10}}}
			if kind == "missing_soul" {
				account.Slots = map[string]dnfrepo.ItemStack{}
			}
			for _, err := range []error{g.Character.Save(ctx, character), g.Inventory.Save(ctx, inv), g.AccountInventory.Save(ctx, account)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return g, true }}
			if kind == "late_failure" {
				failing := g
				failing.AccountAssets = epicBatchLateFailure{g.AccountAssets}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return failing, true }
			}
			session := &gameSession{accountID: character.AccountID, selectedCharacterID: 1}
			if kind == "foreign_owner" {
				session.accountID = "other-account"
			}
			beforeI, _, _ := g.Inventory.Load(ctx, "1")
			beforeA, _, _ := g.AccountInventory.Load(ctx, character.AccountID)
			result, err := s.commitCurrentEpicProductionCompound(ctx, session, currentPlan, currentRequest, time.Now().UTC())
			afterI, _, _ := g.Inventory.Load(ctx, "1")
			afterA, _, _ := g.AccountInventory.Load(ctx, character.AccountID)
			if kind == "missing_soul" || kind == "locked" || kind == "foreign_owner" || kind == "late_failure" {
				if err == nil || !reflect.DeepEqual(beforeI, afterI) || !reflect.DeepEqual(beforeA, afterA) {
					t.Fatalf("failed recipe changed assets err=%v", err)
				}
				if kind == "late_failure" && !errors.Is(err, errEpicBatchLateFailure) {
					t.Fatalf("late failure not reached: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.OutputCount < 3*units || result.OutputCount > 4*units || afterI.Slots["0:10"].Count != 0 || afterI.Slots["0:11"].Count != 0 || afterA.Slots[dnfrepo.AccountSharedInventorySlotKey(363)].Count != 10-int64(units) {
				t.Fatalf("result=%+v inventory=%+v account=%+v", result, afterI.Slots, afterA.Slots)
			}
			if (kind == "sealed" && result.OutputCount != 4) || (kind == "previously_unsealed" && result.OutputCount != 3) {
				t.Fatalf("yield rule not applied: %d", result.OutputCount)
			}
			var saved int64
			for _, stack := range afterI.Slots {
				if stack.ItemID == 10163132 {
					saved += stack.Count
				}
			}
			if saved != int64(result.OutputCount) {
				t.Fatalf("saved=%d output=%d", saved, result.OutputCount)
			}
			body := buildCurrentEpicProductionCompoundSuccessBody(result)
			if len(body) != 9 || binary.LittleEndian.Uint32(body[:4]) != result.OutputCount {
				t.Fatalf("result body=%x", body)
			}
			if _, err := s.commitCurrentEpicProductionCompound(ctx, session, currentPlan, currentRequest, time.Now().UTC()); err == nil {
				t.Fatal("replayed spent weapon accepted")
			}
		})
	}
}

func TestEpicEquipmentRecipeTransaction(t *testing.T) {
	epicEquipmentRecipeCommit(t, epicEquipmentRecipeSource(), epicEquipmentRecipeCatalog(), 700)
}

func TestEpicEquipmentRecipeOutputProbability(t *testing.T) {
	recipe := epicEquipmentRecipeCatalog().catalysts[10163132]
	plan := currentEpicProductionCompoundPlan{Recipe: recipe, MaxItemCountMaxRate: 1000000}
	rolls := []uint32{299999, 300000, 999999}
	calls := 0
	output, err := currentEpicProductionCompoundOutputCount(plan, 3, func(limit uint32) (uint32, error) {
		if limit != 1000000 || calls >= len(rolls) {
			t.Fatal("unexpected draw")
		}
		value := rolls[calls]
		calls++
		return value, nil
	})
	if err != nil || output != 10 || calls != 3 {
		t.Fatalf("output=%d draws=%d err=%v", output, calls, err)
	}
	plan.Recipe.MaxItemCountRate = 0
	if output, err = currentEpicProductionCompoundOutputCount(plan, 3, nil); err != nil || output != 9 {
		t.Fatalf("zero rate output=%d err=%v", output, err)
	}
	plan.Recipe.MaxItemCountRate = 1000000
	if output, err = currentEpicProductionCompoundOutputCount(plan, 3, nil); err != nil || output != 12 {
		t.Fatalf("full rate output=%d err=%v", output, err)
	}
	plan.Recipe.MaxItemCountRate = 300000
	failure := errors.New("entropy unavailable")
	if _, err = currentEpicProductionCompoundOutputCount(plan, 1, func(uint32) (uint32, error) { return 0, failure }); !errors.Is(err, failure) {
		t.Fatalf("entropy err=%v", err)
	}
	for _, kind := range []string{"zero_scale", "excess_rate", "excess_units", "bad_draw"} {
		bad := plan
		units := uint64(1)
		switch kind {
		case "zero_scale":
			bad.MaxItemCountMaxRate = 0
		case "excess_rate":
			bad.Recipe.MaxItemCountRate = 1000001
		case "excess_units":
			units = 65
		}
		if _, err := currentEpicProductionCompoundOutputCount(bad, units, func(n uint32) (uint32, error) { return n, nil }); err == nil {
			t.Fatalf("accepted %s", kind)
		}
	}
}

func TestEpicEquipmentRecipeRealPVF(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for matching equipment recipe")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseCurrentEpicProductionCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	r := c.catalysts[10163132]
	if r.NeedItemType != "equipment" || r.NeedItemParts != "weapon" || r.NeedItemGrade != "unique" || r.NeedItemMinLevel != 70 || r.NeedItemMaxLevel != 85 || !r.IsSeal || r.MinMakeCount != 3 || r.MaxMakeCount != 4 || r.MaxItemCountRate != 300000 || c.maxItemCountMaxRate != 1000000 {
		t.Fatalf("recipe differs: %+v", r)
	}
	items, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int{}
	for id, ref := range items.itemRefs {
		if ref.kind == dungeonDropItemEquipment {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	for _, rawID := range ids {
		id := uint32(rawID)
		ref := items.itemRefs[id]
		text, e := a.ReadText("equipment/" + ref.path)
		if e != nil {
			continue
		}
		d, e := dnfpvf.Parse(ref.path, text)
		if e != nil {
			continue
		}
		rarity, _ := d.Int("rarity")
		level, _ := d.Int("minimum level")
		part, _ := d.Text("equipment type")
		attach, _ := d.Text("attach type")
		if rarity != 3 || level < 70 || level > 85 || part != "[weapon]" || attach != "[sealing]" {
			continue
		}
		t.Logf("matching PVF weapon=%d level=%d path=%s", id, level, ref.path)
		epicEquipmentRecipeCommit(t, a, c, id)
		epicEquipmentRecipeHandler(t, a, id)
		return
	}
	t.Fatal(fmt.Sprintf("no matching weapon among %d equipment definitions", len(ids)))
}

func epicEquipmentRecipeHandler(t *testing.T, archive *platformpvf.Archive, weapon uint32) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	for _, err := range []error{
		g.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "handler-account", Level: 90}),
		g.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{"0:10": {ItemID: int64(weapon), Count: 1, Extra: map[string]string{"seal_flag": "1"}}}}),
		g.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "handler-account", Slots: map[string]dnfrepo.ItemStack{dnfrepo.AccountSharedInventorySlotKey(363): {ItemID: 10099774, Count: 10}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	conn := &bufferConn{}
	s := &Service{initialEquipmentArchive: archive, repositoryProvider: func() (dnfrepo.Group, bool) { return g, true }}
	session := &gameSession{accountID: "handler-account", selectedCharacterID: 1, conn: conn}
	body := make([]byte, 18)
	binary.LittleEndian.PutUint32(body, 10163132)
	binary.LittleEndian.PutUint16(body[4:], 1)
	binary.LittleEndian.PutUint32(body[6:], weapon)
	binary.LittleEndian.PutUint32(body[10:], 10)
	binary.LittleEndian.PutUint32(body[14:], 1)
	if err := s.handleCurrentEpicProductionCompound(session, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 1420 || ack.Header.Classification != 1 || len(ack.Body) != 10 || ack.Body[0] != 1 {
		t.Fatalf("ack=%+v body=%x", ack.Header, ack.Body)
	}
	output := binary.LittleEndian.Uint32(ack.Body[1:5])
	if output < 3 || output > 4 {
		t.Fatalf("wire output=%d", output)
	}
	update, rest := splitGameServerUpperPacket(t, rest)
	if update.Header.MsgID != 14 || update.Header.Classification != 0 || len(rest) != 0 {
		t.Fatalf("refresh=%+v extra=%d", update.Header, len(rest))
	}
	inv, _, _ := g.Inventory.Load(ctx, "1")
	account, _, _ := g.AccountInventory.Load(ctx, session.accountID)
	var saved int64
	for _, stack := range inv.Slots {
		if stack.ItemID == 10163132 {
			saved += stack.Count
		}
	}
	if saved != int64(output) || account.Slots[dnfrepo.AccountSharedInventorySlotKey(363)].Count != 9 {
		t.Fatal("wire and saved settlement differ")
	}
	conn.write.Reset()
	if err := s.handleCurrentEpicProductionCompound(session, body); err != nil {
		t.Fatal(err)
	}
	ack, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 1420 || len(ack.Body) != 2 || ack.Body[0] != 0 || len(rest) != 0 {
		t.Fatalf("spent input reply=%x extra=%d", ack.Body, len(rest))
	}
}
