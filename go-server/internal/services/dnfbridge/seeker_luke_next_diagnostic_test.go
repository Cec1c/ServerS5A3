package dnfbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type seekerLukeSource map[string]string

func (s seekerLukeSource) ReadText(path string) (string, error) {
	v, ok := s[strings.ToLower(strings.ReplaceAll(path, "\\", "/"))]
	if !ok {
		return "", fmt.Errorf("missing Luke extract %s", path)
	}
	return v, nil
}

// This isolates the completed-run menu boundary. Real dungeon and map texts
// determine the selected maze; completion/card state is explicit test setup.
func TestSeekerLukeNextDungeonDiagnostic(t *testing.T) {
	path := os.Getenv("SEEKER_LUKE_SOURCE_JSON")
	if path == "" {
		t.Skip("set SEEKER_LUKE_SOURCE_JSON")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := seekerLukeSource{}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	table, err := worldmap.LoadSource(ctx, source, worldmap.Options{SkipAreas: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	if err = repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 90,
		Stats: map[string]int64{"fatigue": 100, "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{options: options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable: table, worldMapResolver: resolver, dungeonMonsterTable: monsters,
		dungeonRuleSource: source,
		dungeonChoice:     func(int) (int, error) { return 0, nil }, dungeonSeed: func() (uint32, error) { return 1, nil },
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	logSink, readLogs := supplement45LogSink(t)
	svc.packetLog, svc.logger = logSink.packetLog, logSink.logger
	conn := &bufferConn{}
	channel := channelcatalog.Channel{ServerID: 1, ID: 19, Type: 1, Name: "ch.19", Port: 10019}
	session := &gameSession{conn: conn, connID: "qst027-luke-next", channel: channel, residentChannel: channel, selectedCharacterID: 99}
	rt, _, err := svc.prepareDungeonRuntime(ctx, session, dungeoncmd.SelectDungeonRequest{DungeonID: 5101, Difficulty: 2})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = rt
	session.townSceneReadyCharacterID = 99
	session.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 99, TownID: 38, AreaID: 1, PositionX: 450, PositionY: 234, MovementCode: 5, PositionValid: true}
	if _, ok := bindCurrentTownSelectorOrigin(session); !ok {
		t.Fatal("town origin")
	}
	if err = freezeCurrentDungeonTownReturnOrigin(session, rt); err != nil {
		t.Fatal(err)
	}
	completePartyRetryFixture(t, svc, session)
	conn.write.Reset()
	if err = svc.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketEplpCommand), []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	next := session.dungeon.runtime
	if next == nil || next == rt {
		t.Fatalf("no new run; logs=%s", readLogs())
	}
	t.Logf("completed=%d reentered=%d difficulty=%d", rt.Dungeon.ID, next.Dungeon.ID, next.Request.Difficulty)
	if next.Dungeon.ID != 5102 {
		t.Fatalf("next dungeon=%d want=5102; logs=%s", next.Dungeon.ID, readLogs())
	}
}
