package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestEpicProductionNativeProfileRoutes(t *testing.T) {
	for _, profile := range []string{"official-24.9", "90cn785"} {
		first := uint16(1415)
		if profile != "official-24.9" {
			first = 1417
		}
		for i, operation := range []string{"start", "change", "process", "compound", "ability"} {
			for _, transport := range []string{"legacy", "upper"} {
				t.Run(profile+"/"+operation+"/"+transport, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "events.txt")
					logger, err := openPacketLogger(path)
					if err != nil {
						t.Fatal(err)
					}
					defer logger.Close()
					conn := &bufferConn{}
					s := &Service{packetLog: logger, options: options{currentClientVersion: profile, gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
					session := &gameSession{conn: conn}
					op := first + uint16(i)
					// One byte is malformed for every operation. The event proves
					// which semantic decoder received it; ACK number alone cannot.
					if transport == "legacy" {
						err = s.handleGameCommand(session, 1, op, []byte{255})
					} else {
						_, err = currentGameplayModules.DispatchUpper(s, session, op, 1, []byte{255})
					}
					if err != nil {
						t.Fatal(err)
					}
					if conn.write.Len() == 0 {
						t.Fatal("request not routed")
					}
					ack, tail := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
					if binary.LittleEndian.Uint16(conn.write.Bytes()[1:3]) != op || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{0, 3}) || len(tail) != 0 {
						t.Fatalf("wrong failure reply: %x", conn.write.Bytes())
					}
					if err := logger.Close(); err != nil {
						t.Fatal(err)
					}
					log, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(log), "game-upper-epic-production-"+operation+"-blocked") {
						t.Fatalf("wrong semantic decoder: %s", log)
					}
				})
			}
		}
		for _, op := range []uint16{1415, 1416, 1420, 1421} {
			if op >= first && op <= first+4 {
				continue
			}
			t.Run(fmt.Sprintf("%s/not-an-epic-op/%d", profile, op), func(t *testing.T) {
				conn := &bufferConn{}
				s := &Service{options: options{currentClientVersion: profile, gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
				if err := s.handleGameCommand(&gameSession{conn: conn}, 1, op, []byte{255}); err != nil {
					t.Fatal(err)
				}
				if conn.write.Len() != 0 {
					t.Fatalf("other-version opcode generated an epic reply: %x", conn.write.Bytes())
				}
			})
		}
	}
}

func TestDoc136RealPVFNativeCapturedCompound(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	// Exact semantic request recorded at 2026-10-15T11:14:09Z. The header
	// carries no account/token data. Inventory below is an isolated fixture,
	// not a claim about the historical player's complete state.
	raw, err := hex.DecodeString("018a051f000000f654b3ec2f00ba139b000100cb349d008600000020030000")
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"legacy", "upper", "wrong-class"} {
		for _, available := range []int64{800, 799} {
			t.Run(fmt.Sprintf("%s/materials-%d", transport, available), func(t *testing.T) {
				ctx := context.Background()
				g := memory.NewMemoryGroup()
				character := dnfrepo.CharacterRecord{CharacterID: "123", AccountID: "isolated-native-replay", Level: 55, Stats: map[string]int64{currentEpicProductionCarryGroup2Stat: 0}}
				inv := dnfrepo.InventoryRecord{CharacterID: "123", Slots: map[string]dnfrepo.ItemStack{"0:134": {ItemID: 10302667, Count: available}}}
				if err := g.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				if err := g.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
				conn := &bufferConn{}
				session := &gameSession{conn: conn, selectedCharacterID: 123, accountID: character.AccountID}
				s := &Service{initialEquipmentArchive: archive, pvfItemCatalog: items, repositoryProvider: func() (dnfrepo.Group, bool) { return g, true }, options: options{currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
				if transport == "legacy" {
					err = s.handleLegacyGamePacket(session, raw)
				} else {
					class := byte(1)
					if transport == "wrong-class" {
						class = 0
					}
					var packet []byte
					packet, err = dnfproto.BuildChannelPacket(1418, raw[13:], 0, class)
					if err == nil {
						// BuildChannelPacket treats zero as its default class1.
						// Set the actual negative wire class explicitly.
						packet[0] = class
						err = s.handleGameUpper(session, packet)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				afterI, _, err := g.Inventory.Load(ctx, "123")
				if err != nil {
					t.Fatal(err)
				}
				afterC, _, err := g.Character.Load(ctx, "123")
				if err != nil {
					t.Fatal(err)
				}
				if transport == "wrong-class" {
					if conn.write.Len() != 0 || !reflect.DeepEqual(inv, afterI) || !reflect.DeepEqual(character, afterC) {
						t.Fatal("wrong class mutated assets or replied")
					}
					return
				}
				ack, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
				if binary.LittleEndian.Uint16(conn.write.Bytes()[1:3]) != 1418 || ack.Header.Classification != 1 {
					t.Fatalf("wrong reply opcode/class: %x", conn.write.Bytes())
				}
				if available == 799 {
					if len(ack.Body) != 2 || ack.Body[0] != 0 || len(rest) != 0 || !reflect.DeepEqual(inv, afterI) || !reflect.DeepEqual(character, afterC) {
						t.Fatal("short-material rejection failed")
					}
					return
				}
				if !bytes.Equal(ack.Body, []byte{1, 1, 0, 0, 0, 2, 0x88, 0x13, 0, 0}) {
					t.Fatalf("native compound reply=%x want output1/group2/carry5000", ack.Body)
				}
				if afterI.Slots["0:134"].Count != 0 || afterC.Stats[currentEpicProductionCarryGroup2Stat] != 5000 {
					t.Fatal("materials or carry not committed")
				}
				var output int64
				for _, stack := range afterI.Slots {
					if stack.ItemID == 10163130 {
						output += stack.Count
					}
				}
				if output != 1 {
					t.Fatalf("catalyst count=%d", output)
				}
				if len(rest) < 3 || binary.LittleEndian.Uint16(rest[1:3]) != 14 {
					t.Fatal("missing inventory refresh after ACK")
				}
				update, tail := splitLongHengGameServerUpperPacket(t, rest)
				if update.Header.Classification != 0 || len(tail) != 0 || len(update.Body) != 3+2*119 {
					t.Fatal("unexpected item refresh shape")
				}
			})
		}
	}
}
