package dnfbridge

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestR166FiendWarSceneDiagnosticLimits(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	p := &gameSession{selectedCharacterID: 19}
	p.channel.Type = 42
	r := &runtimeDungeonState{}
	r.Dungeon.ID = 4953
	scene := worldmap.DungeonRoomScene{}
	scene.Map.Map.ID = 306000458
	for i := 0; i < 100; i++ {
		for _, stage := range []string{"post_start_placement", "finish_loading_placement", "client_position", "unknown"} {
			s.logFiendWarSceneDiagnostic(p, r, scene, stage, 123, 234)
		}
	}
	if got := strings.Count(logs.String(), "game-fiendwar-scene-diagnostic"); got != 4 {
		t.Fatalf("events=%d want4", got)
	}
	if strings.Count(logs.String(), `"position_x":123`) != 2 {
		t.Fatal("client positions not retained")
	}
	before := logs.Len()
	p.channel.Type = 0
	s.logFiendWarSceneDiagnostic(p, r, scene, "post_start_placement", 0, 0)
	p.channel.Type = 42
	r.Dungeon.ID = 1
	s.logFiendWarSceneDiagnostic(p, r, scene, "post_start_placement", 0, 0)
	if logs.Len() != before {
		t.Fatal("non-FiendWar scene logged")
	}
	r.Dungeon.ID = 4953
	scene.Coordinate.X++
	s.logFiendWarSceneDiagnostic(p, r, scene, "client_position", 12, 34)
	if strings.Count(logs.String(), "game-fiendwar-scene-diagnostic") != 5 {
		t.Fatal("new room evidence missing")
	}
}
