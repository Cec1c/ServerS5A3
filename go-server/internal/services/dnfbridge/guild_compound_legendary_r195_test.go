package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guildcompound"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestRealPVFGuildLegendaryMedalTwoToOne(t *testing.T) {
	// Opt-in through the existing real-PVF fixture; an explicit bad resource
	// must fail there. This test never substitutes a synthetic legendary pool.
	catalog, err := newPVFDungeonDropCatalog(aikenR142Archive(t))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := currentGuildCompoundRules(catalog, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	recipe, found := rules.Catalog.Ladder(guildcompound.CompoundFlag).Recipe(6)
	if !found || recipe.MaxMaterialCount != 2 || recipe.RatePerMaterial != 0 {
		t.Fatalf("actual terminal recipe=%+v found=%t", recipe, found)
	}
	if grade, ok := rules.Catalog.ResultRarity(guildcompound.CompoundFlag, 6, 2); !ok || grade != 6 {
		t.Fatalf("actual legendary two-to-one policy=%d/%t", grade, ok)
	}
	pool, err := rules.Candidates(guildcompound.CompoundFlag, 6)
	if err != nil || len(pool) < 2 {
		t.Fatalf("actual legendary candidates=%v err=%v", pool, err)
	}
	poolSet := map[uint32]bool{}
	for _, id := range pool {
		def, err := rules.Resolve(id)
		if err != nil || def.Family != guildcompound.MaterialFamilyGuildMedal || def.Rarity != 6 {
			t.Fatalf("candidate=%d def=%+v err=%v", id, def, err)
		}
		poolSet[id] = true
	}
	for _, transport := range []string{"upper", "legacy"} {
		for _, inputs := range []string{"different_templates", "same_template_distinct_instances"} {
			t.Run(transport+"/"+inputs, func(t *testing.T) {
				ctx := context.Background()
				group := memory.NewMemoryGroup()
				if err := group.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
					t.Fatal(err)
				}
				inv := repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}
				ids := []uint32{pool[0], pool[1]}
				if inputs == "same_template_distinct_instances" {
					ids[1] = ids[0]
				}
				body := make([]byte, guildcompound.RequestBodyLen)
				binary.LittleEndian.PutUint32(body[13:17], 2)
				binary.LittleEndian.PutUint32(body[17:21], 6)
				for i := 0; i < 4; i++ {
					binary.LittleEndian.PutUint32(body[21+i*4:], guildcompound.AbsentMaterialRef)
				}
				for i, id := range ids {
					slot, err := currentGuildCompoundGrant(catalog, &inv, id)
					if err != nil {
						t.Fatal(err)
					}
					binary.LittleEndian.PutUint32(body[21+i*4:], uint32(slot))
				}
				if len(inv.Slots) != 2 {
					t.Fatal("materials must be two distinct medal instances")
				}
				if err := group.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
				s := &Service{
					options:        options{accountID: "account-1", currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain, gameUpperClientBodyCodec: gameUpperClientBodyCodecPlain},
					pvfItemCatalog: catalog, repositoryProvider: func() (repo.Group, bool) { return group, true },
					logger: slog.New(slog.NewTextHandler(new(bytes.Buffer), nil)),
				}
				g := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19, accountFromToken: true, itemListMigrationsDone: true}
				dispatch := func() error {
					if transport == "upper" {
						frame, err := proto.BuildChannelPacket(864, body, 1, 1)
						if err != nil {
							return err
						}
						return s.handleGameUpper(g, frame)
					}
					raw := make([]byte, proto.LegacyGameHeaderSize+len(body))
					raw[0] = 1
					binary.LittleEndian.PutUint16(raw[1:3], 864)
					binary.LittleEndian.PutUint32(raw[3:7], uint32(len(raw)))
					binary.LittleEndian.PutUint16(raw[11:13], 1)
					copy(raw[proto.LegacyGameHeaderSize:], body)
					return s.handleLegacyGamePacket(g, raw)
				}
				if err := dispatch(); err != nil {
					t.Fatal(err)
				}
				after, found, err := group.Inventory.Load(ctx, "19")
				if err != nil || !found || len(after.Slots) != 1 {
					t.Fatalf("durable result=%+v err=%v", after, err)
				}
				var product uint32
				for _, stack := range after.Slots {
					product = uint32(stack.ItemID)
					if stack.Count != 1 || !poolSet[product] {
						t.Fatalf("product=%+v not one actual legendary medal", stack)
					}
				}
				ack, rest := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
				refresh, rest := splitGameServerUpperPacket(t, rest)
				if ack.Header.MsgID != 864 || ack.Header.Classification != 1 || len(ack.Body) != 5 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:5]) != product {
					t.Fatalf("success must remain u8 + committed u32: %+v body=%x", ack.Header, ack.Body)
				}
				if refresh.Header.MsgID != 13 || refresh.Header.Classification != 0 || len(refresh.Body) != 6+currentItemListEntryWireSize || refresh.Body[3] != 38 || binary.LittleEndian.Uint16(refresh.Body[4:6]) != 1 || len(rest) != 0 {
					t.Fatalf("expected one authoritative list38 row after ACK: %+v body=%x", refresh.Header, refresh.Body)
				}
				row := refresh.Body[6:]
				if binary.LittleEndian.Uint16(row[:2]) != 0 || binary.LittleEndian.Uint32(row[2:6]) != product || binary.LittleEndian.Uint32(row[6:10]) != 1 {
					t.Fatalf("list38 product row=%x", row)
				}
				g.conn.(*bufferConn).write.Reset()
				if err := dispatch(); err != nil {
					t.Fatal(err)
				}
				repeated, _, err := group.Inventory.Load(ctx, "19")
				if err != nil || !reflect.DeepEqual(after, repeated) {
					t.Fatal("repeated stale material refs changed assets")
				}
				failed, _ := splitGameServerUpperPacket(t, g.conn.(*bufferConn).write.Bytes())
				if failed.Header.MsgID != 864 || !bytes.Equal(failed.Body, []byte{0, 1}) {
					t.Fatalf("repeat ACK=%x", failed.Body)
				}
			})
		}
	}
}
