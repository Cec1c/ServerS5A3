package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
)

const merchantUpgradeBridgeTable = "[building upgrade]\n[building]\n0 0 1000 0 3500 0 8000 0 15000 20 25000 200 35000 700 55000 1500 75000 3000 95000 5000\n[/building]\n[mecenary]\n0 0 1000 0 3500 0 8000 0 15000 20 25000 200 35000 700 55000 1500 75000 3000 95000 5000\n[/mecenary]\n[/building upgrade]"

func guildMerchantFixture(t *testing.T) (*Service, *gameSession, r.Group, dungeonDropCatalogTestSource) {
	t.Helper()
	s, p, repos := guildGuardianFixture(t)
	source := guardianBridgeSource(t)
	source["etc/agitwar.etc"] += "\n" + merchantUpgradeBridgeTable
	source["npc/npc.lst"] += "\n389 `mercenary.npc`\n390 `defence.npc`"
	source["npc/mercenary.npc"] = "[guild agit shop npc] `mercenary`\n[clickable distance add] 0 20"
	source["npc/defence.npc"] = "[guild agit shop npc] `defence`\n[clickable distance add] 0 20"
	source["map/guildagit/guildagit_w.map"] = "[npc]\n389 900 165 0\n390 1005 165 0\n[/npc]"
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.pvfItemCatalog = items
	s.townCatalog, err = town.Load(context.Background(), townMoveTestSource{"town/town.lst": "8 `guildagit.twn`", "town/guildagit.twn": "[area]\n0 `guildagit/guildagit_w.map` `[normal]`\n[/area]"}, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p.townPositionSnapshot.AreaID = 0
	p.townPositionSnapshot.PositionX, p.townPositionSnapshot.PositionY = 1005, 165
	actor, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	actor.Stats["area_id"] = 0
	if err := repos.Character.Save(context.Background(), actor); err != nil {
		t.Fatal(err)
	}
	return s, p, repos, source
}

func sendMerchantRequest(t *testing.T, s *Service, p *gameSession, op uint16, body []byte, seq uint16) {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(op, body, seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
		t.Fatal(err)
	}
	if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
}

func merchantUpgradeBody(npc, from, to uint32, poison byte) []byte {
	b := bytes.Repeat([]byte{poison}, 25)
	binary.LittleEndian.PutUint32(b[13:], npc)
	binary.LittleEndian.PutUint32(b[17:], from)
	binary.LittleEndian.PutUint32(b[21:], to)
	return b
}

func TestGuildMerchantUpgradeProductionAndReplay(t *testing.T) {
	for index, npc := range []uint32{390, 389} {
		t.Run(fmt.Sprint(npc), func(t *testing.T) {
			s, p, repos, _ := guildMerchantFixture(t)
			if npc == 389 {
				p.townPositionSnapshot.PositionX = 900
			}
			for step, tc := range []struct {
				from, to uint32
				seq      uint16
				prefix   byte
				want     uint32
			}{{1, 2, 40, 0xa5, 2}, {2, 3, 41, 0x5a, 3}, {1, 2, 40, 0x5a, 3}} {
				p.conn.(*bufferConn).write.Reset()
				sendMerchantRequest(t, s, p, 840, merchantUpgradeBody(npc, tc.from, tc.to, tc.prefix), tc.seq)
				if got := guildReply(t, p, 840); !bytes.Equal(got, []byte{1}) {
					t.Fatalf("step %d ACK %x", step, got)
				}
				want := make([]byte, 5)
				want[0] = byte(index)
				binary.LittleEndian.PutUint32(want[1:], tc.want)
				if got := guildReply(t, p, 778); !bytes.Equal(got, want) {
					t.Fatalf("778 %x want %x", got, want)
				}
				state := guildReply(t, p, 1297)
				if got := binary.LittleEndian.Uint16(state[0x876+index*2:]); got != uint16(tc.want) {
					t.Fatalf("1297 level %d", got)
				}
				exportEssenceBody(t, fmt.Sprintf("merchant-%d-step%d-840", npc, step), guildReply(t, p, 840))
				exportEssenceBody(t, fmt.Sprintf("merchant-%d-step%d-778", npc, step), guildReply(t, p, 778))
			}
			reg, err := guild.Load(context.Background(), repos.Settings)
			if err != nil || len(reg.MerchantUpgradeReceipts) != 2 || reg.Guilds[1].Essence.AP != 95500 {
				t.Fatalf("registry %+v %v", reg, err)
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.writeCurrentGuildEssenceSnapshot(p); err != nil {
				t.Fatal(err)
			}
			if got := binary.LittleEndian.Uint16(guildReply(t, p, 1297)[0x876+index*2:]); got != 3 {
				t.Fatal(got)
			}
		})
	}
}

func TestGuildMerchantUpgradeRejectsWithoutMutation(t *testing.T) {
	for _, kind := range []string{"short", "npc", "skip", "wrong-area", "not-ready", "wrong-town", "deleted", "no-purchase", "no-upgrade", "stale-session", "balance"} {
		t.Run(kind, func(t *testing.T) {
			s, p, repos, _ := guildMerchantFixture(t)
			body := merchantUpgradeBody(390, 1, 2, 0xa5)
			ctx := context.Background()
			reg, _ := guild.Load(ctx, repos.Settings)
			switch kind {
			case "short":
				body = body[:24]
			case "npc":
				binary.LittleEndian.PutUint32(body[13:], 1500)
			case "skip":
				binary.LittleEndian.PutUint32(body[21:], 3)
			case "wrong-area":
				p.townPositionSnapshot.AreaID = 7
			case "not-ready":
				p.townSceneReadyCharacterID = 0
			case "wrong-town":
				p.townPositionSnapshot.TownID = 1
			case "deleted":
				actor, _, _ := repos.Character.Load(ctx, "29")
				actor.Stats["delete_flag"] = 1
				if err := repos.Character.Save(ctx, actor); err != nil {
					t.Fatal(err)
				}
			case "no-purchase", "no-upgrade":
				g, m, _ := reg.GuildForCharacter("29")
				mask := guild.GradePermissionsFor(g, m.Grade)
				bit := guild.PurchasePermission
				if kind == "no-upgrade" {
					bit = guild.MerchantUpgradePermission
				}
				g.GradePermissions = map[byte]uint32{m.Grade: mask &^ bit}
			case "stale-session":
				s.gameSessions[p.selectedCharacterID] = &gameSession{}
			case "balance":
				reg.Guilds[1].Essence.AP = 999
			}
			if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			p.conn.(*bufferConn).write.Reset()
			sendMerchantRequest(t, s, p, 840, body, 40)
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected request mutated registry")
			}
			if kind != "stale-session" {
				if got := guildReply(t, p, 840); len(got) != 2 || got[0] != 0 {
					t.Fatalf("failure ACK %x", got)
				}
				exportEssenceBody(t, "merchant-upgrade-reject-"+kind+"-840", guildReply(t, p, 840))
			}
		})
	}
}

func TestGuildMerchantUpgradeRefreshesCurrentGuildPeers(t *testing.T) {
	s, p, _, _ := guildMerchantFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []uint16{30, 31, 32, 33} {
		peer := s.gameSessions[id]
		if err := s.startGameSessionEvents(ctx, peer); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.stopGameSessionEvents(stop, peer); err != nil {
				t.Error(err)
			}
		})
	}
	sendMerchantRequest(t, s, p, 840, merchantUpgradeBody(390, 1, 2, 0), 40)
	for _, id := range []uint16{30, 31, 32, 33} {
		peer := s.gameSessions[id]
		if err := s.callGameSession(ctx, peer, "merchant-refresh-drain", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if id == 30 || id == 33 {
			body := guildReply(t, peer, 1297)
			if len(body) != 0x895 || binary.LittleEndian.Uint16(body[0x876:]) != 2 {
				t.Fatalf("peer %d lacks merchant level", id)
			}
		} else if peer.conn.(*bufferConn).write.Len() != 0 {
			t.Fatalf("sent merchant state to other guild %d", id)
		}
	}
}
