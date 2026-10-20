package dnfbridge

import (
	"context"
	"errors"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

type semesterNotifyUOW struct {
	repo.SemesterClearUnitOfWork
	entered chan struct{}
}

func (u *semesterNotifyUOW) WithinSemesterClear(ctx context.Context, source repo.SemesterClearOwner, owners []repo.SemesterClearOwner, scope string, apply func(repo.Group) error) error {
	close(u.entered)
	return u.SemesterClearUnitOfWork.WithinSemesterClear(ctx, source, owners, scope, apply)
}

func TestNewSemesterClearDoesNotInvertCeraBindingLock(t *testing.T) {
	s, a, r, _ := prepareSemesterActualCompleted(t)
	identity, ok := s.boundGameSessionCharacterSnapshot(a)
	if !ok {
		t.Fatal("missing actual binding")
	}
	g, _ := s.repositoryGroup()
	txEntered := make(chan struct{})
	validateBinding := make(chan struct{})
	ceraDone := make(chan error, 1)
	go func() {
		ceraDone <- g.CeraShopAssets.WithinCeraShopAssets(context.Background(), a.accountID, "99", oe.EpicCampaignScope, func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error {
			close(txEntered)
			<-validateBinding
			if !s.boundGameSessionCharacterCurrent(identity) {
				return errors.New("binding changed")
			}
			return nil
		})
	}()
	select {
	case <-txEntered:
	case <-time.After(time.Second):
		t.Fatal("Cera physical boundary not reached")
	}
	journalEntered := make(chan struct{})
	modified := g
	modified.SemesterClear = &semesterNotifyUOW{g.SemesterClear, journalEntered}
	s.repositoryProvider = func() (repo.Group, bool) { return modified, true }
	journalDone := make(chan error, 1)
	go func() {
		a.dungeon.mu.Lock()
		defer a.dungeon.mu.Unlock()
		r.clearMapCompletionPhaseAPersisted = false
		journalDone <- s.persistCurrentDungeonClearMapCompletionPhaseA(context.Background(), a, r, r.Session.Snapshot())
	}()
	select {
	case <-journalEntered:
	case <-time.After(time.Second):
		t.Fatal("journal UOW boundary not reached")
	}
	close(validateBinding)
	select {
	case e := <-ceraDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("lock inversion: Cera aggregateTxMu -> service.mu while journal service.mu -> aggregateTxMu")
	}
	select {
	case e := <-journalDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("journal did not recover after Cera owner validation")
	}
}
