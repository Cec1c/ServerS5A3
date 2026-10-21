package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guildcompound"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestRealPVFGuildR192CatalogAndTransactions(t *testing.T) {
	archive := aikenR142Archive(t)
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rules, err := currentGuildCompoundRules(catalog, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []guildcompound.CompoundKind{guildcompound.CompoundGem, guildcompound.CompoundFlag} {
		for _, grade := range rules.Catalog.Ladder(kind).Rarities() {
			recipe, _ := rules.Catalog.Ladder(kind).Recipe(grade)
			if !recipe.Compoundable() {
				continue
			}
			pool, err := rules.Candidates(kind, grade)
			if err != nil || len(pool) == 0 {
				t.Fatalf("%s grade%d pool %v: %v", kind, grade, pool, err)
			}
			for _, success := range []bool{true, false} {
				group := memory.NewMemoryGroup()
				ctx := context.Background()
				if err := group.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
					t.Fatal(err)
				}
				inv := repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}
				request := guildcompound.Request{Count: 4, Grade: grade}
				for i := range request.MaterialRefs {
					slot, err := currentGuildCompoundGrant(catalog, &inv, pool[0])
					if err != nil {
						t.Fatal(err)
					}
					request.MaterialRefs[i] = uint32(slot)
				}
				if err := group.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
				localRules := rules
				localRules.Roll = func(upper uint32) uint32 {
					if success {
						return 0
					}
					return upper - 1
				}
				// Four grade-0 medals have a 100% rate. Use two to exercise failure.
				if !success && recipe.SuccessRateFor(4) == 10000 {
					request.Count = 2
					request.MaterialRefs[2] = 0xffffffff
					request.MaterialRefs[3] = 0xffffffff
				}
				owner, err := guildcompound.NewOwner(group)
				if err != nil {
					t.Fatal(err)
				}
				result, err := owner.Apply(ctx, guildcompound.Command{AccountID: "account-1", CharacterID: "19", Kind: kind, Request: request, Rules: localRules, Now: now})
				if err != nil {
					t.Fatalf("%s grade%d success%v: %v", kind, grade, success, err)
				}
				if result.Success != success {
					t.Fatalf("%s grade%d: %+v", kind, grade, result)
				}
				saved, _, err := group.Inventory.Load(ctx, "19")
				if err != nil {
					t.Fatal(err)
				}
				var remaining int64
				for _, stack := range saved.Slots {
					remaining += stack.Count
				}
				want := int64(4 - request.Count)
				if success {
					want++
				}
				if remaining != want {
					t.Fatalf("%s grade%d count=%d want%d", kind, grade, remaining, want)
				}
				if success {
					next, _ := rules.Catalog.Ladder(kind).NextRarity(grade)
					definition, err := rules.Resolve(result.ResultItem)
					if err != nil || definition.Rarity != next {
						t.Fatalf("result=%+v definition=%+v err=%v", result, definition, err)
					}
					if kind == guildcompound.CompoundGem && result.ResultItem != pool[0]+1 {
						t.Fatalf("same-id gem did not upgrade same attribute: %+v", result)
					}
				}
			}
		}
	}
}

func guildR192Fixture(t *testing.T, rate string) (*Service, *gameSession, repo.Group) {
	t.Helper()
	source := guildCompoundPVFTestSource()
	source["equipment/equipment.lst"] += "\n100380009 \"equipment/character/common/flag/medal_09.equ\"\n"
	source["equipment/character/common/flag/medal_09.equ"] = "[equipment type] \"[flag]\"\n[rarity] 0\n"
	source["etc/guilddungeoncompound.etc"] = "[compound info]\n0 4 " + rate + " 1 4 1500 2 4 750 3 2 0\n[/compound info]\n[guild flag compound info]\n0 4 2500 1 4 2000 2 4 1000 3 4 500 6 2 0\n[/guid flag compound info]"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	group := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := group.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	inv := repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}
	if _, err := currentGuildCompoundGrant(catalog, &inv, 90000); err != nil {
		t.Fatal(err)
	}
	stack := inv.Slots["38:49"]
	stack.Count = 4
	stack, err = currentGuildCompoundUpdateStack(stack, 49)
	if err != nil {
		t.Fatal(err)
	}
	inv.Slots["38:49"] = stack
	if err := group.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:        options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain, gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain},
		pvfItemCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return group, true },
		logger: slog.New(slog.NewTextHandler(new(bytes.Buffer), nil)),
	}
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, accountFromToken: true, itemListMigrationsDone: true}
	return service, session, group
}

func guildR192Body(count, slot uint32) []byte {
	b := make([]byte, 37)
	binary.LittleEndian.PutUint32(b[13:], count)
	for i := uint32(0); i < 4; i++ {
		ref := uint32(0xffffffff)
		if i < count {
			ref = slot
		}
		binary.LittleEndian.PutUint32(b[21+i*4:], ref)
	}
	return b
}

func TestGuildR192DispatchCommitsAndRefreshesList38(t *testing.T) {
	for _, transport := range []string{"upper", "legacy", "stream", "trailer"} {
		t.Run(transport, func(t *testing.T) {
			// This synthetic catalog makes the success branch deterministic. Production
			// odds come from the unchanged PVF table and are tested separately.
			s, g, repositories := guildR192Fixture(t, "2500")
			body := guildR192Body(4, 49)
			var err error
			if transport == "upper" {
				frame, e := proto.BuildChannelPacket(835, body, 1, 1)
				if e != nil {
					t.Fatal(e)
				}
				err = s.handleGameUpper(g, frame)
			} else if transport == "stream" || transport == "trailer" {
				if transport == "trailer" {
					body = append(body, 0, 0, 0, 0)
				}
				frame, e := proto.BuildChannelPacket(835, body, 1, 1)
				if e != nil {
					t.Fatal(e)
				}
				packets, remaining, skipped, e := proto.SplitLatestGameStream(frame, 65535)
				if e != nil || len(packets) != 1 || len(remaining) != 0 || skipped != 0 {
					t.Fatalf("stream: packets=%v remaining=%x skipped=%d err=%v", packets, remaining, skipped, e)
				}
				switch packets[0].Kind {
				case proto.LatestGameStreamLegacy:
					err = s.handleLegacyGamePacket(g, packets[0].Data)
				case proto.LatestGameStreamUpper:
					err = s.handleGameUpper(g, packets[0].Data)
				default:
					t.Fatalf("unexpected stream kind %v", packets[0].Kind)
				}
			} else {
				err = s.handleGameCommand(g, 1, 835, body)
			}
			if err != nil {
				t.Fatal(err)
			}
			inv, _, err := repositories.Inventory.Load(context.Background(), "19")
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.Slots) != 1 || inv.Slots["38:49"].ItemID != 90001 || inv.Slots["38:49"].Count != 1 {
				t.Fatalf("committed inventory: %+v", inv.Slots)
			}
			ack, rest := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
			refresh, rest := splitGameServerUpperPacket(t, rest)
			// op13 uses the client's 16-byte raw-item header; the generic
			// 13-byte splitter retains its three reserved bytes in Body.
			if ack.Header.MsgID != 835 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || refresh.Header.MsgID != 13 || refresh.Header.Classification != 0 || len(refresh.Body) != 6+currentItemListEntryWireSize || refresh.Body[3] != 38 || binary.LittleEndian.Uint16(refresh.Body[4:6]) != 1 || len(rest) != 0 {
				t.Fatalf("unexpected ack/refresh: %+v %+v rest=%x", ack, refresh, rest)
			}
		})
	}
}

type guildR192FailedWrite struct{ bufferConn }

func (*guildR192FailedWrite) Write([]byte) (int, error) {
	return 0, errors.New("injected compound ack write failure")
}

func TestGuildR192WriteFailureKeepsOneCommittedResult(t *testing.T) {
	s, g, repositories := guildR192Fixture(t, "2500")
	g.conn = &guildR192FailedWrite{}
	if err := s.handleGameCommand(g, 1, 835, guildR192Body(4, 49)); err == nil {
		t.Fatal("expected write error")
	}
	inv, _, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Slots) != 1 || inv.Slots["38:49"].ItemID != 90001 || inv.Slots["38:49"].Count != 1 {
		t.Fatalf("commit lost or duplicated: %+v", inv.Slots)
	}
}

func TestGuildR192MedalRouteUsesOwnPage(t *testing.T) {
	s, g, repositories := guildR192Fixture(t, "2500")
	inv, _, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	body := guildR192Body(4, 0)
	for i := 0; i < 4; i++ {
		slot, err := currentGuildCompoundGrant(s.pvfItemCatalog, &inv, 100380009)
		if err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint32(body[21+i*4:], uint32(slot))
	}
	if err := repositories.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(g, 1, 864, body); err != nil {
		t.Fatal(err)
	}
	after, _, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if after.Slots["38:49"].Count != 4 || after.Slots["38:0"].Count != 1 || len(after.Slots) != 2 {
		t.Fatalf("medal result: %+v", after.Slots)
	}
	result, err := currentGuildCompoundDefinition(s.pvfItemCatalog, uint32(after.Slots["38:0"].ItemID))
	if err != nil || result.Rarity != 1 || result.Family != guildcompound.MaterialFamilyGuildMedal {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	packets := splitItemGrantNoticeStream(t, s, g.conn.(*bufferConn).write.Bytes())
	if len(packets) != 2 || packets[0].msgID != 864 || !bytes.Equal(packets[0].body, []byte{1}) || packets[1].body[0] != 38 {
		t.Fatalf("packets=%+v", packets)
	}
}

func TestGuildR192InvalidRequestsDoNotConsume(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  []byte
		class byte
	}{
		{"short", []byte{0}, 1}, {"one", guildR192Body(1, 49), 1}, {"cross_page", guildR192Body(4, 0), 1}, {"wrong_class", guildR192Body(4, 49), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, g, repositories := guildR192Fixture(t, "2500")
			before, _, _ := repositories.Inventory.Load(context.Background(), "19")
			frame, err := proto.BuildChannelPacket(835, tc.body, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			frame[0] = tc.class
			if err = s.handleGameUpper(g, frame); err != nil {
				t.Fatal(err)
			}
			after, _, _ := repositories.Inventory.Load(context.Background(), "19")
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected request changed inventory")
			}
		})
	}
}
