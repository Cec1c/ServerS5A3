package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfmonster "longheng.io/server/internal/modules/dnf/monster"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

const doc94EkernClearConditionObjectID = uint32(53764)

func doc94Official249DeathBody(objectKey uint32) []byte {
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
	binary.LittleEndian.PutUint32(body[0:4], objectKey)
	binary.LittleEndian.PutUint16(body[4:6], ^uint16(0))
	return body
}
func doc94EkernBossRoomPVFSource() bridgePVFSource {
	return bridgePVFSource{
		worldmap.DefaultMapList: "74195 \x60legend/ekern_boss.map\x60\n",
		"map/legend/ekern_boss.map": "[map name]\n\x60Ekern BossChecker\x60\n" +
			"[dungeon]\n5097\n[type]\n\x60[boss]\x60\n" +
			"[passive object]\n53764 476 234 0\n",
		worldmap.DefaultDungeonList: "5097 \x60ekern_single.dgn\x60\n",
		"dungeon/ekern_single.dgn": "[name]\n\x60Ekern Single - Lost Temple\x60\n" +
			"[minimum required level]\n90\n[basis level]\n90\n[limit party count]\n1\n" +
			"[maze info]\n[size]\n1 1\n[greed]\n\x60A\x60\n" +
			"[map specification]\n\x60boss\x60 0 0 74195\n[start map]\n0 0\n[boss map]\n0 0\n" +
			"[clear condition]\n[destroy object]\n53764 1\n[/clear condition]\n",
		worldmap.DefaultWorldMapList: "45 \x60legend.wdm\x60\n",
		"worldmap/legend.wdm":        "[name]\n\x60魔界集结地\x60\n[dungeon]\n5097 -1\n[/dungeon]\n",
		dnfmonster.DefaultList:       "3001 \x60unused.gob\x60\n",
		"monster/unused.gob":         "[name]\n\x60Unused\x60\n[level]\n90\n[hp]\n1\n[exp]\n0\n",
	}
}
func seedDungeonDailyCountCharacter(t *testing.T, repos dnfrepo.Group, id string, selected uint16, account string) *gameSession {
	t.Helper()
	character := dnfrepo.CharacterRecord{CharacterID: id, AccountID: account, Level: 90, Stats: map[string]int64{"town_id": 107, "area_id": 0, "pos_x": 250, "pos_y": 224, "direction": 5, "area_state": 3, "fatigue": 100}}
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{selectedCharacterID: selected, accountID: account, conn: &bufferConn{}, connID: "daily-count-test"}
	return session
}
func enterDungeonOnce(t *testing.T, s *Service, session *gameSession, dungeonID uint32) (*runtimeDungeonState, error) {
	t.Helper()
	runtime, _, err := s.prepareDungeonRuntime(context.Background(), session, dungeoncmd.SelectDungeonRequest{DungeonID: dungeonID, Difficulty: 0})
	if err != nil {
		return nil, err
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		t.Fatal(err)
	}
	if err := s.commitDungeonRuntime(session, runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}
