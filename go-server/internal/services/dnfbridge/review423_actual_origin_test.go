package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Actual town resources; memory repository/session/transport only. The portal
// and empty 1423 are typed handler fixtures, not emitted native-client proof.
func TestReview423ActualActivityOriginRoute(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("fixed actual PVF required")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"live28_keeps_normal40", "cold28_keeps_normal40", "live50_rejects_dirty28", "reselect50_return_rejects_dirty28", "ordinary40_unchanged", "live50_keeps_normal40"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(currentTownSeriaExitDoorEnv, "")
			t.Setenv(currentLoginPersistedTownEnv, "")
			t.Setenv(currentLoginSeriaEntryTownEnv, "")
			town := int64(28)
			if strings.Contains(name, "50") {
				town = 50
			}
			if name == "ordinary40_unchanged" {
				town = 40
			}
			s, session, repos := seriaRoomEntryTownService(t, town, 0, 441, 207)
			s.options.currentClientVersion = "official-24.9"
			s.townCatalog = table
			s.initialEquipmentArchive = a
			s.seriaRoomDoorMapReader = nil
			origin := currentTownPositionSnapshot{CharacterID: 29, TownID: 40, AreaID: 0, PositionX: 1500, PositionY: 500, MovementCode: 5, PositionValid: true}
			dirty := strings.Contains(name, "dirty28")
			if dirty {
				origin.TownID = 28
				origin.PositionX = 441
				origin.PositionY = 207
			}
			c, found, err := repos.Character.Load(context.Background(), "29")
			if err != nil || !found {
				t.Fatal("fixture unavailable")
			}
			// Legal resource level for the pre-existing event location; no live
			// admission/quest completion is asserted by this memory fixture.
			c.Level = 90
			currentTownPrevVillageApplyOriginStats(c.Stats, origin)
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if name == "cold28_keeps_normal40" {
				session.townPositionSnapshot = currentTownPositionSnapshot{}
				_, target, area, _, path, err := s.currentCharacterListLoginTransition(context.Background(), session, 29, c)
				if err != nil || target != 38 || area != 1 || !townMapPathLooksLikeSeria(path) {
					t.Fatalf("cold scene=%d/%d err=%v", target, area, err)
				}
			} else {
				if err := s.handleTownSetUserArea(session, buildTownSeriaRoomPortalRequest(38, 1, 450, 234, 5, uint16(town))); err != nil {
					t.Fatal(err)
				}
				c, _, err = repos.Character.Load(context.Background(), "29")
				if err != nil || c.Stats["town_id"] != 38 || c.Stats["area_id"] != 1 {
					t.Fatal("fixture portal did not commit Seria")
				}
			}
			if name == "reselect50_return_rejects_dirty28" {
				// New same-character session using only actual stored Stats; no borrowed
				// live-room origin. This is the login location producer, not full auth.
				old := session
				session = &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, accountID: old.accountID, channel: old.channel}
				if _, _, _, _, _, err := s.currentCharacterListLoginTransition(context.Background(), session, 29, c); err != nil {
					t.Fatal(err)
				}
				session.townSceneReadyCharacterID = 29
			}
			c, _, err = repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			wantPrev := int64(40)
			if dirty {
				wantPrev = 28
			}
			if c.Stats[currentTownPrevVillageTownStat] != wantPrev {
				t.Errorf("activity overwrote durable normal origin: got=%d want=%d", c.Stats[currentTownPrevVillageTownStat], wantPrev)
			}
			if dirty {
				if session.townSeriaRoomEntrySnapshot.PositionValid {
					t.Errorf("dirty activity granted room-entry authority: town=%d", session.townSeriaRoomEntrySnapshot.TownID)
				}
			} else if session.townSeriaRoomEntrySnapshot.TownID != 40 || !session.townSeriaRoomEntrySnapshot.PositionValid {
				t.Errorf("normal room-entry authority lost: town=%d", session.townSeriaRoomEntrySnapshot.TownID)
			}
			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			if err := s.handleCurrentPrevVillage(session, nil); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() == 0 {
				t.Fatal("bottom-door route dropped all return output")
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			door, ok, reason := s.currentSeriaRoomReturnDoorLocation(38, 1, 40, 0)
			if dirty {
				door, ok, reason = s.currentSeriaRoomOuterDoorLocation(38, 1)
			}
			if !ok {
				t.Fatal(reason)
			}
			want, _ := buildCurrentSceneTransitionBody(door.TownID, door.AreaID, []currentSceneTransitionRow{{ObjectOrResourceKey: 29, Value1: door.PositionX, Value2: door.PositionY, Value3: 5, Value4: 3}})
			if packet.Header.MsgID != 24 || len(rest) != 0 || !bytes.Equal(packet.Body, want) {
				t.Errorf("bottom door wrong town: got=%d/%d want=%d/%d", packet.Body[0], packet.Body[1], door.TownID, door.AreaID)
			}
			stored, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil || stored.Stats["town_id"] != int64(door.TownID) || stored.Stats["area_id"] != int64(door.AreaID) {
				t.Errorf("exit saved wrong destination town=%d want=%d", stored.Stats["town_id"], door.TownID)
			}
		})
	}
}

func TestReview423Actual28IdentityBoundaries(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("fixed actual PVF required")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := table.Find(28)
	if !ok {
		t.Fatal("actual town28 missing")
	}
	for _, name := range []string{"actual", "case_slashes", "id", "town_path", "street_path", "room_path", "street_gate", "room_gate", "room_position", "area_id", "duplicate_area", "extra_area"} {
		t.Run(name, func(t *testing.T) {
			v := actual
			v.Areas = append([]dnftown.Area(nil), actual.Areas...)
			switch name {
			case "case_slashes":
				v.Path = "TOWN\\EVENT_HENDON.TWN"
				v.Areas[0].MapPath = "EVENT\\16_EPICROAD_TOWN\\16_EPICROAD_TOWN.MAP"
				v.Areas[1].MapPath = "EVENT\\16_EPICROAD_TOWN\\(R)GATE_16_EPICROAD_TOWN.MAP"
			case "id":
				v.ID = 29
			case "town_path":
				v.Path = "town/ordinary.twn"
			case "street_path":
				v.Areas[0].MapPath = "town/ordinary.map"
			case "room_path":
				v.Areas[1].MapPath = "town/ordinary_room.map"
			case "street_gate":
				gate := int64(36)
				v.Areas[0].DungeonGate = &gate
			case "room_gate":
				v.Areas[1].Gate = nil
			case "room_position":
				gate := *v.Areas[1].Gate
				gate.X++
				v.Areas[1].Gate = &gate
			case "area_id":
				v.Areas[0].ID = 2
			case "duplicate_area":
				v.Areas[1] = v.Areas[0]
			case "extra_area":
				v.Areas = append(v.Areas, v.Areas[0])
			}
			if got := currentTownIsEpicRoadReturnOrigin(v); got != (name == "actual" || name == "case_slashes") {
				t.Fatalf("identity exception=%t case=%s", got, name)
			}
		})
	}
	for _, id := range []int64{38, 40} {
		v, ok := table.Find(id)
		if !ok || currentTownIsEpicRoadReturnOrigin(v) {
			t.Fatalf("ordinary town%d misclassified", id)
		}
	}
}
