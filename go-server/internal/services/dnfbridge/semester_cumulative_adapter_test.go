package dnfbridge

import (
	"context"
	"encoding/hex"
	"errors"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func seedSemesterAdapter(t *testing.T) (*Service, *gameSession, *bufferConn, oe.EpicCampaign) {
	t.Helper()
	s, a, c, campaign := seedEpicBridge(t)
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = campaign.Ends.Add(-time.Hour)
	s.gameplayTimers = q
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(nil, "1")
	k := oe.SemesterJournalKey(campaign)
	ch.Stats[k+":day_bitmap"] = (1 << 24) - 1
	ch.Stats[k+":qualified_days"] = 24
	ch.Stats[k+":version"] = 1
	if e := g.Character.Save(nil, ch); e != nil {
		t.Fatal(e)
	}
	return s, a, c, campaign
}
func TestSemesterAdapterDetachedACKNativeContract(t *testing.T) {
	for success, want := range map[bool]string{true: "01d8000000", false: "0001d800000001"} {
		if got := hex.EncodeToString(buildCurrentSemesterCumulativeACK(success)); got != want {
			t.Fatal(got, want)
		}
	}
}
func TestSemesterAdapterActualCatalogClaimPublishReplay(t *testing.T) {
	s, a, c, campaign := seedSemesterAdapter(t)
	identity, ok := s.boundGameSessionCharacterSnapshot(a)
	if !ok {
		t.Fatal("owner")
	}
	state, e := s.claimCurrentSemesterCumulative(a, 0)
	if e != nil {
		t.Fatal(e)
	}
	if len(c.write.Bytes()) != 0 {
		t.Fatal("domain emitted before explicitpublish")
	}
	if state.Claimed != 1 || len(state.MailIDs) != 1 {
		t.Fatal(state)
	}
	if e = s.publishCurrentSemesterCumulative(a, identity, state); e != nil {
		t.Fatal(e)
	}
	ack, rest := splitGameServerUpperPacket(t, c.write.Bytes())
	projection, rest := splitGameServerUpperPacket(t, rest)
	if ack.Header.MsgID != 689 || hex.EncodeToString(ack.Body) != "01d8000000" || projection.Header.MsgID != 654 || hex.EncodeToString(projection.Body) != "1818000700000001000000" || len(rest) == 0 {
		t.Fatal(ack, projection, len(rest))
	}
	g, _ := s.repositoryGroup()
	box, _, _ := g.Mailbox.Load(nil, "1")
	if len(box.Mails) != 1 || len(box.Mails[state.MailIDs[0]].Attachments) != 1 || box.Mails[state.MailIDs[0]].Attachments[0].ItemID != 490010219 || !box.Mails[state.MailIDs[0]].Attachments[0].ExpireAt.After(s.gameplayNow()) {
		t.Fatal(box)
	}
	again, e := s.claimCurrentSemesterCumulative(a, 0)
	if e != nil || len(again.MailIDs) != 0 {
		t.Fatal(again, e)
	}
	ch, _, _ := g.Character.Load(nil, "1")
	if ch.Stats[oe.SemesterJournalKey(campaign)+":day_bitmap"] != (1<<24)-1 {
		t.Fatal("journal lost")
	}
}

func TestSemesterAdapterLateAbortAndCanonicalStaleRollback(t *testing.T) {
	for _, mode := range []string{"lateabort", "generation"} {
		t.Run(mode, func(t *testing.T) {
			s, a, _, campaign := seedSemesterAdapter(t)
			g, _ := s.repositoryGroup()
			base := g.MailboxAssets
			if mode == "lateabort" {
				g.MailboxAssets = epicBridgeAbortMailbox{base, errors.New("semesterafterwrites")}
			} else {
				g.MailboxAssets = semesterStaleMailbox{base, s, a}
			}
			s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			if _, e := s.claimCurrentSemesterCumulative(a, 0); e == nil {
				t.Fatal("accepted")
			}
			ch, _, _ := g.Character.Load(nil, "1")
			box, _, _ := g.Mailbox.Load(nil, "1")
			if len(box.Mails) != 0 || ch.Stats[oe.SemesterJournalKey(campaign)+":claim_bitmap"] != 0 {
				t.Fatal(ch, box)
			}
			s.mu.Lock()
			remaining := len(s.characterAssetReservations)
			s.mu.Unlock()
			if remaining != 0 {
				t.Fatal("reservation leak")
			}
		})
	}
}

type semesterStaleMailbox struct {
	repo.MailboxAssetUnitOfWork
	s *Service
	a *gameSession
}

func (u semesterStaleMailbox) WithinMailboxAssets(ctx context.Context, a, b string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, a, b, func(chars repo.CharacterRepository, inv repo.InventoryRepository, mail repo.MailboxRepository) error {
		return fn(chars, inv, semesterStaleMailStore{mail, u.s, u.a})
	})
}

type semesterStaleMailStore struct {
	repo.MailboxRepository
	s *Service
	a *gameSession
}

func (m semesterStaleMailStore) Save(ctx context.Context, box repo.MailboxRecord) error {
	e := m.MailboxRepository.Save(ctx, box)
	m.s.mu.Lock()
	m.a.characterGeneration++
	m.s.mu.Unlock()
	return e
}

func TestSemesterAdapterCeraUOWBindingValidationNoLockInversion(t *testing.T) {
	s, a, _, _ := seedSemesterAdapter(t)
	g, _ := s.repositoryGroup()
	identity, _ := s.boundGameSessionCharacterSnapshot(a)
	boundary := &epicClaimMailboxBoundary{g.MailboxAssets, make(chan struct{}), make(chan struct{}), make(chan struct{})}
	g.MailboxAssets = boundary
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	done := make(chan error, 1)
	go func() { _, e := s.claimCurrentSemesterCumulative(a, 0); done <- e }()
	select {
	case <-boundary.entered:
	case <-time.After(time.Second):
		t.Fatal("claim boundary")
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	other := make(chan error, 1)
	go func() {
		other <- g.CeraShopAssets.WithinCeraShopAssets(nil, a.accountID, "1", oe.EpicCampaignScope, func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error {
			close(entered)
			<-resume
			if !s.boundGameSessionCharacterCurrent(identity) {
				return errors.New("binding")
			}
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Cera callback")
	}
	close(boundary.resume)
	<-boundary.attempt
	close(resume)
	select {
	case e := <-other:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("Cera deadlock")
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("claim deadlock")
	}
}

func TestSemesterAdapterMissingCampaignCannotActivate(t *testing.T) {
	s, a, _, _ := prepareMonthlyBridge(t)
	g, _ := s.repositoryGroup()
	before, found, e := oe.LoadEpicCampaign(nil, g.Settings)
	if e != nil || found {
		t.Fatal(before, found, e)
	}
	if _, e := s.claimCurrentSemesterCumulative(a, 0); e == nil {
		t.Fatal("created campaign")
	}
	_, found, e = oe.LoadEpicCampaign(nil, g.Settings)
	if e != nil || found || monthlyMailCount(t, s, "1") != 0 {
		t.Fatal(found, e)
	}
}
func TestSemesterAdapterSamplesNowAfterReservationWait(t *testing.T) {
	s, a, _, campaign := seedSemesterAdapter(t)
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	q.mu.Lock()
	q.now = campaign.Starts.AddDate(0, 0, 6).Add(time.Hour)
	q.mu.Unlock()
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(nil, "1")
	key := oe.SemesterJournalKey(campaign)
	ch.Stats[key+":day_bitmap"] = (1 << 8) - 1
	ch.Stats[key+":qualified_days"] = 8
	if e := g.Character.Save(nil, ch); e != nil {
		t.Fatal(e)
	}
	identity, _ := s.boundGameSessionCharacterSnapshot(a)
	prior, e := s.reserveCurrentCharacterAssets(nil, identity, a.accountID)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := s.claimCurrentSemesterCumulative(a, 0); done <- e }()
	select {
	case e := <-done:
		t.Fatal("did not wait", e)
	case <-time.After(100 * time.Millisecond):
	}
	q.mu.Lock()
	q.now = campaign.Starts.AddDate(0, 0, 7).Add(time.Hour)
	q.mu.Unlock()
	s.releaseCurrentCharacterAssets(prior)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal("used old pre-reservation date", e)
		}
	case <-time.After(time.Second):
		t.Fatal("reservation wait deadlock")
	}
}
