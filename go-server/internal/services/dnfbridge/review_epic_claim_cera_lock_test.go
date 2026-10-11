package dnfbridge

import (
	"context"
	"errors"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

type epicClaimMailboxBoundary struct {
	repo.MailboxAssetUnitOfWork
	entered, resume, attempt chan struct{}
}

func (u *epicClaimMailboxBoundary) WithinMailboxAssets(ctx context.Context, a, b string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	close(u.entered)
	<-u.resume
	close(u.attempt)
	return u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, a, b, fn)
}
func TestReviewExisting350ClaimDoesNotInvertCeraBindingLock(t *testing.T) {
	s, a, _, _ := seedEpicBridge(t)
	g, _ := s.repositoryGroup()
	identity, ok := s.boundGameSessionCharacterSnapshot(a)
	if !ok {
		t.Fatal("missing binding")
	}
	boundary := &epicClaimMailboxBoundary{g.MailboxAssets, make(chan struct{}), make(chan struct{}), make(chan struct{})}
	modified := g
	modified.MailboxAssets = boundary
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	claimDone := make(chan error, 1)
	go func() { _, e := s.claimCurrentEpicReward(a, 0); claimDone <- e }()
	select {
	case <-boundary.entered:
	case <-time.After(time.Second):
		t.Fatal("existing claim physical boundary not reached")
	}
	ceraEntered, validate := make(chan struct{}), make(chan struct{})
	ceraDone := make(chan error, 1)
	go func() {
		ceraDone <- g.CeraShopAssets.WithinCeraShopAssets(context.Background(), a.accountID, "1", oe.EpicCampaignScope, func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error {
			close(ceraEntered)
			<-validate
			if !s.boundGameSessionCharacterCurrent(identity) {
				return errors.New("binding changed")
			}
			return nil
		})
	}()
	select {
	case <-ceraEntered:
	case <-time.After(time.Second):
		t.Fatal("Cera physical transaction not reached")
	}
	close(boundary.resume)
	<-boundary.attempt
	close(validate)
	select {
	case e := <-ceraDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("existing350 claim service/asset lock inversion")
	}
	select {
	case e := <-claimDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("claim did not resume")
	}
	if monthlyMailCount(t, s, "1") != 1 {
		t.Fatal("existing claim did not grant exactly one real resource mail")
	}
	s.mu.Lock()
	n := len(s.characterAssetReservations)
	s.mu.Unlock()
	if n != 0 {
		t.Fatal("claim leaked reservation")
	}
}
