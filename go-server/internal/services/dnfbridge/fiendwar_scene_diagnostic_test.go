package dnfbridge

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func r168FiendWarRun(channelID int, channelType byte, runID string) *currentRaidDungeonEntry {
	return &currentRaidDungeonEntry{
		RaidKey:      7,
		InstanceID:   "inst-7",
		RunID:        runID,
		ChannelID:    channelID,
		ChannelType:  channelType,
		Participants: []uint16{19},
	}
}

func TestR168FiendWarSceneDiagnosticUsesRunIdentityAndSceneLimit(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	p := &gameSession{selectedCharacterID: 19}
	p.channel.ID = 244
	p.channel.Type = 42
	r := &runtimeDungeonState{
		Dungeon:   worldmap.Dungeon{ID: 4975},
		MazeIndex: 0,
		Seed:      9,
		raidEntry: r168FiendWarRun(244, 42, "run-4975"),
	}
	scene := worldmap.DungeonRoomScene{}
	scene.Coordinate = worldmap.RoomCoordinate{X: 1, Y: 2}
	scene.Map.Map.ID = 306000230

	for i := 0; i < 100; i++ {
		for _, stage := range []string{"post_start_placement", "finish_loading_placement", "client_position", "unknown"} {
			s.logFiendWarSceneDiagnostic(p, r, scene, stage, 123, 234)
		}
	}
	if got := strings.Count(logs.String(), "game-fiendwar-scene-diagnostic"); got != 4 {
		t.Fatalf("events=%d want4", got)
	}
	if strings.Count(logs.String(), "\"position_x\":123") != 2 {
		t.Fatal("client positions not retained")
	}
	if !strings.Contains(logs.String(), "\"dungeon_id\":4975") {
		t.Fatal("FiendWar facility dungeon was filtered out")
	}

	negativeCases := []struct {
		name        string
		channelType byte
		entry       *currentRaidDungeonEntry
		mapID       int64
	}{
		{"non_fiendwar_channel", 0, r168FiendWarRun(244, 42, "run-4975"), 306000240},
		{"missing_run", 42, nil, 306000241},
		{"wrong_channel_type", 42, r168FiendWarRun(244, 23, "run-4975"), 306000242},
		{"wrong_channel_id", 42, r168FiendWarRun(245, 42, "run-4975"), 306000243},
		{"empty_run_id", 42, r168FiendWarRun(244, 42, ""), 306000244},
	}
	for _, tc := range negativeCases {
		before := logs.Len()
		p.channel.Type = tc.channelType
		r.raidEntry = tc.entry
		// A fresh scene key and state ensure a rejected gate cannot hide behind
		// the post_start_placement limit already consumed above.
		negativeScene := worldmap.DungeonRoomScene{}
		negativeScene.Coordinate = worldmap.RoomCoordinate{X: 3, Y: 2}
		negativeScene.Map.Map.ID = tc.mapID
		r.fiendWarSceneDiagnostic = fiendWarSceneDiagnosticState{}
		s.logFiendWarSceneDiagnostic(p, r, negativeScene, "post_start_placement", 0, 0)
		if logs.Len() != before {
			t.Fatalf("%s logged without a bound FiendWar run", tc.name)
		}
	}

	p.channel.Type = 42
	r.raidEntry = r168FiendWarRun(244, 42, "run-4975")
	scene.Coordinate.X++
	s.logFiendWarSceneDiagnostic(p, r, scene, "client_position", 12, 34)
	if got := strings.Count(logs.String(), "game-fiendwar-scene-diagnostic"); got != 5 {
		t.Fatalf("events=%d want5 after room change", got)
	}
}

func TestR168FiendWarSceneDiagnosticAcceptsVerifiedDungeonIDs(t *testing.T) {
	for _, dungeonID := range []int64{4953, 4978, 4964, 4975} {
		dungeonID := dungeonID
		t.Run(fmt.Sprintf("%d", dungeonID), func(t *testing.T) {
			var logs bytes.Buffer
			s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			p := &gameSession{selectedCharacterID: 19}
			p.channel.ID = 244
			p.channel.Type = 42
			r := &runtimeDungeonState{
				Dungeon:   worldmap.Dungeon{ID: dungeonID},
				MazeIndex: 0,
				Seed:      9,
				raidEntry: r168FiendWarRun(244, 42, fmt.Sprintf("run-%d", dungeonID)),
			}
			scene := worldmap.DungeonRoomScene{}
			scene.Coordinate = worldmap.RoomCoordinate{X: 1, Y: 2}
			scene.Map.Map.ID = 306000230 + dungeonID
			s.logFiendWarSceneDiagnostic(p, r, scene, "post_start_placement", 0, 0)
			if got := strings.Count(logs.String(), "game-fiendwar-scene-diagnostic"); got != 1 {
				t.Fatalf("events=%d want1", got)
			}
			if !strings.Contains(logs.String(), fmt.Sprintf("%q:%d", "dungeon_id", dungeonID)) {
				t.Fatalf("dungeon %d missing from diagnostic", dungeonID)
			}
		})
	}
}

func TestR168FiendWarFinishDiagnosticSurvivesRoomMoveEarlyReturn(t *testing.T) {
	service, session, logs := r168FiendWarDiagnosticFixture(t)
	session.postFinishLoadingPlayerStateSent = true

	if err := service.sendCurrentPostFinishLoadingPlayerState(session, "r168_room_move_finish"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(logs.String(), "game-fiendwar-scene-diagnostic"); got != 1 {
		t.Fatalf("events=%d want1 after early-return finish", got)
	}
	if !strings.Contains(logs.String(), "\"stage\":\"finish_loading_observed\"") {
		t.Fatal("finish observation stage missing")
	}
	if !strings.Contains(logs.String(), "\"placement_sent\":false") {
		t.Fatal("early-return observation must record placement_sent=false")
	}
	if strings.Contains(logs.String(), "\"actor_slot\"") {
		t.Fatal("early-return observation recorded unsent placement fields")
	}
	if !strings.Contains(logs.String(), "\"dungeon_id\":4975") {
		t.Fatal("FiendWar facility dungeon was filtered out on finish")
	}

	if err := service.sendCurrentPostFinishLoadingPlayerState(session, "r168_room_move_finish_repeat"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(logs.String(), "game-fiendwar-scene-diagnostic"); got != 1 {
		t.Fatalf("events=%d want1 after repeated early-return finish", got)
	}

	runtime := session.dungeon.runtime
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("fixture scene missing")
	}
	service.logFiendWarSceneDiagnostic(session, runtime, scene, "finish_loading_placement", 0, 0)
	if got := strings.Count(logs.String(), "game-fiendwar-scene-diagnostic"); got != 2 {
		t.Fatalf("events=%d want2 after successful placement", got)
	}
	if !strings.Contains(logs.String(), "\"stage\":\"finish_loading_placement\"") {
		t.Fatal("successful placement stage missing")
	}
	if !strings.Contains(logs.String(), "\"placement_sent\":true") {
		t.Fatal("successful placement must record placement_sent=true")
	}

	if err := service.sendCurrentPostFinishLoadingPlayerState(session, "r168_room_move_finish_after_success"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(logs.String(), "game-fiendwar-scene-diagnostic"); got != 2 {
		t.Fatalf("events=%d want2 after observation repeat", got)
	}
}

func r168FiendWarDiagnosticFixture(t *testing.T) (*Service, *gameSession, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	service := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	_, resolver, _ := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	topology, err := worldmap.BuildDungeonLayout(resolver, 700, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
		return choice.Candidates[0].ID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		t.Fatal(err)
	}
	dungeonSession, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatal(err)
	}
	session := &gameSession{selectedCharacterID: 19}
	session.channel.ID = 244
	session.channel.Type = 42
	runtime := &runtimeDungeonState{
		Dungeon:   worldmap.Dungeon{ID: 4975},
		MazeIndex: 0,
		Seed:      9,
		Session:   dungeonSession,
		raidEntry: r168FiendWarRun(244, 42, "run-4975"),
	}
	session.dungeon.runtime = runtime
	return service, session, &logs
}
