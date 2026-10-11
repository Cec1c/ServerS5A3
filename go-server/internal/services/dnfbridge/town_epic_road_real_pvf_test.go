package dnfbridge

import (
	"context"
	"os"
	"testing"

	"bytes"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"strings"
)

func TestEpic39ActualTownIdentity(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires the explicit actual PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{38, 50, 107} {
		town, ok := table.Find(id)
		if !ok {
			t.Fatalf("town %d missing", id)
		}
		t.Logf("town=%+v", town)
		text, err := a.ReadText(town.Path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s:\n%s", town.Path, text)
		for _, area := range town.Areas {
			if id == 38 && area.ID != 1 {
				continue
			}
			for _, candidate := range []string{"map/" + area.MapPath, area.MapPath} {
				text, err := a.ReadText(candidate)
				if err == nil {
					t.Logf("%s:\n%s", candidate, text)
					break
				}
			}
		}
	}
	for _, p := range []string{"map/common/(r)gate_seria.map", "npc/event_npc_soryu_2018.npc", "npc/npc.lst"} {
		text, err := a.ReadText(p)
		if err != nil {
			t.Fatal(err)
		}
		if p != "npc/npc.lst" {
			t.Logf("%s:\n%s", p, text)
		}
		if p == "npc/npc.lst" {
			doc, err := dnfpvf.Parse(p, text)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range dnfpvf.ParseList(doc) {
				if entry.ID != 3136 && entry.ID != 375 && entry.ID != 20276 && entry.ID != 20275 {
					continue
				}
				r := "npc/" + strings.TrimPrefix(entry.Path, "npc/")
				text, err := a.ReadText(r)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("npc %d %s:\n%s", entry.ID, r, text)
			}
		}
	}
}

func TestEpic39RealPVFReturnOrigin(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires the explicit actual PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	epic, ok := table.Find(50)
	if !ok || !strings.EqualFold(epic.Path, "town/2018_Epicroad2.twn") || epic.DisableSavePoint {
		t.Fatalf("unexpected actual epic town: %+v", epic)
	}
	for _, name := range []string{"cold_login_preserves_normal_origin", "live_room_entry_preserves_normal_origin", "epic_is_not_persistable", "dirty_entry_snapshot_rejected"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(currentTownSeriaExitDoorEnv, "")
			t.Setenv(currentLoginPersistedTownEnv, "")
			t.Setenv(currentLoginSeriaEntryTownEnv, "")
			s, session, repos := seriaRoomEntryTownService(t, 50, 0, 441, 207)
			s.options.currentClientVersion = "official-24.9"
			s.initialEquipmentArchive = a
			s.townCatalog = table
			s.seriaRoomDoorMapReader = nil
			origin := currentTownPositionSnapshot{CharacterID: 29, TownID: 40, AreaID: 0, PositionX: 1500, PositionY: 500, MovementCode: 5, PositionValid: true}
			epicOrigin := session.townPositionSnapshot
			c, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			currentTownPrevVillageApplyOriginStats(c.Stats, origin)
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "epic_is_not_persistable":
				if _, ok := s.currentTownPrevVillageOriginIsPersistable(epicOrigin); ok {
					t.Fatal("actual epic town50 incorrectly admitted as durable ordinary return origin")
				}
				return
			case "dirty_entry_snapshot_rejected":
				if _, ok, reason := s.currentTownSeriaRoomEntryOriginFrom(29, epicOrigin); ok {
					t.Fatalf("dirty event entry accepted reason=%q", reason)
				}
				return
			case "cold_login_preserves_normal_origin":
				// Fresh character selection after logging out in epic town50:
				// all session snapshots are gone; only the character's five normal-origin fields remain.
				session.townPositionSnapshot = currentTownPositionSnapshot{}
				session.townPrevVillageSnapshot = currentTownPositionSnapshot{}
				_, town, area, _, mapPath, err := s.currentCharacterListLoginTransition(context.Background(), session, 29, c)
				if err != nil || town != 38 || area != 1 || !townMapPathLooksLikeSeria(mapPath) {
					t.Fatalf("login town=%d area=%d map=%s err=%v", town, area, mapPath, err)
				}
			default:
				if err := s.handleTownSetUserArea(session, buildTownSeriaRoomPortalRequest(38, 1, 450, 234, 5, 50)); err != nil {
					t.Fatal(err)
				}
			}
			stored, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil || stored.Stats[currentTownPrevVillageTownStat] != 40 || session.townSeriaRoomEntrySnapshot != origin {
				t.Errorf("ordinary origin overwritten: persisted=%+v entry=%+v err=%v", stored.Stats, session.townSeriaRoomEntrySnapshot, err)
			}
			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			if err := s.handleCurrentPrevVillage(session, nil); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			// The door location is resolved from the actual West Coast map, not the saved position.
			door, found, reason := s.currentSeriaRoomReturnDoorLocation(38, 1, 40, 0)
			if !found {
				t.Fatalf("actual West Coast door missing: %s", reason)
			}
			want, _ := buildCurrentSceneTransitionBody(40, door.AreaID, []currentSceneTransitionRow{{ObjectOrResourceKey: 29, Value1: door.PositionX, Value2: door.PositionY, Value3: 5, Value4: 3}})
			if packet.Header.MsgID != currentSceneTransitionMsgID || len(rest) != 0 || !bytes.Equal(packet.Body, want) {
				t.Errorf("bottom door body=%x want=%x trailing=%d", packet.Body, want, len(rest))
			}
			stored, _, err = repos.Character.Load(context.Background(), "29")
			if err != nil || stored.Stats["town_id"] != 40 || session.townSeriaRoomEntrySnapshot.PositionValid {
				t.Errorf("exit destination/consume: stats=%+v entry=%+v err=%v", stored.Stats, session.townSeriaRoomEntrySnapshot, err)
			}
			if !t.Failed() {
				t.Logf("actual epic50 -> Seria38/1 -> West Coast40/%d (%d,%d)", door.AreaID, door.PositionX, door.PositionY)
			}
		})
	}
}

func TestEpic39IdentityExceptionIsBoundToActualResource(t *testing.T) {
	gate := int64(47)
	actual := dnftown.Town{ID: 50, Path: "town/2018_Epicroad2.twn", Areas: []dnftown.Area{{ID: 0, MapPath: "Event/2018_EpicRoad2_town/2018_EpicRoad2_town.map", DungeonGate: &gate}}}
	for _, name := range []string{"actual", "case_and_slashes", "wrong_id", "different_town_resource", "different_map_resource", "different_area", "missing_gate", "different_gate", "extra_area"} {
		t.Run(name, func(t *testing.T) {
			town := actual
			town.Areas = append([]dnftown.Area(nil), actual.Areas...)
			switch name {
			case "case_and_slashes":
				town.Path = "TOWN\\2018_EPICROAD2.TWN"
				town.Areas[0].MapPath = "EVENT\\2018_EPICROAD2_TOWN\\2018_EPICROAD2_TOWN.MAP"
			case "wrong_id":
				town.ID++
			case "different_town_resource":
				town.Path = "town/other.twn"
			case "different_map_resource":
				town.Areas[0].MapPath = "Other.map"
			case "different_area":
				town.Areas[0].ID++
			case "missing_gate":
				town.Areas[0].DungeonGate = nil
			case "different_gate":
				other := int64(48)
				town.Areas[0].DungeonGate = &other
			case "extra_area":
				town.Areas = append(town.Areas, town.Areas[0])
			}
			if got := currentTownIsEpicRoadReturnOrigin(town); got != (name == "actual" || name == "case_and_slashes") {
				t.Fatalf("exception=%v for %+v", got, town)
			}
		})
	}
}

func TestEpic39RealPVFDirtyOriginLegacyExit(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires the explicit actual PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"normal_saved_origin", "only_dirty_saved_origin"} {
		t.Run(name, func(t *testing.T) {
			s, session, repos := seriaRoomEntryTownService(t, 38, 1, 450, 234)
			s.townCatalog = table
			t.Setenv(currentTownSeriaExitDoorEnv, "prev_village")
			dirty := currentTownPositionSnapshot{CharacterID: 29, TownID: 50, AreaID: 0, PositionX: 441, PositionY: 207, MovementCode: 5, PositionValid: true}
			session.townPrevVillageSnapshot = dirty
			c, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			if name == "normal_saved_origin" {
				dirty.TownID = 40
				dirty.PositionX = 1500
				dirty.PositionY = 500
			}
			currentTownPrevVillageApplyOriginStats(c.Stats, dirty)
			if err := repos.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			if err := s.handleCurrentPrevVillage(session, nil); err != nil {
				t.Fatal(err)
			}
			stored, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			if name == "only_dirty_saved_origin" {
				if conn.write.Len() != 0 || stored.Stats["town_id"] != 38 {
					t.Fatalf("missing ordinary origin must withhold legacy return; body=%x stats=%v", conn.write.Bytes(), stored.Stats)
				}
			} else {
				packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				want, _ := buildCurrentSceneTransitionBody(40, 0, []currentSceneTransitionRow{{ObjectOrResourceKey: 29, Value1: 1500, Value2: 500, Value3: 5, Value4: 3}})
				if stored.Stats["town_id"] != 40 || len(rest) != 0 || !bytes.Equal(packet.Body, want) {
					t.Fatalf("verified normal recovery body=%x want=%x stats=%v", packet.Body, want, stored.Stats)
				}
			}
		})
	}
}

func TestEpic39RealPVFDirtyColdLoginUsesActualOuterDoor(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires the explicit actual PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, session, repos := seriaRoomEntryTownService(t, 38, 1, 450, 234)
	s.townCatalog = table
	s.initialEquipmentArchive = a
	s.seriaRoomDoorMapReader = nil
	t.Setenv(currentTownSeriaExitDoorEnv, "")
	t.Setenv(currentLoginSeriaEntryTownEnv, "")
	// Archived native once06 char31 had persisted room38 and previous town50
	// (448,205). No earlier ordinary origin remains; do not fabricate it.
	c, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	dirty := currentTownPositionSnapshot{CharacterID: 29, TownID: 50, AreaID: 0, PositionX: 448, PositionY: 205, MovementCode: 5, PositionValid: true}
	currentTownPrevVillageApplyOriginStats(c.Stats, dirty)
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	session.townPositionSnapshot = currentTownPositionSnapshot{}
	if _, _, _, _, _, err := s.currentCharacterListLoginTransition(context.Background(), session, 29, c); err != nil {
		t.Fatal(err)
	}
	if session.townPrevVillageSnapshot.PositionValid || session.townSeriaRoomEntrySnapshot.PositionValid {
		t.Error("dirty epic town rebound during cold login")
	}
	door, found, reason := s.currentSeriaRoomOuterDoorLocation(38, 1)
	if !found {
		t.Fatalf("actual outer door missing: %s", reason)
	}
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if err := s.handleCurrentPrevVillage(session, nil); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	want, _ := buildCurrentSceneTransitionBody(door.TownID, door.AreaID, []currentSceneTransitionRow{{ObjectOrResourceKey: 29, Value1: door.PositionX, Value2: door.PositionY, Value3: 5, Value4: 3}})
	if len(rest) != 0 || !bytes.Equal(packet.Body, want) || door.TownID == 50 {
		t.Fatalf("cold dirty exit body=%x want=%x", packet.Body, want)
	}
	stored, _, err := repos.Character.Load(context.Background(), "29")
	if err != nil || stored.Stats["town_id"] != int64(door.TownID) || stored.Stats[currentTownPrevVillageTownStat] != 50 {
		t.Fatalf("door commit must not invent lost historical origin: stats=%v err=%v", stored.Stats, err)
	}
	t.Logf("dirty origin withheld; actual room outer door=%d/%d (%d,%d)", door.TownID, door.AreaID, door.PositionX, door.PositionY)
}
