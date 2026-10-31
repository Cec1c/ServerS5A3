package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// These are the five item lists independently observed in the current client
// and checked against the current PVF. No player inventory is accessed.
var relicArmorChoices = [][]uint32{
	{100150548, 100050667, 100200523, 100250541, 100100579},
	{100110533, 100160474, 100260514, 100210498, 100060547},
	{100120530, 100170505, 100070537, 100220505, 100270501},
	{100130499, 100180467, 100080513, 100280462, 100230467},
	{100140350, 100290341, 100090355, 100240328, 100190321},
}

func relicCompactBody(slot, category uint16, ids []uint32) []byte {
	b := make([]byte, 26)
	binary.LittleEndian.PutUint16(b[0:2], slot)
	binary.LittleEndian.PutUint16(b[2:4], category)
	for i, id := range ids {
		binary.LittleEndian.PutUint32(b[4+4*i:], id)
	}
	return b
}

func TestRelicBoxesRealPVFAndInventory(t *testing.T) {
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
	ctx := context.Background()
	for _, sourceID := range []uint32{490008265, 490008654} {
		definition, err := resolveCurrentBoosterDefinition(catalog, sourceID, currentBoosterRequestSelection)
		if err != nil {
			t.Fatalf("source %d: %v", sourceID, err)
		}
		for category := 0; category < len(definition.SelectionCategory); category++ {
			candidates := definition.SelectionCategory[uint16(category)]
			if len(candidates) == 0 {
				t.Fatalf("source %d empty category %d", sourceID, category)
			}
			var body []byte
			if sourceID == 490008265 {
				if category >= len(relicArmorChoices) {
					t.Fatalf("unexpected armor category %d", category)
				}
				body = relicCompactBody(75, uint16(category), relicArmorChoices[category])
			} else {
				body = make([]byte, 10)
				binary.LittleEndian.PutUint16(body[0:2], 75)
				binary.LittleEndian.PutUint16(body[2:4], uint16(category))
				binary.LittleEndian.PutUint32(body[4:8], candidates[0].ItemID)
			}
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:75": {ItemID: int64(sourceID), Count: 1}}}); err != nil {
				t.Fatal(err)
			}
			s := currentBoosterTestService(catalog, repos)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 19}
			request, err := parseCurrentBoosterOpenRequest(body)
			if err != nil {
				t.Fatalf("source %d category %d parse: %v", sourceID, category, err)
			}
			if err := s.handleCurrentBoosterItem(session, body); err != nil {
				t.Fatalf("source %d category %d use: %v", sourceID, category, err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			if len(ack.Body) == 0 || ack.Body[0] != 1 {
				t.Fatalf("source %d category %d failure response", sourceID, category)
			}
			bag, ok, err := repos.Inventory.Load(ctx, "19")
			if err != nil || !ok {
				t.Fatalf("load result: %v", err)
			}
			counts := map[uint32]int64{}
			for _, stack := range bag.Slots {
				counts[uint32(stack.ItemID)] += stack.Count
			}
			if counts[sourceID] != 0 {
				t.Fatal("source box remains")
			}
			for _, choice := range request.Selections {
				if counts[choice.ItemID] != 1 {
					t.Fatalf("category %d item %d count %d", category, choice.ItemID, counts[choice.ItemID])
				}
			}
			if len(counts) != len(request.Selections) {
				t.Fatalf("unexpected rewards: %v", counts)
			}
		}
	}
}

func TestRelicUpgradeMetadataRejectsUnverifiedValues(t *testing.T) {
	for _, value := range []string{"1", "", "0 0", "`0`"} {
		doc, err := dnfpvf.Parse("stackable/test.stk", "[booster select category]\n0 0\n[booster equipment upgrade]\n"+value+"\n[equipment]\n123 1\n[/equipment]\n[/booster select category]\n")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := parseCurrentBoosterSelectionCatalog(doc); err == nil {
			t.Fatalf("unverified upgrade %q accepted", value)
		}
	}
}

func TestRelicCompactSelectionRejectsForgedRequests(t *testing.T) {
	valid := relicCompactBody(75, 0, relicArmorChoices[0])
	for name, change := range map[string]func([]byte){
		"duplicate": func(b []byte) { copy(b[8:12], b[4:8]) },
		"zero":      func(b []byte) { clear(b[4:8]) },
		"tail":      func(b []byte) { b[24] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), valid...)
			change(b)
			if _, err := parseCurrentBoosterOpenRequest(b); err == nil {
				t.Fatal("accepted malformed input")
			}
		})
	}
	req, err := parseCurrentBoosterOpenRequest(valid)
	if err != nil {
		t.Fatal(err)
	}
	category := make([]currentBoosterSelectionCandidate, 0, 5)
	for _, id := range relicArmorChoices[0] {
		category = append(category, currentBoosterSelectionCandidate{ItemID: id, Count: 1})
	}
	def := currentBoosterDefinition{Source: dungeonDropItemDefinition{ItemID: 490008265}, SelectionCategory: map[uint16][]currentBoosterSelectionCandidate{0: category}}
	if _, err := currentBoosterSelectedCandidates(def, req); err != nil {
		t.Fatal(err)
	}
	other := def
	other.Source.ItemID = 490008654
	if _, err := currentBoosterSelectedCandidates(other, req); err == nil {
		t.Fatal("compact format accepted for other box")
	}
	wrong := req
	wrong.SelectionContext = 1
	if _, err := currentBoosterSelectedCandidates(def, wrong); err == nil {
		t.Fatal("wrong category accepted")
	}
	wrong = req
	wrong.Selections = append([]currentBoosterSelectionRequest(nil), req.Selections...)
	wrong.Selections[0].ItemID++
	if _, err := currentBoosterSelectedCandidates(def, wrong); err == nil {
		t.Fatal("forged item accepted")
	}
}
