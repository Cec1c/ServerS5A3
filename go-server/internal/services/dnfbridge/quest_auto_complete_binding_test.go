package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func bindAutoCompleteMainTestSession(s *Service, p *gameSession) {
	p.accountID = defaultAccountPrefix + "1"
	p.loginPacketSeen = true
	p.characterGeneration = 1
	s.gameSessions = map[uint16]*gameSession{p.selectedCharacterID: p}
}

type autoCompleteObserveWriteConn struct {
	*bufferConn
	first func()
}

func (c *autoCompleteObserveWriteConn) Write(body []byte) (int, error) {
	if c.first != nil {
		observe := c.first
		c.first = nil
		observe()
	}
	return c.bufferConn.Write(body)
}

func TestCurrentAutoCompleteMainQuestReleasesReservationBeforeACK(t *testing.T) {
	s, p, g := newQuestClearCouponTestService(t, 1)
	bindAutoCompleteMainTestSession(s, p)
	written := false
	c := &autoCompleteObserveWriteConn{bufferConn: &bufferConn{}, first: func() {
		s.mu.Lock()
		remaining := len(s.characterAssetReservations)
		s.mu.Unlock()
		if remaining != 0 {
			t.Fatal("reservation held during socket I/O")
		}
		written = true
	}}
	p.conn = c
	if err := s.handleCurrentAutoCompleteMainQuest(p, []byte{100, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Fatal("no completion response")
	}
	ack, _ := splitGameServerUpperPacket(t, c.write.Bytes())
	if ack.Header.MsgID != 1429 || len(ack.Body) != 13 || ack.Body[12] != 1 {
		t.Fatalf("success ACK=%+v %x", ack.Header, ack.Body)
	}
	q, found, err := g.Quest.Load(context.Background(), "17")
	if err != nil || !found || q.States[100].Status != "completed" {
		t.Fatal("completion missing", err)
	}
}

type autoCompleteReplaceSettlement struct {
	repo.CharacterSettlementUnitOfWork
	before func()
}

func (u autoCompleteReplaceSettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(repo.Group) error) error {
	u.before()
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, apply)
}

func TestCurrentAutoCompleteMainQuestRevalidatesAfterTransactionWait(t *testing.T) {
	s, p, g := newQuestClearCouponTestService(t, 1)
	bindAutoCompleteMainTestSession(s, p)
	before, _, err := g.Quest.Load(context.Background(), "17")
	if err != nil {
		t.Fatal(err)
	}
	replaced := false
	g.CharacterSettlement = autoCompleteReplaceSettlement{CharacterSettlementUnitOfWork: g.CharacterSettlement, before: func() {
		s.mu.Lock()
		// Inject a stale generation at the transaction boundary. Canonical
		// binding writers additionally wait for the reservation to complete.
		s.gameSessions[17] = &gameSession{selectedCharacterID: 17, characterGeneration: 2}
		s.mu.Unlock()
		replaced = true
	}}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	if err = s.handleCurrentAutoCompleteMainQuest(p, []byte{100, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	after, _, err := g.Quest.Load(context.Background(), "17")
	if !replaced || err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("stale transaction changed quests: replaced=%v err=%v", replaced, err)
	}
	if p.conn.(*bufferConn).write.Len() != 0 || len(s.characterAssetReservations) != 0 {
		t.Fatal("stale projection or leaked reservation")
	}
}

func TestCurrentAutoCompleteMainQuestRejectsUnownedOrStaleBinding(t *testing.T) {
	for _, condition := range []string{"unbound", "replaced_session", "generation_zero", "wrong_account", "not_logged_in"} {
		t.Run(condition, func(t *testing.T) {
			s, p, repos := newQuestClearCouponTestService(t, 1)
			bindAutoCompleteMainTestSession(s, p)
			switch condition {
			case "unbound":
				delete(s.gameSessions, p.selectedCharacterID)
			case "replaced_session":
				s.gameSessions[p.selectedCharacterID] = &gameSession{selectedCharacterID: p.selectedCharacterID, characterGeneration: 2}
			case "generation_zero":
				p.characterGeneration = 0
			case "wrong_account":
				p.accountID = "different-account"
			case "not_logged_in":
				p.loginPacketSeen = false
			}
			before, _, err := repos.Quest.Load(context.Background(), "17")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.handleCurrentAutoCompleteMainQuest(p, []byte{0, 0, 0, 0}); err != nil {
				t.Fatal(err)
			}
			after, _, err := repos.Quest.Load(context.Background(), "17")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("quest changed: err=%v before=%+v after=%+v", err, before, after)
			}
			if len(p.conn.(*bufferConn).write.Bytes()) != 0 {
				t.Fatal("stale binding received quest projection")
			}
			if len(s.characterAssetReservations) != 0 {
				t.Fatal("asset reservation leaked")
			}
		})
	}
}
