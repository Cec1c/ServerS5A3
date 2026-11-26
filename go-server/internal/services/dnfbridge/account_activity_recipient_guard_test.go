package dnfbridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type recipientGuardSettlement struct {
	base            repo.CharacterSettlementUnitOfWork
	failCharacterID string
	loadErr         error
}

func (s *recipientGuardSettlement) WithinCharacterSettlement(ctx context.Context, characterID string, apply func(repo.Group) error) error {
	return s.base.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		if characterID == s.failCharacterID {
			tx.Character = &recipientGuardCharacterRepository{
				CharacterRepository: tx.Character,
				loadErr:             s.loadErr,
			}
		}
		return apply(tx)
	})
}

type recipientGuardCharacterRepository struct {
	repo.CharacterRepository
	loadErr error
}

func (r *recipientGuardCharacterRepository) Load(context.Context, string) (repo.CharacterRecord, bool, error) {
	return repo.CharacterRecord{}, false, r.loadErr
}

func recipientGuardFixture(t *testing.T) (repo.Group, *Service, *gameSession, *runtimeDungeonState) {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	for _, id := range []string{"1", "2", "3"} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{AccountID: "owner-" + id, CharacterID: id}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return g, true }}
	session := &gameSession{selectedCharacterID: 1, accountID: "owner-1"}
	r := &runtimeDungeonState{
		Dungeon: worldmap.Dungeon{ID: 6062},
		activityAbyssEvent: &repo.ActivityEvent{
			ID:          strings.Repeat("a", 32) + ":1:abyss_completed:encounter",
			AccountID:   "owner-1",
			CharacterID: "1",
			Metric:      repo.ActivityAbyss,
			Amount:      1,
			OccurredAt:  time.Now(),
			DungeonID:   6062,
			Source:      "epic_road",
		},
		activityAbyssRecipients: []dungeonDropRecipient{
			{CharacterID: 1, AccountID: "owner-1"},
			{CharacterID: 2, AccountID: "foreign"},
			{CharacterID: 3, AccountID: "owner-3"},
		},
	}
	return g, s, session, r
}

// A stale or missing peer account must neither poison valid teammates'
// settlement nor acquire another account's activity. Frozen valid recipients
// remain replay-safe; a stale reporter still fails closed.
func TestAccountActivityStalePeerAccountDoesNotBlockValidRecipients(t *testing.T) {
	ctx := context.Background()
	g, s, session, r := recipientGuardFixture(t)

	for i := 0; i < 2; i++ {
		r.activityAbyssRecorded = false
		if err := s.recordCurrentActivityAbyss(session, r, worldmap.DungeonRoomScene{}, true); err != nil {
			t.Fatal(err)
		}
	}
	for account, want := range map[string]int64{
		"owner-1": 1,
		"owner-2": 0,
		"owner-3": 1,
		"foreign": 0,
	} {
		got, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: account})
		if err != nil || got.Metrics[repo.ActivityAbyss] != want {
			t.Fatalf("account=%s summary=%+v want=%d error=%v", account, got, want, err)
		}
	}

	r.activityAbyssRecorded = false
	r.activityAbyssRecipients[0].AccountID = "foreign"
	if err := s.recordCurrentActivityAbyss(session, r, worldmap.DungeonRoomScene{}, true); !errors.Is(err, repo.ErrActivityTransaction) {
		t.Fatalf("stale reporter was not rejected: %v", err)
	}

	t.Run("peer load error propagates", func(t *testing.T) {
		g, s, session, r := recipientGuardFixture(t)
		loadErr := errors.New("peer character load failed")
		g.CharacterSettlement = &recipientGuardSettlement{
			base:            g.CharacterSettlement,
			failCharacterID: "2",
			loadErr:         loadErr,
		}
		s.repositoryProvider = func() (repo.Group, bool) { return g, true }

		if err := s.recordCurrentActivityAbyss(session, r, worldmap.DungeonRoomScene{}, true); !errors.Is(err, loadErr) {
			t.Fatalf("character load error was not propagated: %v", err)
		}
		if r.activityAbyssRecorded {
			t.Fatal("partially failed settlement was marked recorded")
		}
		owner3, err := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "owner-3"})
		if err != nil {
			t.Fatal(err)
		}
		if owner3.Metrics[repo.ActivityAbyss] != 0 {
			t.Fatalf("valid recipient after the propagated error was silently skipped: %+v", owner3)
		}
	})
}
