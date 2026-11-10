package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/platform/config"
)

// Synthetic two-monster fixture with the real Ekern base room's team numbers.
// This proves a code boundary, not the shape of a captured Ekern death packet.
func ekernDiagnosticFixture(t *testing.T, enabled bool) (*Service, *gameSession, *bytes.Buffer) {
	t.Helper()
	source := bridgeGenericDungeonMovePVF(false)
	source["map/dungeon/test/start.map"] += "[monster]\n" +
		"3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n" +
		"3001 10 0 200 200 0 0 0 `[fixed]` `[normal]`\n[/monster]\n" +
		"[monster team]\n100 101\n[/monster team]\n"
	s, rt := prepareSyntheticMoveRuntimeFromPVF(t, source)
	if _, err := rt.Room.AnnounceAllActors(rt.Session); err != nil {
		t.Fatal(err)
	}
	rt.Dungeon.Path = "dungeon/Legend/Ekern/Warriors/synthetic.dgn"
	s.options.currentClientVersion = "official-24.9"
	s.options.ekernDeathDiagnostic = enabled
	logs := &bytes.Buffer{}
	s.logger = slog.New(slog.NewJSONHandler(logs, nil))
	p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99,
		connID: "PRIVATE_CONNECTION", accountID: "PRIVATE_ACCOUNT",
		dungeon: dungeonSessionState{runtime: rt}}
	return s, p, logs
}

func ekernDiagnosticBody(target uint32, owner uint16, contributor uint32) []byte {
	size := dungeoncmd.Official249DieMonsterBaseSize
	if contributor != 0 {
		size += dungeoncmd.Official249DieMonsterEntrySize
	}
	body := make([]byte, size)
	binary.LittleEndian.PutUint32(body, target)
	binary.LittleEndian.PutUint16(body[4:], owner)
	if contributor != 0 {
		body[30] = 1
		binary.LittleEndian.PutUint16(body[31:], 0x211)
		binary.LittleEndian.PutUint16(body[33:], uint16(contributor))
		binary.LittleEndian.PutUint64(body[35:], 1)
	}
	return body
}

func ekernDiagnosticEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == ekernDiagnosticMessage {
			events = append(events, event)
		}
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("private connection/account/body escaped to diagnostic log")
	}
	return events
}

func TestEkernDiagnosticDefaultsOff(t *testing.T) {
	for _, value := range []string{"", "false", "true"} {
		t.Setenv(ekernDeathDiagnosticEnv, value)
		if got := optionsFromConfig(config.ServiceConfig{}).ekernDeathDiagnostic; got != (value == "true") {
			t.Fatalf("env=%q enabled=%t", value, got)
		}
	}
}

func TestEkernDiagnosticScopeAndDisabledNoState(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		enabled    bool
	}{
		{"disabled", "dungeon/legend/ekern/warriors/test.dgn", false},
		{"other dungeon", "dungeon/ordinary/test.dgn", true},
		{"prefix collision", "dungeon/legend/ekern_other/test.dgn", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, logs := ekernDiagnosticFixture(t, tc.enabled)
			rt := p.dungeon.runtime
			rt.Dungeon.Path = tc.path
			if d := s.beginEkernDiagnosticLocked(rt, "death", 74); d != nil || rt.ekernDiagnostic != nil {
				t.Fatal("disabled/out-of-scope diagnostic allocated state")
			}
			if logs.Len() != 0 {
				t.Fatal("disabled/out-of-scope diagnostic logged")
			}
		})
	}
}

func TestEkernDiagnosticSentinelContributionRejectionDoesNotChangeAuthority(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s, p, logs := ekernDiagnosticFixture(t, enabled)
		rt := p.dungeon.runtime
		before := rt.Room.Snapshot()
		beforeScene := rt.Session.Snapshot().Scene
		body := ekernDiagnosticBody(before.Monsters[0].ObjectKey, ^uint16(0), before.Monsters[1].ObjectKey)
		// 两个在册怪物的 0x211 互杀现在由房间互杀规则正常结算；改用 0x111
		// （AI 角色贡献）后，在没有消耗品租约时仍然被拒，这条用例验证的正是
		// "仍被拒的形状"不会因诊断而改变权威状态。
		binary.LittleEndian.PutUint16(body[31:], 0x111)
		if err := s.handleDungeonMonsterDeath(p, body); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, rt.Room.Snapshot()) || !reflect.DeepEqual(beforeScene, rt.Session.Snapshot().Scene) || p.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("diagnostic changed rejected death state or emitted a game packet")
		}
		events := ekernDiagnosticEvents(t, logs)
		if !enabled {
			if len(events) != 0 {
				t.Fatal("default-off path logged")
			}
			continue
		}
		if len(events) != 1 {
			t.Fatalf("events=%v", events)
		}
		e := events[0]
		if e["outcome"] != "sentinel_shape_rejected" || e["target_announced_live"] != true ||
			e["friendly_apc_proven"] != false || e["ordinary_op38_attempted"] != false ||
			e["after_remaining_blocking_count"] != float64(2) ||
			// 0x111 形状的贡献者不按房间怪物解析，因此没有阵营证据（-1）。
			!reflect.DeepEqual(e["contributor_pvf_teams"], []any{float64(-1)}) ||
			!reflect.DeepEqual(e["contributor_keys"], []any{float64(403)}) {
			t.Fatalf("lost rejection/contributor/remaining-blocker evidence: %v", e)
		}
	}
}

func TestEkernDiagnosticPlayerDeathPreservesWireAndRecordsWrite(t *testing.T) {
	var baseline []byte
	for _, enabled := range []bool{false, true} {
		s, p, logs := ekernDiagnosticFixture(t, enabled)
		rt := p.dungeon.runtime
		key := rt.Room.Snapshot().Monsters[0].ObjectKey
		if err := s.handleDungeonMonsterDeath(p, ekernDiagnosticBody(key, currentSceneActorObjectKey(99), 0)); err != nil {
			t.Fatal(err)
		}
		if _, dead := rt.Room.DefeatedDeathTarget(key); !dead {
			t.Fatal("legal player-owned death no longer committed")
		}
		wire := p.conn.(*bufferConn).write.Bytes()
		if !enabled {
			baseline = append([]byte(nil), wire...)
			continue
		}
		if !bytes.Equal(baseline, wire) {
			t.Fatal("enabling diagnostic changed game wire bytes")
		}
		events := ekernDiagnosticEvents(t, logs)
		if len(events) != 1 || events[0]["ordinary_op38_written"] != true ||
			events[0]["after_remaining_blocking_count"] != float64(1) {
			t.Fatalf("missing committed death/write evidence: %v", events)
		}
	}
}

func TestEkernDiagnosticMalformedAndMoveRejection(t *testing.T) {
	s, p, logs := ekernDiagnosticFixture(t, true)
	if err := s.handleDungeonMonsterDeath(p, []byte("PRIVATE_BODY")); err != nil {
		t.Fatal(err)
	}
	p.dungeon.mu.Lock()
	err := s.handleDungeonMoveRequestLocked(p, dungeoncmd.MoveMapRequest{NextX: 1}, dungeoncmd.MoveMapRequestSize, "synthetic", nil)
	p.dungeon.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	events := ekernDiagnosticEvents(t, logs)
	if len(events) != 2 || events[0]["outcome"] != "decode_rejected" ||
		events[1]["outcome"] != "move_preflight_rejected" || events[1]["current_room_not_cleared"] != true {
		t.Fatalf("missing malformed/move evidence: %v", events)
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("diagnostic sent gameplay packets for malformed death/uncleared move")
	}
	if err := s.handleDungeonMoveMapPlanned(p, []byte("PRIVATE_BODY"), nil, true); err != nil {
		t.Fatal(err)
	}
	events = ekernDiagnosticEvents(t, logs)
	if len(events) != 3 || events[2]["outcome"] != "move_boundary_rejected" {
		t.Fatalf("malformed op45 was invisible to diagnostic: %v", events)
	}
}

func TestEkernDiagnosticCountAndTimeBudgets(t *testing.T) {
	s, p, logs := ekernDiagnosticFixture(t, true)
	rt := p.dungeon.runtime
	for i := 0; i < ekernDiagnosticLimit+5; i++ {
		d := s.beginEkernDiagnosticLocked(rt, "death", 74)
		s.finishEkernDiagnosticLocked(rt, d, "synthetic", nil)
	}
	if got := len(ekernDiagnosticEvents(t, logs)); got != ekernDiagnosticLimit {
		t.Fatalf("sample budget=%d want=%d", got, ekernDiagnosticLimit)
	}
	s, p, _ = ekernDiagnosticFixture(t, true)
	rt = p.dungeon.runtime
	if s.beginEkernDiagnosticLocked(rt, "death", 74) == nil {
		t.Fatal("first sample rejected")
	}
	rt.ekernDiagnostic.started = s.gameplayNow().Add(-ekernDiagnosticTTL)
	if s.beginEkernDiagnosticLocked(rt, "death", 74) != nil {
		t.Fatal("expired run budget restarted")
	}
}
