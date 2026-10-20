package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"testing"
	"time"
)

type epicHeldAcceptSave struct {
	repo.QuestRepository
	entered, release chan struct{}
	failure          bool
}

func (q epicHeldAcceptSave) Save(ctx context.Context, v repo.QuestRecord) error {
	close(q.entered)
	select {
	case <-q.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if q.failure {
		return errors.New("actual5578 accept save fault")
	}
	return q.QuestRepository.Save(ctx, v)
}

func TestActualEpic350AcceptCanonicalReservationAndRelease(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(strconv.FormatBool(failure), func(t *testing.T) {
			s, a, b, _ := prepareMonthlyBridge(t)
			ctx := context.Background()
			g, _ := s.repositoryGroup()
			id := strconv.Itoa(int(a.selectedCharacterID))
			ch, _, _ := g.Character.Load(ctx, id)
			ch.Level = 90
			g.Character.Save(ctx, ch)
			a.townSceneReadyCharacterID = a.selectedCharacterID
			if _, e := s.loadQuestCatalog(ctx); e != nil {
				t.Fatal(e)
			}
			if !s.currentEpicRoadActivityAvailable() {
				t.Fatal("actual350 not available")
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			g.Quest = epicHeldAcceptSave{g.Quest, entered, release, failure}
			s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			req := make([]byte, 4)
			binary.LittleEndian.PutUint16(req, 31)
			binary.LittleEndian.PutUint16(req[2:], 5578)
			done := make(chan error, 1)
			go func() { done <- s.handleCurrentAcceptQuest(a, req) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("accept never reached real Save")
			}
			changed := make(chan struct{})
			go func() { s.bindGameSessionCharacter(a, 1000+a.selectedCharacterID); close(changed) }()
			select {
			case <-changed:
				t.Fatal("canonical writer bypassed durable accept reservation")
			case <-time.After(25 * time.Millisecond):
			}
			unrelated := make(chan struct{})
			go func() { s.bindGameSessionCharacter(b, b.selectedCharacterID); close(unrelated) }()
			select {
			case <-unrelated:
			case <-time.After(time.Second):
				t.Fatal("non-conflicting binding blocked")
			}
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("accept lock inversion")
			}
			select {
			case <-changed:
			case <-time.After(time.Second):
				t.Fatal("reservation leaked after accept")
			}
			record, _, _ := g.Quest.Load(ctx, id)
			_, exists := record.States[5578]
			if exists == failure {
				t.Fatalf("failure%t persisted%t", failure, exists)
			}
		})
	}
}
