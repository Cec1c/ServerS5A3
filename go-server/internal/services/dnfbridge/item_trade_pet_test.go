package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestPetArtifactTradeRequiresMatchingUnboundTradeableDefinition(t *testing.T) {
	for _, tc := range []struct {
		name, family, attach string
		bound, sealed, want  bool
	}{
		{"free", "[artifact red]", "[free]", false, false, true},
		{"sealed", "[artifact blue]", "[sealing]", false, true, true},
		{"opened", "[artifact green]", "[sealing]", false, false, false},
		{"bound", "[artifact red]", "[free]", true, false, false},
		{"untradeable", "[artifact red]", "[untradable]", false, false, false},
		{"body", "[creature]", "[free]", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := dungeonDropItemDefinition{ItemID: 9001, Kind: dungeonDropItemEquipment, EquipmentType: tc.family, AttachType: tc.attach}
			stack := r.ItemStack{ItemID: 9001, Count: 1, Bind: tc.bound, Extra: map[string]string{}}
			if tc.sealed {
				stack.Extra["seal_flag"] = "1"
			}
			if got := currentPetArtifactTradeAllowed(def, stack); got != tc.want {
				t.Fatalf("allowed=%t want=%t", got, tc.want)
			}
			stack.ItemID++
			if currentPetArtifactTradeAllowed(def, stack) {
				t.Fatal("accepted wrong item identity")
			}
		})
	}
}

func TestPetArtifactAuctionNativeRegisterChecksFamilyAndCommitsOnce(t *testing.T) {
	for _, family := range []string{"[artifact red]", "[artifact blue]", "[artifact green]", "[creature]"} {
		t.Run(family, func(t *testing.T) {
			ctx := context.Background()
			catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
				"monster/monster.lst": "", "stackable/stackable.lst": "",
				"equipment/equipment.lst":     "9001 `creature/test.equ`\n",
				"equipment/creature/test.equ": fmt.Sprintf("[name] `Pet`\n[equipment type] `%s`\n[attach type] `[free]`\n", family),
			})
			if err != nil {
				t.Fatal(err)
			}
			g := memory.NewMemoryGroup()
			stack := r.ItemStack{ItemID: 9001, Count: 1, RawEntry: make([]byte, currentItemListEntryWireSize)}
			seedCurrentAuctionBridgeCharacter(t, ctx, g, "10", "seller-account", "Seller", 20_000, map[string]r.ItemStack{"7:140": stack})
			s := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, repositoryProvider: func() (r.Group, bool) { return g, true }}
			seedAuctionSequenceForBridge(t, s, 100)
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 10, accountID: "seller-account"}
			request := buildCurrentAuctionRegisterRequestForTest(140, 9001, 1, -1, 100, 100, 24)
			request[1] = 7
			if err := s.registerCurrentAuctionItemLocked(session, request); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
			inv, _, err := g.Inventory.Load(ctx, "10")
			if err != nil {
				t.Fatal(err)
			}
			character, _, err := g.Character.Load(ctx, "10")
			if err != nil {
				t.Fatal(err)
			}
			if family == "[creature]" {
				if len(ack.Body) == 0 || ack.Body[0] != 0 || character.Stats["gold"] != 20_000 || !reflect.DeepEqual(inv.Slots["7:140"], stack) {
					t.Fatal("body in artifact slot was listed or mutated")
				}
				return
			}
			if len(ack.Body) == 0 || ack.Body[0] != 1 || len(inv.Slots) != 0 || character.Stats["gold"] != 10_000 {
				t.Fatalf("native register did not commit artifact and deposit: ack=%x inventory=%+v gold=%d", ack.Body, inv.Slots, character.Stats["gold"])
			}
			if err := s.registerCurrentAuctionItemLocked(session, request); err != nil {
				t.Fatal(err)
			}
			after, _, err := g.Character.Load(ctx, "10")
			if err != nil || after.Stats["gold"] != character.Stats["gold"] {
				t.Fatal("replay debited deposit again")
			}
		})
	}
}

func TestPetArtifactMarketWireRejectsOtherPetSegments(t *testing.T) {
	for _, slot := range []int16{0, 139, 140, 188, 189} {
		want := slot >= 140 && slot <= 188
		if list, ok := onlineItemTradeStorageSourceList(7, slot); ok != want || (ok && list != 7) {
			t.Fatalf("trade slot=%d list=%d accepted=%t", slot, list, ok)
		}
		body := make([]byte, currentAuctionRegisterRequestSize)
		body[1] = 7
		binary.LittleEndian.PutUint16(body[2:4], uint16(slot))
		binary.LittleEndian.PutUint32(body[4:8], 9001)
		binary.LittleEndian.PutUint32(body[8:12], 1)
		binary.LittleEndian.PutUint32(body[12:16], 100)
		binary.LittleEndian.PutUint32(body[16:20], 100)
		binary.LittleEndian.PutUint32(body[20:24], 100)
		binary.LittleEndian.PutUint32(body[24:28], 24)
		request, err := decodeCurrentAuctionRegisterRequest(body)
		if (err == nil) != want || (err == nil && request.ListType != 7) {
			t.Fatalf("auction slot=%d request=%+v err=%v", slot, request, err)
		}
	}
}
