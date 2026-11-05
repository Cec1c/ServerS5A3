package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type weddingRoomNativeCase struct {
	Name            string `json:"name"`
	Payload         string `json:"payload_hex"`
	FromBag         bool   `json:"from_bag"`
	FromSlot        uint16 `json:"from_slot"`
	SourceItem      int64  `json:"source_item"`
	ToBag           bool   `json:"to_bag"`
	ToSlot          uint16 `json:"to_slot"`
	DestinationHint int64  `json:"destination_hint"`
}

func (f weddingRoomNativeCase) body(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func weddingRoomNativeCases(t *testing.T) []weddingRoomNativeCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/wedding_room_native_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SHA   string                  `json:"client_sha256"`
		Cases []weddingRoomNativeCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SHA != "a59db943c5f44c82e7db0e02305b3403aac3a16d0ec025b396e97da491a42231" || len(fixture.Cases) != 4 {
		t.Fatal("wrong native sender fixture")
	}
	return fixture.Cases
}

func TestWeddingRoomNativeRequestsCommitPlacementRemovalAndBothProjections(t *testing.T) {
	for _, f := range weddingRoomNativeCases(t) {
		t.Run(f.Name, func(t *testing.T) { weddingRoomNativeMoveFlow(t, f, false, false, false) })
	}
	f := weddingRoomNativeCases(t)[0]
	t.Run("replace-installed", func(t *testing.T) { weddingRoomNativeMoveFlow(t, f, true, false, false) })
	t.Run("stale-source", func(t *testing.T) { weddingRoomNativeMoveFlow(t, f, false, true, false) })
	t.Run("closed-editor", func(t *testing.T) { weddingRoomNativeMoveFlow(t, f, false, false, true) })
}

func weddingRoomNativeMoveFlow(t *testing.T, f weddingRoomNativeCase, replace, stale, closed bool) {
	t.Helper()
	s, actor, peer, scope := weddingRoomLeaseFixture(t)
	g, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("missing repositories")
	}
	ctx := context.Background()
	// The town-only fixture replaces Character with a loose fake store.
	// Asset transactions must share the same coherent repository group.
	seed := g
	g = memory.NewMemoryGroup()
	for _, id := range []string{"29", "30"} {
		c, found, err := seed.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatal("fixture character", id, err)
		}
		if err := r.SaveCharacterFields(ctx, g.Character, c, append(r.AllCharacterFields(), r.CharacterFieldWedding)...); err != nil {
			t.Fatal(err)
		}
		e, found, err := seed.Equipment.Load(ctx, id)
		if err != nil || !found {
			t.Fatal("fixture equipment", id, err)
		}
		if err := g.Equipment.Save(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	s.repositoryProvider = func() (r.Group, bool) { return g, true }
	s.wedding.rules.Furniture = map[int64]wedding.Furniture{
		10002685: {ItemID: 10002685, Points: 10, Positions: map[uint16]bool{0: true, 27: true}},
		10002797: {ItemID: 10002797, Points: 20, Positions: map[uint16]bool{0: true, 27: true}},
	}
	room := wedding.Room{Version: 1, Bag: map[uint16]r.ItemStack{}, Placed: map[uint16]r.ItemStack{}}
	source := r.ItemStack{ItemID: f.SourceItem, Count: 1, Extra: map[string]string{"identity": "moving-furniture"}}
	if stale {
		source.ItemID = 10002797
	}
	if f.FromBag {
		room.Bag[f.FromSlot] = source
	} else {
		room.Placed[f.FromSlot] = source
	}
	old := r.ItemStack{ItemID: 10002797, Count: 1, Extra: map[string]string{"identity": "replaced-furniture"}}
	if replace {
		room.Placed[f.ToSlot] = old
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		t.Fatal(err)
	}
	points, err := owner.RoomPoints(room)
	if err != nil {
		t.Fatal(err)
	}
	beforeChars := map[string]r.CharacterRecord{}
	for _, id := range []string{"29", "30"} {
		c, found, err := g.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatal("character", id, err)
		}
		c.Stats["wedding_v1_room_points"] = points
		if err := r.SaveCharacterFields(ctx, g.Character, c, r.CharacterFieldWedding); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		beforeChars[id] = c
	}
	raw, err := json.Marshal(room)
	if err != nil {
		t.Fatal(err)
	}
	before := r.SettingsRecord{Scope: scope, Values: map[string]string{"room": string(raw)}}
	if err := g.Settings.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	if handled, err := currentGameplayModules.DispatchUpper(s, actor, 1045, 1, weddingRoomEditorNativeBody(t, "open-button")); err != nil || !handled {
		t.Fatal(err)
	}
	if got := guildReply(t, actor, 1045); !bytes.Equal(got, []byte{1, 0, 0, 0, 0, 0}) {
		t.Fatalf("open ACK %x", got)
	}
	if closed {
		if handled, err := currentGameplayModules.DispatchUpper(s, actor, 1045, 1, weddingRoomEditorNativeBody(t, "close")); err != nil || !handled {
			t.Fatal("close dispatch", handled, err)
		}
		if _, exists := s.wedding.roomEditors[scope]; exists {
			t.Fatal("native close retained editor before move")
		}
	}
	for _, p := range []*gameSession{actor, peer} {
		p.conn.(*bufferConn).write.Reset()
	}
	handled, err := currentGameplayModules.DispatchUpper(s, actor, 1046, 1, f.body(t))
	if err != nil || !handled {
		t.Fatalf("dispatch %v %v", handled, err)
	}
	ack := guildReply(t, actor, 1046)
	if stale || closed {
		if bytes.Equal(ack, []byte{1, 0, 0, 0, 0}) {
			t.Fatal("stale source or closed editor was accepted")
		}
		after, _, err := g.Settings.Load(ctx, scope)
		if err != nil || !reflect.DeepEqual(before.Values, after.Values) {
			t.Fatal("rejected move changed room", err)
		}
		return
	}
	if !bytes.Equal(ack, []byte{1, 0, 0, 0, 0}) {
		t.Fatalf("move ACK %x", ack)
	}
	a, _, _ := g.Character.Load(ctx, "29")
	b, _, _ := g.Character.Load(ctx, "30")
	after, err := owner.ReadRoom(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	from, to := after.Placed, after.Placed
	if f.FromBag {
		from = after.Bag
	}
	if f.ToBag {
		to = after.Bag
	}
	if !reflect.DeepEqual(to[f.ToSlot], source) {
		t.Fatalf("destination item identity lost: %+v", after)
	}
	if replace {
		if !reflect.DeepEqual(from[f.FromSlot], old) {
			t.Fatal("replaced item was lost")
		}
	} else if _, found := from[f.FromSlot]; found {
		t.Fatal("source duplicated")
	}
	wantPoints, err := owner.RoomPoints(after)
	if err != nil {
		t.Fatal(err)
	}
	x, y, err := wedding.ValidatePair(a, b)
	if err != nil || x.RoomPoints != wantPoints || y.RoomPoints != wantPoints {
		t.Fatalf("shared bonus %v/%v %v", x, y, err)
	}
	for _, c := range []r.CharacterRecord{a, b} {
		if c.Stats["gold"] != beforeChars[c.CharacterID].Stats["gold"] || c.Stats["wedding_v1_love_points"] != beforeChars[c.CharacterID].Stats["wedding_v1_love_points"] {
			t.Fatal("move debited a wallet")
		}
	}
	for _, p := range []*gameSession{actor, peer} {
		stream := p.conn.(*bufferConn).write.Bytes()
		seen := map[uint16]int{}
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			stream = rest
			if packet.Header.MsgID != 1008 && packet.Header.MsgID != 1009 && packet.Header.MsgID != 1011 {
				continue
			}
			if packet.Header.Classification != 0 {
				t.Fatal("wrong room projection class")
			}
			seen[packet.Header.MsgID]++
			record := map[string]any{"name": f.Name, "replace": replace, "character": p.selectedCharacterID, "classification": 0, "opcode": packet.Header.MsgID, "payload_hex": hex.EncodeToString(packet.Body)}
			evidence, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("ROOM_NATIVE_PACKET %s", evidence)
		}
		for _, opcode := range []uint16{1008, 1009, 1011} {
			if seen[opcode] != 1 {
				t.Fatalf("char=%d opcode=%d count=%d", p.selectedCharacterID, opcode, seen[opcode])
			}
		}
	}
}
