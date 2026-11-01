package dnfbridge

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adminapi"
)

func TestGameAdminOnlineSnapshot(t *testing.T) {
	s := &Service{options: options{playerAdminSecret: strings.Repeat("x", 32)}, gameSessions: map[uint16]*gameSession{}}
	s.gameSessions[20] = &gameSession{selectedCharacterID: 20, characterGeneration: 7}
	s.gameSessions[10] = &gameSession{selectedCharacterID: 10, characterGeneration: 3}
	s.gameSessions[30] = nil
	api := &accountAPIServer{service: s}
	call := func(body, secret string) (int, adminapi.Response, string) {
		r := httptest.NewRequest("POST", adminapi.Path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		api.handleGameAdmin(w, r)
		var result adminapi.Response
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return w.Code, result, w.Body.String()
	}
	body := `{"version":1,"action":"online_snapshot"}`
	if status, _, _ := call(body, ""); status != 404 {
		t.Fatal(status)
	}
	status, out, raw := call(body, strings.Repeat("x", 32))
	if status != 200 || !out.OK || out.OnlineSnapshot.Validate() != nil {
		t.Fatal(status, raw)
	}
	snap := out.OnlineSnapshot
	if len(snap.Members) != 2 || snap.Members[0].CharacterID != 10 || snap.Members[1].Generation != 7 || time.Since(snap.ObservedAt) > time.Second {
		t.Fatal(snap)
	}
	for _, forbidden := range []string{"accountID", "connID", "token", "address"} {
		if strings.Contains(raw, forbidden) {
			t.Fatal("session detail leak")
		}
	}
	s.mu.Lock()
	s.gameSessions[10] = &gameSession{selectedCharacterID: 10, characterGeneration: 4}
	delete(s.gameSessions, 20)
	s.mu.Unlock()
	_, next, _ := call(body, strings.Repeat("x", 32))
	if next.OnlineSnapshot.ID == snap.ID || next.OnlineSnapshot.Members[0].Generation != 4 || snap.Members[0].Generation != 3 || len(snap.Members) != 2 {
		t.Fatal("snapshot aliases live state")
	}
	_, invalid, _ := call(`{"version":1,"action":"online_snapshot","characterId":10}`, strings.Repeat("x", 32))
	if invalid.OK || invalid.OnlineSnapshot != nil {
		t.Fatal("extraneous target accepted")
	}
	s.gameSessions[10].characterGeneration = 0
	_, invalid, _ = call(body, strings.Repeat("x", 32))
	if invalid.OK || invalid.OnlineSnapshot != nil {
		t.Fatal("invalid identity returned partial snapshot")
	}
	s.gameSessions = map[uint16]*gameSession{}
	_, empty, _ := call(body, strings.Repeat("x", 32))
	if empty.OnlineSnapshot.Validate() != nil || len(empty.OnlineSnapshot.Members) != 0 {
		t.Fatal("empty snapshot")
	}
	for i := 1; i <= adminapi.MaxOnlineSnapshotMembers; i++ {
		id := uint16(i)
		s.gameSessions[id] = &gameSession{selectedCharacterID: id, characterGeneration: uint64(i)}
	}
	_, maximum, raw := call(body, strings.Repeat("x", 32))
	if !maximum.OK || len(maximum.OnlineSnapshot.Members) != adminapi.MaxOnlineSnapshotMembers || len(raw) > 512<<10 {
		t.Fatal("maximum snapshot", len(raw))
	}
	id := uint16(adminapi.MaxOnlineSnapshotMembers + 1)
	s.gameSessions[id] = &gameSession{selectedCharacterID: id, characterGeneration: 1}
	_, overflow, _ := call(body, strings.Repeat("x", 32))
	if overflow.OK || overflow.OnlineSnapshot != nil {
		t.Fatal("overflow truncated")
	}
}

func TestGameAdminOnlineSnapshotConcurrentReplacement(t *testing.T) {
	s := &Service{gameSessions: map[uint16]*gameSession{}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 1; n <= 500; n++ {
			s.mu.Lock()
			s.gameSessions[10] = &gameSession{selectedCharacterID: 10, characterGeneration: uint64(n)}
			delete(s.gameSessions, 10)
			s.mu.Unlock()
		}
	}()
	for range 500 {
		snapshot, err := s.administrativeOnlineSnapshot()
		if err != nil || snapshot.Validate() != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
