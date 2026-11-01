package dnfbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCharacterEditAdmissionIsScopedAndFenced(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := admissionService(admissionFenceFixture{wait: func(ctx context.Context, account string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	session := &gameSession{accountID: "a"}
	done := make(chan error, 1)
	go func() { done <- s.admitCharacterForEdits(context.Background(), session, "a", 10) }()
	<-entered
	for _, id := range []uint16{10, 11} {
		p, err := s.administrativeCharacterEditStatus("a", id)
		if err != nil || p.Busy != (id == 10) {
			t.Fatal(p, err)
		}
	}
	select {
	case <-done:
		t.Fatal("selection crossed uncommitted GM transaction")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Removing an online-directory entry alone is not completed cleanup.
	s.gameSessions = map[uint16]*gameSession{}
	p, err := s.administrativeCharacterEditStatus("a", 10)
	if err != nil || !p.Busy {
		t.Fatal("cleanup marker lost", p, err)
	}
	s.releaseAccountEditAdmission(session)
	p, err = s.administrativeCharacterEditStatus("a", 10)
	if err != nil || p.Busy {
		t.Fatal("cleanup retained marker", p, err)
	}
}

func TestCharacterEditAdmissionFailureAndMultipleConnections(t *testing.T) {
	s := admissionService(admissionFenceFixture{wait: func(context.Context, string) error { return context.Canceled }})
	a, b := &gameSession{}, &gameSession{}
	for _, session := range []*gameSession{a, b} {
		if err := s.admitCharacterForEdits(context.Background(), session, "a", 10); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	s.releaseCharacterEditAdmission(a)
	p, err := s.administrativeCharacterEditStatus("a", 10)
	if err != nil || !p.Busy {
		t.Fatal("other connection was cleared", p, err)
	}
	s.releaseCharacterEditAdmission(b)
	p, err = s.administrativeCharacterEditStatus("a", 10)
	if err != nil || p.Busy {
		t.Fatal(p, err)
	}
	// A live compatibility binding is occupied even without a new marker.
	s.liveGameSessions = map[string]*gameSession{"other": {selectedCharacterID: 11}}
	p, err = s.administrativeCharacterEditStatus("a", 11)
	if err != nil || !p.Busy {
		t.Fatal(p, err)
	}
	p, err = s.administrativeCharacterEditStatus("a", 10)
	if err != nil || p.Busy {
		t.Fatal("sibling blocked", p, err)
	}
}

func TestGameAdminCharacterEditStatus(t *testing.T) {
	s := admissionService(admissionFenceFixture{wait: func(context.Context, string) error { return nil }})
	s.options.playerAdminSecret = strings.Repeat("x", 32)
	api := &accountAPIServer{service: s}
	for _, body := range []string{`{"version":1,"action":"character_edit_status","accountId":"a","characterId":10}`, `{"version":1,"action":"character_edit_status","accountId":"a"}`, `{"version":1,"action":"character_edit_status","accountId":" a ","characterId":10}`} {
		r := httptest.NewRequest("POST", "/api/game/admin", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+s.options.playerAdminSecret)
		w := httptest.NewRecorder()
		api.handleGameAdmin(w, r)
		var out struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.OK != (body == `{"version":1,"action":"character_edit_status","accountId":"a","characterId":10}`) {
			t.Fatal(w.Body.String())
		}
	}
}
