package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func temporaryTownReturnTable(t *testing.T) *dnftown.Table {
	t.Helper()
	table, err := dnftown.Load(context.Background(), townMoveTestSource{
		"town/town.lst":      "38 `normal.twn` 40 `west.twn` 107 `temporary.twn`",
		"town/normal.twn":    "[exclude user pretown info]\n[area]\n0 `new_Elvengard.map` `[normal]`\n[/area]\n[area]\n1 `new_seria_room.map` `[gate]` 450 234\n[/area]",
		"town/west.twn":      "[area]\n0 `WestCoast.map` `[normal]`\n[/area]",
		"town/temporary.twn": "[disable save point]\n[exclude user pretown info]\n[area]\n0 `Town/DayDungeon/DayDungeon_town.map` `[dungeon gate]` 114\n[/area]",
	}, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func TestTemporaryTownReturnUsesOnlyTheCharactersValidatedNormalOrigin(t *testing.T) {
	s := &Service{townCatalog: temporaryTownReturnTable(t)}
	position := func(id uint16, town, area byte) currentTownPositionSnapshot {
		return currentTownPositionSnapshot{CharacterID: id, TownID: town, AreaID: area, PositionX: 523, PositionY: 142, MovementCode: 5, PositionValid: true}
	}
	for _, tc := range []struct {
		name                     string
		currentTown, currentArea byte
		saved                    currentTownPositionSnapshot
		persisted                currentTownPositionSnapshot
		wantTown                 byte
		wantOK                   bool
	}{
		{"ordinary town replaces older origin", 40, 0, position(29, 38, 0), currentTownPositionSnapshot{}, 40, true},
		{"temporary town keeps normal session origin", 107, 0, position(29, 40, 0), position(29, 38, 0), 40, true},
		{"other character session origin ignored", 107, 0, position(30, 38, 0), position(29, 40, 0), 40, true},
		{"stale temporary session origin ignored", 107, 0, position(29, 107, 0), position(29, 40, 0), 40, true},
		{"missing origin stays absent", 107, 0, currentTownPositionSnapshot{}, currentTownPositionSnapshot{}, 0, false},
		{"old persisted temporary origin rejected", 107, 0, currentTownPositionSnapshot{}, position(29, 107, 0), 0, false},
		{"unknown town origin rejected", 107, 0, position(29, 99, 0), currentTownPositionSnapshot{}, 0, false},
		{"room keeps normal origin", 38, 1, position(29, 40, 0), currentTownPositionSnapshot{}, 40, true},
		{"exclude cache marker alone keeps actual street", 38, 0, position(29, 40, 0), currentTownPositionSnapshot{}, 38, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dnfrepo.CharacterRecord{CharacterID: "29", Stats: map[string]int64{"town_id": int64(tc.currentTown), "area_id": int64(tc.currentArea), "pos_x": 523, "pos_y": 142, "direction": 5}}
			if tc.persisted.PositionValid {
				currentTownPrevVillageApplyOriginStats(c.Stats, tc.persisted)
			}
			session := &gameSession{selectedCharacterID: 29, townPrevVillageSnapshot: tc.saved}
			got, ok := s.currentTownSeriaReturnOriginLocked(session, 29, c)
			if ok != tc.wantOK || (ok && (got.CharacterID != 29 || got.TownID != tc.wantTown || !got.PositionValid)) {
				t.Fatalf("origin=%+v ok=%t", got, ok)
			}
		})
	}
}

func TestTemporaryTownExitThenSeriaBottomDoorReturnsToNormalTown(t *testing.T) {
	s, session, repositories := seriaRoomEntryTownService(t, 107, 0, 849, 160)
	s.townCatalog = temporaryTownReturnTable(t)
	origin := currentTownPositionSnapshot{CharacterID: 29, TownID: 40, AreaID: 0, PositionX: 523, PositionY: 142, MovementCode: 5, PositionValid: true}
	session.townPrevVillageSnapshot = origin
	c, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatal("fixture character unavailable")
	}
	currentTownPrevVillageApplyOriginStats(c.Stats, origin)
	if err := repositories.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := s.handleTownSetUserArea(session, buildTownSeriaRoomPortalRequest(38, 1, 433, 311, 5, 107)); err != nil {
		t.Fatal(err)
	}
	if session.townSeriaRoomEntrySnapshot != origin {
		t.Fatalf("temporary town replaced the normal return origin: %+v", session.townSeriaRoomEntrySnapshot)
	}
	c, _, err = repositories.Character.Load(context.Background(), "29")
	if err != nil || c.Stats[currentTownPrevVillageTownStat] != 40 {
		t.Fatal("returning from the temporary town overwrote the durable normal town")
	}
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if err := s.handleCurrentPrevVillage(session, nil); err != nil {
		t.Fatal(err)
	}
	packet, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
	want, err := buildCurrentSceneTransitionBody(40, 0, []currentSceneTransitionRow{{ObjectOrResourceKey: 29, Value1: 523, Value2: 142, Value3: 5, Value4: 3}})
	if err != nil || packet.Header.MsgID != currentSceneTransitionMsgID || len(trailing) != 0 || !bytes.Equal(packet.Body, want) {
		t.Fatalf("bottom-door destination packet=%+v body=%x trailing=%d", packet.Header, packet.Body, len(trailing))
	}
	c, _, err = repositories.Character.Load(context.Background(), "29")
	if err != nil || c.Stats["town_id"] != 40 || c.Stats["area_id"] != 0 || session.townSeriaRoomEntrySnapshot.PositionValid {
		t.Fatal("bottom-door return did not commit and consume its normal origin")
	}
}

func TestPublishedPVFAbisDisablesSavePointButElvengardDoesNot(t *testing.T) {
	path := os.Getenv("YKCN90_ABIS_PVF")
	if path == "" {
		t.Skip("explicit published-PVF check")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	abis, found := table.Find(107)
	if !found || !abis.DisableSavePoint || !strings.EqualFold(abis.Path, "town/chn_daydungeon.twn") {
		t.Fatalf("actual Abis town policy=%+v found=%t", abis, found)
	}
	elvengard, found := table.Find(38)
	if !found || elvengard.DisableSavePoint {
		t.Fatalf("actual Elvengard town policy=%+v found=%t", elvengard, found)
	}
}
