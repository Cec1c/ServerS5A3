package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func seedGoldDiagnosticSnapshot(p *gameSession) {
	storeGoldDiagnosticSnapshot(p, p.selectedCharacterID, p.channel.ID)
}

func goldDiagnosticEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(row["msg"].(string), "dnfbridge gold ") {
			events = append(events, row)
		}
	}
	return events
}

func goldDiagnosticBody(t *testing.T) []byte {
	t.Helper()
	var drops []currentDungeonDeathDropWire
	for i, amount := range []uint32{2740, 1, 0xb40a0000} {
		id := uint32(0)
		if i == 1 {
			id = 3227
		}
		entry, err := currentDungeonDeathDropItemState(uint16(7+i), dungeonDropItemDefinition{ItemID: id, Kind: dungeonDropItemStackable}, amount)
		if err != nil {
			t.Fatal(err)
		}
		drops = append(drops, currentDungeonDeathDropWire{SceneObjectKey: uint32(37 + i), Item: entry, UnknownTailSentinel: 65535, OwnerActorObjectKey: 99})
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(401, currentDungeonDeathResponseMonster, drops)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestGoldDiagnosticObservesActualWireWithoutChangingAmount(t *testing.T) {
	for _, target := range []int{0, 98, 99} {
		var logs bytes.Buffer
		s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), options: options{goldDiagnosticCharID: target}}
		conn := &bufferConn{}
		p := &gameSession{conn: conn, connID: "gold-diagnostic", selectedCharacterID: 99, upperSeq: 37}
		seedGoldDiagnosticSnapshot(p)
		body := goldDiagnosticBody(t)
		original := append([]byte(nil), body...)
		if err := s.sendGameUpperRawClass(p, 38, body, 0); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
		if !bytes.Equal(body, original) || !bytes.Equal(packet.Body, original) || len(rest) != 0 || p.upperSeq != 38 {
			t.Fatal("diagnostic changed packet or sequence")
		}
		events := goldDiagnosticEvents(t, &logs)
		if target != 99 {
			if len(events) != 0 {
				t.Fatal("unselected connection logged", events)
			}
			continue
		}
		if len(events) != 1 || events[0]["write_failed"] != false || events[0]["sequence"] != float64(37) {
			t.Fatal(events)
		}
		rows := events[0]["gold_rows"].([]any)
		if len(rows) != 2 {
			t.Fatal(rows)
		}
		first, last := rows[0].(map[string]any), rows[1].(map[string]any)
		if first["amount"] != float64(2740) || first["object_key"] != float64(37) || last["amount"] != float64(0xb40a0000) || last["signed_amount"] != float64(-1274413056) {
			t.Fatal(rows)
		}
	}
}

type goldDiagnosticFailConn struct{ bufferConn }

func (*goldDiagnosticFailConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestGoldDiagnosticMarksWriteFailureAndBoundsMalformedInput(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), options: options{goldDiagnosticCharID: 99}}
	p := &gameSession{conn: &goldDiagnosticFailConn{}, selectedCharacterID: 99}
	seedGoldDiagnosticSnapshot(p)
	if err := s.sendGameUpperRawClass(p, 38, goldDiagnosticBody(t), 0); err == nil {
		t.Fatal("lost write error")
	}
	events := goldDiagnosticEvents(t, &logs)
	if len(events) != 1 || events[0]["write_failed"] != true {
		t.Fatal(events)
	}
	logs.Reset()
	for _, body := range [][]byte{nil, {1}, {1, 0, 255}, {1, 0, 1, 0}} {
		s.logGoldDiagnosticWire(p, 38, 0, 0, body, false)
	}
	for _, event := range goldDiagnosticEvents(t, &logs) {
		if _, ok := event["gold_rows"]; ok {
			t.Fatal("invented rows from malformed body", event)
		}
	}
	logs.Reset()
	p.goldDiagnosticCount.Store(0)
	for i := 0; i < currentGoldDiagnosticLimit+20; i++ {
		s.logGoldDiagnosticWire(p, 38, 0, 0, nil, false)
	}
	if len(goldDiagnosticEvents(t, &logs)) != currentGoldDiagnosticLimit || p.goldDiagnosticCount.Load() != currentGoldDiagnosticLimit {
		t.Fatal("event cap not enforced")
	}
}

func TestGoldDiagnosticTracksRealPickupRejectionAndRedactsPrivateFields(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), options: options{goldDiagnosticCharID: 99}}
	p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, accountID: "PRIVATE_ACCOUNT"}
	seedGoldDiagnosticSnapshot(p)
	if err := s.handleCurrentDungeonPickup(p, currentDungeonPickupTestBody(37)); err != nil {
		t.Fatal(err)
	}
	events := goldDiagnosticEvents(t, &logs)
	if len(events) != 2 || events[0]["kind"] != "game-dungeon-pickup-received" || events[1]["reason"] != "dungeon_drop_runtime_missing" || events[1]["drop_object_key"] != float64(37) {
		t.Fatal(events)
	}
	logs.Reset()
	s.logGameEvent(p, "game-dungeon-pickup-blocked", "drop_object_key", uint32(38), "body", "PRIVATE_BODY", "account_id", "PRIVATE_ACCOUNT", "request_token0", uint16(123), "reason", "PRIVATE_REASON", "error", io.ErrClosedPipe, "amount", "PRIVATE_AMOUNT")
	events = goldDiagnosticEvents(t, &logs)
	if len(events) != 1 || events[0]["failed"] != true || events[0]["drop_object_key"] != float64(38) || strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal(events)
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("rejected pickup sent a success")
	}
}

func TestGoldDiagnosticDoesNotClaimSuppressedOrQueuedPacketsWereSent(t *testing.T) {
	for _, mode := range []string{"suppressed", "queued", "short"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), options: options{goldDiagnosticCharID: 99}}
			conn := &bufferConn{}
			p := &gameSession{conn: conn, selectedCharacterID: 99}
			seedGoldDiagnosticSnapshot(p)
			t.Setenv(upperSendFilterEnv, "")
			if mode == "suppressed" {
				t.Setenv(upperSendFilterEnv, "0:38")
			}
			resetUpperSendFilterCache()
			defer resetUpperSendFilterCache()
			if mode == "queued" {
				p.channelSceneBatch = &currentChannelSceneBatch{}
			}
			if mode == "short" {
				p.conn = &sharedDiagnosticFaultConn{short: true}
			}
			if err := s.sendGameUpperRawClass(p, 38, goldDiagnosticBody(t), 0); (err != nil) != (mode == "short") {
				t.Fatal(err)
			}
			events := goldDiagnosticEvents(t, &logs)
			if mode == "short" {
				if len(events) != 1 || events[0]["write_failed"] != true {
					t.Fatal(events)
				}
				return
			}
			if len(events) != 0 || conn.write.Len() != 0 {
				t.Fatal("claimed suppressed/queued packet was sent", events)
			}
			if mode == "queued" {
				if err := s.finishCurrentOrdinaryChannelSceneBatch(p, nil); err != nil {
					t.Fatal(err)
				}
				events = goldDiagnosticEvents(t, &logs)
				if len(events) != 1 || events[0]["write_failed"] != false || conn.write.Len() == 0 {
					t.Fatal("flushed packet missing", events)
				}
			}
		})
	}
}

func TestGoldDiagnosticPreservesPickupCommitAndReplay(t *testing.T) {
	s, runtime, repos := prepareCurrentDungeonDropTest(t, 0, 3227)
	source := s.dungeonMonsterTable.source.(bridgePVFSource)
	// The fixture selects difficulty index 1. The public base formula uses a
	// 1.20 gold difficulty bonus, so a base of 100 with zero variance yields
	// floor(100 * 1.20) = 120. Keep exact pickup and replay assertions on it.
	source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
	source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
	currentDungeonMonsterDropRulesByCatalog.Delete(s.dungeonMonsterTable)
	currentDungeonGoldReferencesByCatalog.Delete(s.dungeonMonsterTable)
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	s.options.goldDiagnosticCharID = 99
	p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	seedGoldDiagnosticSnapshot(p)
	wires, err := s.planCurrentDungeonMonsterDrops(runtime, runtime.Room.Snapshot().Monsters[0], currentSceneActorObjectKey(99))
	if err != nil {
		t.Fatal(err)
	}
	var gold *runtimeDungeonDrop
	for _, w := range wires {
		if d := runtime.DropOwner.byObjectKey[w.SceneObjectKey]; d != nil && d.isGold() {
			gold = d
			break
		}
	}
	if gold == nil || gold.Amount != 120 {
		t.Fatal("missing known gold drop", gold)
	}
	for i := 0; i < 2; i++ {
		if err := s.handleCurrentDungeonPickup(p, currentDungeonPickupTestBody(gold.ObjectKey)); err != nil {
			t.Fatal(err)
		}
	}
	character, found, err := repos.Character.Load(context.Background(), "99")
	if err != nil || !found || character.Stats["gold"] != 120 || gold.Status != runtimeDungeonDropConsumed {
		t.Fatal("pickup ledger changed", character, err)
	}
	var accepted, replayed int
	for _, e := range goldDiagnosticEvents(t, &logs) {
		if e["kind"] == "game-dungeon-pickup-accepted" {
			accepted++
			if e["amount"] != float64(120) {
				t.Fatal(e)
			}
		}
		if e["kind"] == "game-dungeon-pickup-replayed" {
			replayed++
		}
	}
	if accepted != 1 || replayed != 1 {
		t.Fatal("missing transaction/replay evidence", logs.String())
	}
}

func TestGoldDiagnosticSnapshotLifecycleAndFiltering(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), options: options{goldDiagnosticCharID: 99}}
	p := &gameSession{conn: &bufferConn{}, connID: "snapshot-lifecycle", selectedCharacterID: 99}
	p.channel.ID = 121

	s.installGoldDiagnosticSnapshot(p)
	snapshot := p.goldDiagnosticSnapshot.Load()
	if snapshot == nil || snapshot.characterID != 99 || snapshot.channelID != 121 {
		t.Fatalf("installed snapshot = %+v", snapshot)
	}

	// Wire and pickup logging use the installed pair rather than mutable
	// business fields once selection has committed.
	p.selectedCharacterID, p.channel.ID = 98, 999
	s.logGoldDiagnosticPickup(p, "game-dungeon-pickup-received", "body_len", 21)
	events := goldDiagnosticEvents(t, &logs)
	if len(events) != 1 || events[0]["char_id"] != float64(99) || events[0]["channel_id"] != float64(121) {
		t.Fatalf("pickup snapshot drifted: %v", events)
	}

	s.clearGoldDiagnosticSnapshot(p)
	if p.goldDiagnosticSnapshot.Load() != nil {
		t.Fatal("cleanup retained a diagnostic snapshot")
	}
	logs.Reset()
	s.logGoldDiagnosticPickup(p, "game-dungeon-pickup-received", "body_len", 21)
	if len(goldDiagnosticEvents(t, &logs)) != 0 {
		t.Fatal("cleared snapshot emitted an event")
	}
}

type goldDiagnosticLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *goldDiagnosticLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *goldDiagnosticLockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func TestGoldDiagnosticSnapshotPairRemainsAtomicDuringSelectionWrites(t *testing.T) {
	var logs goldDiagnosticLockedBuffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), options: options{goldDiagnosticCharID: 99}}
	p := &gameSession{conn: &bufferConn{}, connID: "snapshot-concurrent", selectedCharacterID: 99}
	p.channel.ID = 121
	storeGoldDiagnosticSnapshot(p, 99, 121)
	s.logGoldDiagnosticWire(p, 38, 0, 0, nil, false)
	stopUpdates := make(chan struct{})
	updatesDone := make(chan struct{})
	go func() {
		defer close(updatesDone)
		for i := 0; ; i++ {
			select {
			case <-stopUpdates:
				return
			default:
			}
			s.mu.Lock()
			if i%2 == 0 {
				p.selectedCharacterID, p.channel.ID = 99, 121
				storeGoldDiagnosticSnapshot(p, 99, 121)
			} else {
				p.selectedCharacterID, p.channel.ID = 98, 122
				storeGoldDiagnosticSnapshot(p, 98, 122)
			}
			s.mu.Unlock()
			runtime.Gosched()
		}
	}()

	var writers sync.WaitGroup
	for i := 0; i < 4; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for j := 0; j < 32; j++ {
				p.wireMu.Lock()
				s.logGoldDiagnosticWire(p, 38, 0, uint16(j), nil, false)
				p.wireMu.Unlock()
			}
		}()
	}
	writers.Wait()
	close(stopUpdates)
	<-updatesDone

	events := goldDiagnosticEvents(t, bytes.NewBuffer(logs.Bytes()))
	if len(events) == 0 {
		t.Fatal("configured snapshot emitted no events")
	}
	for _, event := range events {
		if event["char_id"] != float64(99) || event["channel_id"] != float64(121) {
			t.Fatalf("mixed snapshot pair: %v", event)
		}
	}
}
