package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func eventItemBridgeFixture(t *testing.T, catalog *pvfDungeonDropCatalog, archive *platformpvf.Archive, sourceID int64) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, p, g := guildMileageFixture(t)
	s.pvfItemCatalog, s.initialEquipmentArchive = catalog, archive
	ctx := context.Background()
	c, _, err := g.Character.Load(ctx, "29")
	if err != nil {
		t.Fatal(err)
	}
	c.Level = 90
	for _, err := range []error{
		g.Account.Save(ctx, repo.AccountRecord{AccountID: c.AccountID}),
		g.Character.Save(ctx, c),
		g.Settings.Save(ctx, newCharacterContainerStateSettings("29", s.gameplayNow())),
		g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "29", Slots: map[string]repo.ItemStack{"0:70": {ItemID: sourceID, Count: 1, Extra: map[string]string{"item_kind": "stackable"}}, "0:10": {ItemID: 404000036, Count: 1, ExpireAt: s.gameplayNow().Add(24 * time.Hour), Extra: map[string]string{"item_kind": "equipment", "upgrade_level": "12", "forge_level": "7"}}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s, p, g
}

func eventItemBody(op uint16) []byte {
	if op != 515 {
		return []byte{70, 0}
	}
	b := testQuestClearCouponRequestBody(70, 88)
	binary.LittleEndian.PutUint32(b[15:], 10)
	return b
}

func TestEventItemNativeEnvelopes(t *testing.T) {
	for _, op := range []uint16{160, 430, 515} {
		body := eventItemBody(op)
		if _, slot, _, err := parseCurrentEventItemRequest(op, body); err != nil || slot != 70 {
			t.Fatalf("%d: %v", op, err)
		}
		for _, bad := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), 0)} {
			if _, _, _, err := parseCurrentEventItemRequest(op, bad); err == nil {
				t.Fatalf("%d accepted size %d", op, len(bad))
			}
		}
	}
	for _, offset := range []int{1, 2, 3, 7, 11, 17, 18} {
		body := eventItemBody(515)
		body[offset] = 255
		if _, _, _, err := parseCurrentEventItemRequest(515, body); err == nil {
			t.Fatalf("bad offset %d accepted", offset)
		}
	}
	legacy, ok := currentGameplayModules.NormalizeLegacy(430, []byte{70, 0, 1, 2, 3, 4})
	if !ok || !bytes.Equal(legacy, []byte{70, 0}) {
		t.Fatalf("legacy=%x ok=%v", legacy, ok)
	}
}

func TestEventItemsRealPVFDispatchAndDurableEffects(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DNFBRIDGE_REAL_PVF_SMOKE", "") // The shared fixture uses synthetic scene data.
	for _, tt := range []struct {
		name string
		op   uint16
		item int64
		kind inventory.EventItemKind
	}{
		{"inventory_old", 430, 490008261, inventory.EventInventoryExpansion},
		{"inventory_level_event", 430, 490009309, inventory.EventInventoryExpansion},
		{"silver_cargo", 160, 490008262, inventory.EventSilverCargo},
		{"silver_cargo_level_event", 160, 490009310, inventory.EventSilverCargo},
		{"permanent_growth_weapon", 515, 490009319, inventory.EventPermanentWeapon},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, p, g := eventItemBridgeFixture(t, catalog, a, tt.item)
			body := eventItemBody(tt.op)
			var handled bool
			var err error
			if tt.op == 515 {
				handled, err = s.handleAlignedGameCommand(p, 1, tt.op, body)
			} else {
				handled, err = currentGameplayModules.DispatchUpper(s, p, tt.op, 1, body)
			}
			if err != nil || !handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			ctx := context.Background()
			bag, _, _ := g.Inventory.Load(ctx, "29")
			if bag.Slots["0:70"].Count != 0 {
				t.Fatal("source not consumed")
			}
			state, _, err := repo.LoadCharacterContainerState(ctx, g.Settings, "29")
			if err != nil {
				t.Fatal(err)
			}
			wantOps := []uint16{}
			switch tt.kind {
			case inventory.EventInventoryExpansion:
				if state.MainSlotCount != 8 {
					t.Fatalf("%+v", state)
				}
				wantOps = []uint16{430, 14, 13}
			case inventory.EventSilverCargo:
				if state.PersonalCargoSlotCount != 40 {
					t.Fatalf("%+v", state)
				}
				wantOps = []uint16{160, 66, 14}
			case inventory.EventPermanentWeapon:
				w := bag.Slots["0:10"]
				if w.ItemID != 404000022 || !w.ExpireAt.IsZero() || w.Extra["upgrade_level"] != "12" || w.Extra["forge_level"] != "7" {
					t.Fatalf("%+v", w)
				}
				wantOps = []uint16{515, 14, 14}
			}
			var ops []uint16
			raw := p.conn.(*bufferConn).write.Bytes()
			for len(raw) > 0 {
				frame, next := splitCurrentGameServerUpperPacketAuto(t, raw)
				raw = next
				ops = append(ops, frame.Header.MsgID)
				if frame.Header.MsgID == 430 && (frame.Header.Classification != 1 || !bytes.Equal(frame.Body, []byte{1})) {
					t.Fatalf("inventory upgrade ACK class=%d body=%x", frame.Header.Classification, frame.Body)
				}
				if frame.Header.MsgID == 515 && !bytes.Equal(frame.Body, []byte{1, 70, 0, 0, 88, 0, 0, 0}) {
					t.Fatalf("action88 ACK %x", frame.Body)
				}
				if frame.Header.MsgID == 66 && !bytes.Equal(frame.Body, []byte{1, 0, 40, 0}) {
					t.Fatalf("cargo notice %x", frame.Body)
				}
				if tt.op == 515 && len(ops) == 2 {
					var removed currentItemListEntry
					removed.patchCore(10, 0xffffffff, 0)
					if !bytes.Equal(frame.Body, buildCurrentItemUpdateBody(0, []currentItemListEntry{removed})) {
						t.Fatal("old weapon object was not removed before replacement")
					}
				}
			}
			if !reflect.DeepEqual(ops, wantOps) {
				t.Fatalf("ops=%v want=%v", ops, wantOps)
			}
			stable := repo.CloneInventory(bag)
			if err := s.handleCurrentEventItem(p, tt.op, body); err != nil {
				t.Fatal(err)
			}
			bag, _, _ = g.Inventory.Load(ctx, "29")
			if !reflect.DeepEqual(stable, bag) {
				t.Fatal("retry changed committed inventory")
			}
		})
	}
}

func TestEventItemRealPVFRejectsCrossUse(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		kind           inventory.EventItemKind
		source, target int64
	}{
		{inventory.EventInventoryExpansion, 490009319, 0}, {inventory.EventSilverCargo, 490009309, 0}, {inventory.EventPermanentWeapon, 490009319, 404000022}, {inventory.EventPermanentWeapon, 490009319, 100150548},
	} {
		_, _, _, err := currentEventItemPolicy(catalog, tt.kind, repo.ItemStack{ItemID: tt.source, Count: 1}, repo.ItemStack{ItemID: tt.target, Count: 1, ExpireAt: now.Add(time.Hour)}, now, "[seria room]")
		if err == nil {
			t.Fatalf("accepted cross-use %+v", tt)
		}
	}
}

type eventItemSendFailure struct{ bufferConn }

func (*eventItemSendFailure) Write([]byte) (int, error) {
	return 0, errors.New("event item response disconnected")
}

func TestEventItemRealPVFWeaponPairsAndSendFailure(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	d, err := catalog.ResolveItem(490009319)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDungeonCardPVFDocument(a, d.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	pairs := doc.Ints("stackable action pair indexes")
	if len(pairs) != 74 {
		t.Fatalf("pair count=%d", len(pairs)/2)
	}
	for i := 0; i < len(pairs); i += 2 {
		p, _, _, err := currentEventItemPolicy(catalog, inventory.EventPermanentWeapon, repo.ItemStack{ItemID: 490009319, Count: 1}, repo.ItemStack{ItemID: pairs[i], Count: 1, ExpireAt: now.Add(time.Hour)}, now, "[seria room]")
		if err != nil || p.OutputItemID != pairs[i+1] {
			t.Fatalf("pair %d -> %d: %+v %v", pairs[i], pairs[i+1], p, err)
		}
	}
	t.Setenv("DNFBRIDGE_REAL_PVF_SMOKE", "")
	for _, tt := range []struct {
		op   uint16
		item int64
	}{{430, 490009309}, {160, 490008262}, {515, 490009319}} {
		s, p, g := eventItemBridgeFixture(t, catalog, a, tt.item)
		p.conn = &eventItemSendFailure{}
		if err := s.handleCurrentEventItem(p, tt.op, eventItemBody(tt.op)); err == nil {
			t.Fatal("send failure not returned")
		}
		bag, _, _ := g.Inventory.Load(context.Background(), "29")
		if bag.Slots["0:70"].Count != 0 {
			t.Fatal("commit was undone after send failure")
		}
		p.conn = &bufferConn{}
		if err := s.handleCurrentEventItem(p, tt.op, eventItemBody(tt.op)); err != nil {
			t.Fatal(err)
		}
		after, _, _ := g.Inventory.Load(context.Background(), "29")
		if !reflect.DeepEqual(bag, after) {
			t.Fatal("response retry duplicated item use")
		}
	}
}
