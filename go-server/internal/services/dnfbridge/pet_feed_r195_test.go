package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func r195PetFeedFixture(t *testing.T, realPVF bool) (*Service, *gameSession, *creatorTutorialFailConn, repo.Group) {
	t.Helper()
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	var catalog *dnfpet.PVFCatalog
	var err error
	itemID := int64(63000)
	if realPVF {
		path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
		if path == "" {
			t.Skip("runtime PVF required")
		}
		a, e := platformpvf.LoadArchive(platformpvf.Options{Path: path})
		if e != nil {
			t.Fatal(e)
		}
		catalog, err = dnfpet.NewPVFCatalog(a)
		itemID = 400990170
	} else {
		source := bridgePetCatalogFixture()
		source["stackable/stackable.lst"] = "24 `cash/creature/food.stk`\n"
		source["stackable/cash/creature/food.stk"] = "[stackable type] `[feed]`\n[int data] 300 -20 30\n[/int data]\n"
		catalog, err = dnfpet.NewPVFCatalog(source)
	}
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 119)
	binary.LittleEndian.PutUint16(raw, 189)
	binary.LittleEndian.PutUint32(raw[2:6], 24)
	binary.LittleEndian.PutUint32(raw[6:10], 2)
	for _, e := range []error{
		r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1"}),
		r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"7:189": {ItemID: 24, Count: 2, RawEntry: raw, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[feed]", "raw_entry_hex": hex.EncodeToString(raw), "amount_or_count": "2"}}}}),
		r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "19", Entries: map[string]repo.EquipmentEntry{"26": {SlotIndex: 26, ItemID: itemID, RawEntry: testCurrentEquippedCreatureRaw(26, uint32(itemID), 37)}}}),
		r.Pet.Save(ctx, repo.PetRecord{CharacterID: "19", EquippedKey: "37", Entries: map[string]repo.PetEntry{"37": {PetKey: "37", CreatureKey: 37, ItemID: itemID, SourceListType: 3, SourceSlotIndex: 26, Satiety: 5, SatietyMicros: 5_000_000, Level: 1, NameRaw: []byte("Pet")}}}),
	} {
		if e != nil {
			t.Fatal(e)
		}
	}
	conn := &creatorTutorialFailConn{err: errors.New("injected pet feed socket failure")}
	s := &Service{options: options{accountID: "dnf:1", currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, petCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	p := &gameSession{conn: conn, connID: "r195-pet-feed", selectedCharacterID: 19}
	return s, p, conn, r
}

type r195PetFeedWire struct {
	Opcode uint16 `json:"opcode"`
	Class  byte   `json:"class"`
	Body   []byte `json:"body"`
}

func r195ReadPetFeedWire(t *testing.T, b []byte) []r195PetFeedWire {
	t.Helper()
	var out []r195PetFeedWire
	for len(b) > 0 {
		p, rest := splitLongHengGameServerUpperPacket(t, b)
		out = append(out, r195PetFeedWire{p.Header.MsgID, p.Header.Classification, append([]byte(nil), p.Body...)})
		b = rest
	}
	return out
}

func TestR195PetFeedPublicCommandCommitsAndProjects(t *testing.T) {
	s, p, c, r := r195PetFeedFixture(t, false)
	for _, after := range []byte{35, 65} {
		c.write.Reset()
		if err := s.handleGameCommand(p, 1, 44, buildRejectedUseStackableBody(189, 7, 10764, 24)); err != nil {
			t.Fatal(err)
		}
		wire := r195ReadPetFeedWire(t, c.write.Bytes())
		if len(wire) != 3 || wire[0].Opcode != 44 || wire[0].Class != 1 || !bytes.Equal(wire[0].Body, []byte{1, 189, 0, 7, 12, 42, 0, 0, 24, 0, 0, 0}) || wire[1].Opcode != 103 || wire[1].Class != 0 || !bytes.Equal(wire[1].Body, []byte{37, 0, 0, 0, after, 0, 0, 0}) || wire[2].Opcode != 14 || wire[2].Class != 0 {
			t.Fatalf("wire=%+v", wire)
		}
		pet, _, _ := r.Pet.Load(context.Background(), "19")
		if pet.Entries["37"].Satiety != after {
			t.Fatal("satiety missing")
		}
	}
	c.write.Reset()
	if err := s.handleGameCommand(p, 1, 44, buildRejectedUseStackableBody(189, 7, 10764, 24)); err != nil {
		t.Fatal(err)
	}
	wire := r195ReadPetFeedWire(t, c.write.Bytes())
	if len(wire) != 1 || wire[0].Opcode != 44 || !bytes.Equal(wire[0].Body, []byte{0, 13, 7, 12, 42, 0, 0, 24, 0, 0, 0}) {
		t.Fatalf("empty source wire=%+v", wire)
	}
}

func TestR195PetFeedPostCommitWriteFailuresDoNotRepeatDebit(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			s, p, c, r := r195PetFeedFixture(t, false)
			c.failAt = failAt
			if err := s.handleGameCommand(p, 1, 44, buildRejectedUseStackableBody(189, 7, 10764, 24)); err == nil {
				t.Fatal("write failure hidden")
			}
			inv, _, _ := r.Inventory.Load(context.Background(), "19")
			pet, _, _ := r.Pet.Load(context.Background(), "19")
			if inv.Slots["7:189"].Count != 1 || pet.Entries["37"].Satiety != 35 {
				t.Fatal("committed mutation repeated or rolled back")
			}
			if got := r195ReadPetFeedWire(t, c.write.Bytes()); len(got) != failAt-1 {
				t.Fatalf("projection jumped failed write: %+v", got)
			}
		})
	}
}

func TestR195PetFeedWrongOwnerAndOtherListRemainRejected(t *testing.T) {
	for _, wrongOwner := range []bool{true, false} {
		s, p, c, r := r195PetFeedFixture(t, false)
		list := byte(7)
		if wrongOwner {
			s.options.accountID = "dnf:other"
		} else {
			list = 0
		}
		if err := s.handleGameCommand(p, 1, 44, buildRejectedUseStackableBody(189, list, 10764, 24)); err != nil {
			t.Fatal(err)
		}
		inv, _, _ := r.Inventory.Load(context.Background(), "19")
		pet, _, _ := r.Pet.Load(context.Background(), "19")
		if inv.Slots["7:189"].Count != 2 || pet.Entries["37"].Satiety != 5 {
			t.Fatal("invalid request mutated pet/feed")
		}
		wire := r195ReadPetFeedWire(t, c.write.Bytes())
		if len(wire) != 1 || wire[0].Opcode != 44 || wire[0].Body[0] != 0 {
			t.Fatalf("rejection=%+v", wire)
		}
	}
}

func TestR195PetFeedRealPVFAndNativeExport(t *testing.T) {
	s, p, c, r := r195PetFeedFixture(t, true)
	if err := s.handleGameCommand(p, 1, 44, buildRejectedUseStackableBody(189, 7, 10764, 24)); err != nil {
		t.Fatal(err)
	}
	inv, _, _ := r.Inventory.Load(context.Background(), "19")
	pet, _, _ := r.Pet.Load(context.Background(), "19")
	if inv.Slots["7:189"].Count != 1 || pet.Entries["37"].Satiety != 35 || inv.Slots["7:189"].Extra["amount_or_count"] != "1" {
		t.Fatal("real-PVF feed did not commit")
	}
	wire := r195ReadPetFeedWire(t, c.write.Bytes())
	if len(wire) != 3 || wire[0].Opcode != 44 || wire[1].Opcode != 103 || wire[2].Opcode != 14 {
		t.Fatalf("real-PVF wire=%+v", wire)
	}
	if directory := os.Getenv("DNF90_PET_FEED_FIXTURE_DIR"); directory != "" {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
		b, err := json.MarshalIndent(wire, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "pet-feed-wire.json"), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
