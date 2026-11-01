package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestConsumableIntegrationExpansionUsesRegisteredOwner(t *testing.T) {
	// Purchase materials describe the NPC exchange, not a second use debit.
	// This request used to be intercepted by the narrower event-item owner.
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst": "", "equipment/equipment.lst": "",
		"stackable/stackable.lst": "700 `ticket.stk`\n",
		"stackable/ticket.stk":    "[stackable type] `[etc]` 0\n[minimum level] 1\n[usable job] `[all]`\n[/usable job]\n[action type] `[upgrade inventory]`\n[/action type]\n[need material] 800 2\n[daily purchase limit] `account` 1\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s, p, g := eventItemBridgeFixture(t, catalog, nil, 700)
	for _, class := range []byte{0, 2} {
		if err := s.handleGameCommand(p, class, 430, []byte{70, 0}); err != nil {
			t.Fatal(err)
		}
		bag, _, _ := g.Inventory.Load(context.Background(), "29")
		if bag.Slots["0:70"].Count != 1 {
			t.Fatalf("class %d consumed ticket", class)
		}
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleGameCommand(p, 1, 430, []byte{70, 0}); err != nil {
		t.Fatal(err)
	}
	bag, _, _ := g.Inventory.Load(context.Background(), "29")
	state, _, err := repo.LoadCharacterContainerState(context.Background(), g.Settings, "29")
	if err != nil || bag.Slots["0:70"].Count != 0 || state.MainSlotCount != 8 {
		t.Fatalf("ticket=%+v state=%+v err=%v", bag.Slots["0:70"], state, err)
	}
	ack, tail := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if ack.Header.MsgID != 430 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatalf("ACK=%+v body=%x", ack.Header, ack.Body)
	}
	for _, op := range []uint16{14, 13} {
		frame, rest := splitCurrentGameServerUpperPacketAuto(t, tail)
		tail = rest
		if frame.Header.MsgID != op {
			t.Fatalf("refresh=%d want=%d", frame.Header.MsgID, op)
		}
	}
	if len(tail) != 0 {
		t.Fatal("unexpected extra reply")
	}
}

func TestConsumableIntegrationSelectionDefaultsUseRewardRows(t *testing.T) {
	text := "[booster selection num] 2\n[booster select category] 0\n[default select] 2 700 999\n[stackable] 700 3 701 4 702 5\n[/booster select category]"
	doc, err := dnfpvf.Parse("stackable/select.stk", text)
	if err != nil {
		t.Fatal(err)
	}
	rows, categories, err := parseCurrentBoosterSelectionCatalog(doc)
	if err != nil || len(rows) != 3 || !categories[0][0].DefaultSelected || categories[0][0].Count != 3 {
		t.Fatalf("rows=%+v categories=%+v err=%v", rows, categories, err)
	}
	def := currentBoosterDefinition{SelectionRequired: 2, SelectionCategory: categories}
	for _, tc := range []struct {
		ids []uint32
		ok  bool
	}{{[]uint32{700, 701}, true}, {[]uint32{701, 702}, false}, {[]uint32{700, 999}, false}, {[]uint32{700, 700}, false}} {
		req := currentBoosterOpenRequest{Kind: currentBoosterRequestSelection}
		for _, id := range tc.ids {
			req.Selections = append(req.Selections, currentBoosterSelectionRequest{ItemID: id})
		}
		got, err := currentBoosterSelectedCandidates(def, req)
		if (err == nil) != tc.ok || (tc.ok && got[0].Count != 3) {
			t.Fatalf("ids=%v selected=%+v err=%v", tc.ids, got, err)
		}
	}
}

func TestConsumableIntegrationSelectionDuplicateBounds(t *testing.T) {
	for _, tc := range []struct {
		count       int
		rows, close string
		ok          bool
	}{
		{1, "700 3 700 3", "", true},
		{1, "700 3 700 4", "", false},
		{0, "700 3 700 3", "", false},
		{1, "700 3", "[/booster select category]\n", true},
	} {
		doc, err := dnfpvf.Parse("stackable/select.stk", fmt.Sprintf("[booster selection num] %d\n[booster select category] 0\n[stackable] %s\n[/booster select category]\n%s", tc.count, tc.rows, tc.close))
		if err != nil {
			t.Fatal(err)
		}
		_, categories, err := parseCurrentBoosterSelectionCatalog(doc)
		if (err == nil) != tc.ok || (tc.ok && len(categories[0]) != 1) {
			t.Fatalf("case=%+v categories=%+v err=%v", tc, categories, err)
		}
	}
}
