package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/wedding"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func weddingCancelNPCFixture(t *testing.T) (*Service, []*gameSession, r.Group) {
	t.Helper()
	s, peers, _, repos := weddingClearPartyFixture(t)
	catalog, e := town.Load(context.Background(), s.initialEquipmentArchive, town.Options{})
	if e != nil {
		t.Fatal(e)
	}
	s.townCatalog = catalog
	items, e := newPVFDungeonDropCatalog(s.initialEquipmentArchive)
	if e != nil {
		t.Fatal(e)
	}
	s.pvfItemCatalog = items
	var priestTown, priestArea byte
	found := false
search:
	for _, location := range catalog.Towns() {
		if location.ID < 0 || location.ID > 255 {
			continue
		}
		for _, area := range location.Areas {
			if area.ID < 0 || area.ID > 255 {
				continue
			}
			m, e := worldmap.LoadTownPlacementMap(s.initialEquipmentArchive, area.MapPath)
			if e != nil {
				continue
			}
			for _, npc := range m.NPCs {
				if npc.NPCID == 20013 {
					priestTown, priestArea, found = byte(location.ID), byte(area.ID), true
					break search
				}
			}
		}
	}
	if !found {
		t.Fatal("matching PVF priest unavailable")
	}
	t.Logf("matching PVF priest town=%d area=%d", priestTown, priestArea)
	s.options.currentClientVersion = "official-24.9"
	s.onlinePlayers = newOnlinePlayerManager()
	for i, p := range peers {
		p.dungeon.runtime = nil
		p.townSceneReadyCharacterID = p.selectedCharacterID
		p.channel = channelcatalog.Channel{ID: 10}
		p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: p.selectedCharacterID, TownID: priestTown, AreaID: priestArea, PositionX: 600, PositionY: 250, PositionValid: true}
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: p.selectedCharacterID, Session: p, TownID: priestTown, AreaID: priestArea, PositionX: 600, PositionY: 250})
		id := fmt.Sprint(p.selectedCharacterID)
		c, _, _ := repos.Character.Load(context.Background(), id)
		c.Stats["gold"] = []int64{900000, 1000000}[i]
		if e := r.SaveCharacterFields(context.Background(), repos.Character, c, r.CharacterFieldStats); e != nil {
			t.Fatal(e)
		}
		p.conn.(*bufferConn).write.Reset()
		identity, bound := s.boundGameSessionCharacterSnapshot(p)
		if !bound {
			t.Fatal("fixture actor not bound")
		}
		if err := s.validateUpgradeNPCScene(identity, 20013); err != nil {
			t.Fatalf("fixture not at ready priest: %v", err)
		}
	}
	return s, peers, repos
}

func TestWeddingCurrentPVFCancellationFromNPCUsesRealPartnerAndOfflineSupport(t *testing.T) {
	for _, upper := range []bool{false, true} {
		for _, offline := range []bool{false, true} {
			t.Run(fmt.Sprintf("upper=%t/offline=%t", upper, offline), func(t *testing.T) {
				s, p, repos := weddingCancelNPCFixture(t)
				logs := captureWeddingDiagnostics(s)
				if offline {
					delete(s.gameSessions, p[1].selectedCharacterID)
				}
				body := make([]byte, 20)
				body[17] = 5
				// Reuse this real-PVF pair to prove early scene rejection remains
				// observable without packet logging and changes neither partner.
				beforeScene := captureWeddingCancellationState(t, repos, p)
				readyCharacter := p[0].townSceneReadyCharacterID
				p[0].townSceneReadyCharacterID = 0
				sendCargoWire(t, s, p[0], upper, 1032, 0, body)
				earlyOps, earlyBodies := cargoReplies(t, p[0])
				if len(earlyOps) != 1 || earlyOps[0] != 1032 || int32(binary.LittleEndian.Uint32(earlyBodies[0][1:])) != -1 {
					t.Fatalf("scene rejection ACK %v %x", earlyOps, earlyBodies)
				}
				assertWeddingCancellationDiagnostic(t, logs, "npc_scene", -1, true)
				if !reflect.DeepEqual(beforeScene, captureWeddingCancellationState(t, repos, p)) {
					t.Fatal("scene rejection changed either persisted partner")
				}
				p[0].townSceneReadyCharacterID = readyCharacter
				logs.Reset()
				sendCargoWire(t, s, p[0], upper, 1032, 1, body)
				assertWeddingCancellationDiagnostic(t, logs, "committed", 0, true)
				ops, bodies := cargoReplies(t, p[0])
				if len(ops) < 2 || ops[0] != 1032 || len(bodies[0]) != 12 || bodies[0][0] != 1 || binary.LittleEndian.Uint32(bodies[0][1:]) != 0 || bodies[0][9] != 5 {
					t.Fatalf("cancel ACK %v %x", ops, bodies)
				}
				selfCleared := false
				wallet := false
				noticeIndex, infoIndex := -1, -1
				for i, op := range ops {
					if op == 1002 {
						if len(bodies[i]) != 1 || bodies[i][0] != 5 {
							t.Fatalf("cancel notice shape %x", bodies[i])
						}
						noticeIndex = i
					}
					if op == 1000 {
						infoIndex = i
						selfCleared = len(bodies[i]) == 15 && binary.LittleEndian.Uint32(bodies[i]) == 0 && bodies[i][4] == 0
					}
					if op == 14 {
						wallet = true
					}
				}
				if !selfCleared || !wallet {
					t.Fatalf("missing owned state/wallet %v %x", ops, bodies)
				}
				if noticeIndex < 0 || noticeIndex >= infoIndex {
					t.Fatalf("native clear must precede own info %v", ops)
				}
				otherOps, otherBodies := cargoReplies(t, p[1])
				if offline && len(otherOps) != 0 {
					t.Fatal("offline partner received packets")
				}
				if !offline {
					cleared := false
					noticeIndex, infoIndex = -1, -1
					for i, op := range otherOps {
						if op == 1002 {
							if len(otherBodies[i]) != 1 || otherBodies[i][0] != 5 {
								t.Fatal("partner notice shape")
							}
							noticeIndex = i
						}
						if op == 1000 {
							infoIndex = i
							cleared = len(otherBodies[i]) == 15 && binary.LittleEndian.Uint32(otherBodies[i]) == 0 && otherBodies[i][4] == 0
						}
					}
					if !cleared {
						t.Fatalf("partner did not clear %v %x", otherOps, otherBodies)
					}
					if noticeIndex < 0 || noticeIndex >= infoIndex {
						t.Fatalf("partner native clear must precede info %v", otherOps)
					}
				}
				for i, peer := range p {
					c, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
					state, e := wedding.Read(c)
					q, _, _ := repos.Quest.Load(context.Background(), c.CharacterID)
					if e != nil || state.Phase != wedding.Single || state.PartnerID != 0 || len(q.States) != 0 {
						t.Fatalf("pair not cleared: %+v %+v %v", state, q, e)
					}
					if c.Stats["gold"] != []int64{400000, 1000000}[i] {
						t.Fatal("wrong wallet charge")
					}
				}
				before, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(p[0].selectedCharacterID))
				logs.Reset()
				sendCargoWire(t, s, p[0], upper, 1032, 2, body)
				assertWeddingCancellationDiagnostic(t, logs, "state", -1, true)
				ops, bodies = cargoReplies(t, p[0])
				if len(ops) != 1 || ops[0] != 1032 || int32(binary.LittleEndian.Uint32(bodies[0][1:])) != -1 {
					t.Fatalf("repeat ACK %v %x", ops, bodies)
				}
				after, _, _ := repos.Character.Load(context.Background(), before.CharacterID)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("repeat cancel changed balance")
				}
			})
		}
	}
}

func TestWeddingCurrentPVFCancellationRejectsLowGoldAndMalformedTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target uint32
		gold   int64
		want   int32
	}{{"low-gold", 0, 499999, -7}, {"client-chosen-partner", 1, 900000, -1}} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, repos := weddingCancelNPCFixture(t)
			logs := captureWeddingDiagnostics(s)
			id := fmt.Sprint(p[0].selectedCharacterID)
			c, _, _ := repos.Character.Load(context.Background(), id)
			c.Stats["gold"] = tc.gold
			_ = r.SaveCharacterFields(context.Background(), repos.Character, c, r.CharacterFieldStats)
			before := captureWeddingCancellationState(t, repos, p)
			body := make([]byte, 20)
			body[17] = 5
			binary.LittleEndian.PutUint32(body[13:], tc.target)
			sendCargoWire(t, s, p[0], false, 1032, 1, body)
			ops, bodies := cargoReplies(t, p[0])
			if len(ops) != 1 || ops[0] != 1032 || int32(binary.LittleEndian.Uint32(bodies[0][1:])) != tc.want {
				t.Fatalf("failure ACK %v %x", ops, bodies)
			}
			reason := "malformed_fields"
			if tc.name == "low-gold" {
				reason = "gold"
			}
			assertWeddingCancellationDiagnostic(t, logs, reason, tc.want, true)
			if !reflect.DeepEqual(before, captureWeddingCancellationState(t, repos, p)) {
				t.Fatal("failed cancel changed persisted state")
			}
		})
	}
}
