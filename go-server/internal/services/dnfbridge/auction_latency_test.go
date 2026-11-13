package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/auction"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestAuctionClockDoesNotPostponeOrBlockSharedTimer(t *testing.T) {
	q := newFakeCurrentDungeonDeathTimerQueue()
	r := memory.NewMemoryGroup()
	s := &Service{gameplayTimers: q, repositoryProvider: func() (repo.Group, bool) { return r, true }}
	ctx, cancel := context.WithCancel(context.Background())
	s.startCurrentAuctionClock(ctx)
	s.auctionMu.Lock()
	for i := 0; i < 60; i++ {
		s.scheduleCurrentAuctionMaintenanceLocked(30 * time.Second)
	}
	if q.scheduleCount != 1 {
		s.auctionMu.Unlock()
		cancel()
		t.Fatalf("requests replaced existing deadline %d times", q.scheduleCount)
	}
	done := make(chan struct{})
	go func() { q.tasks[0].callback(time.Now()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		s.auctionMu.Unlock()
		cancel()
		t.Fatal("shared timer blocked behind auction mutex")
	}
	cancel()
	s.auctionMu.Unlock()
	s.wg.Wait()
	if _, found, err := r.Character.Load(context.Background(), auction.EscrowCharacterID); err != nil || found {
		t.Fatalf("cancelled clock created escrow %v %v", found, err)
	}
}

type auctionAttemptCharacters struct {
	repo.CharacterRepository
	attempts []string
}

func (c *auctionAttemptCharacters) Load(ctx context.Context, id string) (repo.CharacterRecord, bool, error) {
	if id != auction.EscrowCharacterID {
		c.attempts = append(c.attempts, id)
	}
	return c.CharacterRepository.Load(ctx, id)
}

func TestAuctionMaintenanceAdvancesPastFailedRecipients(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	slots := map[string]repo.ItemStack{}
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("%03d", i)
		slots["auction:payout:"+id] = repo.ItemStack{Extra: map[string]string{"seller_id": id}}
	}
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("active%03d", i)
		slots["auction:listing:"+id] = repo.ItemStack{Extra: map[string]string{"seller_id": id, "expires_at": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}}
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: auction.EscrowCharacterID, Slots: slots}); err != nil {
		t.Fatal(err)
	}
	c := &auctionAttemptCharacters{CharacterRepository: r.Character}
	r.Character = c
	s := &Service{repositoryProvider: func() (repo.Group, bool) { return r, true }}
	for i := 0; i < 2; i++ {
		pending, err := s.maintainCurrentAuctionLocked(ctx)
		if !pending || err == nil {
			t.Fatalf("invalid recipients should remain durable pending=%v err=%v", pending, err)
		}
	}
	seen := map[string]bool{}
	for _, id := range c.attempts {
		seen[id] = true
	}
	if len(c.attempts) != 16 || len(seen) != 16 {
		t.Fatalf("maintenance starved later recipients: %v", c.attempts)
	}
}
