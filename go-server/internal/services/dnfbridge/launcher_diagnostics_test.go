package dnfbridge

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestLauncherContextLifecycle(t *testing.T) {
	s := &Service{gameSessions: make(map[uint16]*gameSession)}
	grant := s.launcherDiagnostics.issue("game-token-fixture", strings.Repeat("a", 32), time.Now())
	if grant == nil {
		t.Fatal("no grant")
	}
	first := &gameSession{connID: "game-1", characterGeneration: 1}
	s.bindLauncherDiagnostics(first, "game-token-fixture")
	s.captureLauncherContext(first, false)
	entry := first.launcherDiagnostic.entry
	if entry.snapshot.Fields["character_name"].Value != nil {
		t.Fatal("unselected identity must be empty")
	}
	first.selectedCharacterID = 11
	first.launcherCharacterID = 11
	first.launcherCharacterName = "Fixture A"
	s.gameSessions[11] = first
	s.captureLauncherContext(first, false)
	first.selectedCharacterID = 0
	delete(s.gameSessions, 11)
	s.captureLauncherContext(first, false)
	if entry.snapshot.Fields["character_name"].Value != nil {
		t.Fatal("return to selection retained A")
	}
	first.selectedCharacterID = 22
	first.characterGeneration = 2
	first.launcherCharacterID = 22
	first.launcherCharacterName = "Fixture B"
	s.gameSessions[22] = first
	s.captureLauncherContext(first, false)
	second := &gameSession{connID: "game-2", selectedCharacterID: 22, characterGeneration: 3, launcherCharacterID: 22, launcherCharacterName: "Fixture B"}
	s.bindLauncherDiagnostics(second, "game-token-fixture")
	s.gameSessions[22] = second
	s.captureLauncherContext(second, false)
	s.captureLauncherContext(first, true)
	if entry.snapshot.Fields["connection_id"].Value == nil || *entry.snapshot.Fields["connection_id"].Value != "game-2" || entry.snapshot.Status != "current" {
		t.Fatal("retired connection overwrote reconnect")
	}
	second.dungeon.runtime = &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 310}, Character: dnfrepo.CharacterRecord{CharacterID: "22"}, settlementPhase: currentDungeonSettlementPhaseCardsRevealed, startedAt: time.Now()}
	s.captureLauncherContext(second, false)
	s.captureLauncherContext(second, true)
	if *entry.snapshot.Fields["character_name"].Value != "Fixture B" || *entry.snapshot.Fields["settlement_phase"].Value != "cards_revealed" || entry.snapshot.Fields["character_name"].Status != "last_known" || entry.events[len(entry.events)-1].Kind != "business_disconnect" {
		t.Fatal("disconnect lost B or settlement phase")
	}
	api := &accountAPIServer{service: s}
	body := `{"read_token":"` + grant.ReadToken + `","correlation_id":"` + grant.CorrelationID + `","after":0}`
	w := httptest.NewRecorder()
	api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(body)))
	if w.Code != 200 || strings.Contains(w.Body.String(), grant.ReadToken) || strings.Contains(w.Body.String(), "game-token-fixture") {
		t.Fatal("readback failed or leaked credentials")
	}
	var reply launcherContextReply
	if json.Unmarshal(w.Body.Bytes(), &reply) != nil || len(reply.Events) < 5 || reply.Snapshot.CorrelationID != grant.CorrelationID {
		t.Fatal("timeline incomplete")
	}
	if path := os.Getenv("DNF_LAUNCHER_CONTEXT_FIXTURE"); path != "" {
		if err := os.WriteFile(path, w.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w = httptest.NewRecorder()
	api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(body)))
	if w.Code != 429 {
		t.Fatal("poll not bounded")
	}
	body = strings.Replace(body, grant.CorrelationID, strings.Repeat("b", 32), 1)
	w = httptest.NewRecorder()
	api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(body)))
	if w.Code != 404 {
		t.Fatal("cross-correlation read accepted")
	}
}

func TestLauncherContextConcurrentReadAndLimits(t *testing.T) {
	s := &Service{gameSessions: make(map[uint16]*gameSession)}
	grant := s.launcherDiagnostics.issue("game-token", strings.Repeat("a", 32), time.Now())
	game := &gameSession{connID: "game-owner", selectedCharacterID: 7, launcherCharacterID: 7, launcherCharacterName: "Fixture"}
	s.gameSessions[7] = game
	s.bindLauncherDiagnostics(game, "game-token")
	api := &accountAPIServer{service: s}
	body := `{"read_token":"` + grant.ReadToken + `","correlation_id":"` + grant.CorrelationID + `","after":0}`
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			game.characterGeneration = uint64(i + 1)
			s.captureLauncherContext(game, false)
		}
	}()
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(body)))
		if w.Code != 200 && w.Code != 429 {
			t.Fatalf("read status %d", w.Code)
		}
	}
	wg.Wait()
	w := httptest.NewRecorder()
	api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(strings.Replace(body, `"after":0`, `"after":18446744073709551615`, 1))))
	if w.Code != 400 {
		t.Fatal("future cursor accepted")
	}
	s.launcherDiagnostics.mu.Lock()
	game.launcherDiagnostic.entry.expires = time.Now().Add(-time.Second)
	s.launcherDiagnostics.mu.Unlock()
	w = httptest.NewRecorder()
	api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(body)))
	if w.Code != 404 {
		t.Fatal("expired capability accepted")
	}
	for i := 0; i < 256; i++ {
		if s.launcherDiagnostics.issue("new-token-"+strconv.Itoa(i), strings.Repeat("a", 32), time.Now()) == nil {
			t.Fatalf("early subscriber limit %d", i)
		}
	}
	if s.launcherDiagnostics.issue("overflow", strings.Repeat("b", 32), time.Now()) != nil {
		t.Fatal("subscriber limit exceeded")
	}
}

func TestLauncherContextIdleExpiry(t *testing.T) {
	s := &Service{}
	now := time.Now()
	grant := s.launcherDiagnostics.issue("idle-token", strings.Repeat("c", 32), now)
	game := &gameSession{connID: "idle-game"}
	s.bindLauncherDiagnostics(game, "idle-token")
	s.captureLauncherContext(game, false)
	e := game.launcherDiagnostic.entry
	sequence := e.sequence
	e.idleUntil = now.Add(-time.Second)
	s.captureLauncherContext(game, true)
	if e.sequence != sequence || e.snapshot.Status != "current" {
		t.Fatal("idle subscription continued collecting")
	}
	other := &gameSession{connID: "late-game"}
	s.bindLauncherDiagnostics(other, "idle-token")
	if other.launcherDiagnostic != nil {
		t.Fatal("idle subscription accepted a new game connection")
	}
	w := httptest.NewRecorder()
	api := &accountAPIServer{service: s}
	body := `{"read_token":"` + grant.ReadToken + `","correlation_id":"` + grant.CorrelationID + `","after":0}`
	api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(body)))
	if w.Code != 404 {
		t.Fatal("idle credential accepted")
	}
	if s.launcherDiagnostics.issue("fresh-token", strings.Repeat("d", 32), now) == nil || len(s.launcherDiagnostics.launches) != 1 || len(s.launcherDiagnostics.readers) != 1 {
		t.Fatal("idle subscription was not removed during issuance")
	}
}

func TestLauncherContextOptInIsolationAndBounds(t *testing.T) {
	s := &Service{gameSessions: make(map[uint16]*gameSession)}
	if s.launcherDiagnostics.issue("token", "", time.Now()) != nil {
		t.Fatal("collection without opt in")
	}
	ga := s.launcherDiagnostics.issue("a", strings.Repeat("a", 32), time.Now())
	gb := s.launcherDiagnostics.issue("b", strings.Repeat("b", 32), time.Now())
	a := &gameSession{connID: "game-a", selectedCharacterID: 1, launcherCharacterID: 1, launcherCharacterName: "Fixture A"}
	b := &gameSession{connID: "game-b", selectedCharacterID: 2, launcherCharacterID: 2, launcherCharacterName: "Fixture B"}
	s.gameSessions[1] = a
	s.gameSessions[2] = b
	s.bindLauncherDiagnostics(a, "a")
	s.bindLauncherDiagnostics(b, "b")
	for i := 0; i < 100; i++ {
		a.characterGeneration = uint64(i + 1)
		s.captureLauncherContext(a, false)
	}
	s.captureLauncherContext(b, false)
	if len(a.launcherDiagnostic.entry.events) != 64 || *b.launcherDiagnostic.entry.snapshot.Fields["character_name"].Value != "Fixture B" || ga.CorrelationID == gb.CorrelationID {
		t.Fatal("bounded per-client timeline invalid")
	}
	api := &accountAPIServer{service: s}
	for _, body := range []string{`{"read_token":"` + ga.ReadToken + `","correlation_id":"` + gb.CorrelationID + `"}`, `{"read_token":"` + strings.Repeat("f", 64) + `","correlation_id":"` + ga.CorrelationID + `"}`} {
		w := httptest.NewRecorder()
		api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(body)))
		if w.Code != 404 {
			t.Fatal("cross-client capability accepted")
		}
	}
	w := httptest.NewRecorder()
	api.handleLauncherDiagnostics(w, httptest.NewRequest("POST", "/api/diagnostics/context", strings.NewReader(`{"read_token":"`+ga.ReadToken+`","correlation_id":"`+ga.CorrelationID+`","after":0}`)))
	var reply launcherContextReply
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	if !reply.EventsLost || len(reply.Events) != 64 {
		t.Fatal("truncation not reported")
	}
}
