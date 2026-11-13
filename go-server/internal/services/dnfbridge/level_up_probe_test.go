package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type bridgeRewardProbe struct {
	r.CharacterRepository
	probes, full      int
	probeErr, fullErr error
	ids               []string
}

func (p *bridgeRewardProbe) LoadLevelUpRewardProbe(ctx context.Context, id string) (r.LevelUpRewardProbe, bool, error) {
	p.probes++
	p.ids = append(p.ids, id)
	if p.probeErr != nil {
		return r.LevelUpRewardProbe{}, false, p.probeErr
	}
	c, found, err := p.CharacterRepository.Load(ctx, id)
	return r.LevelUpRewardProbe{CharacterID: c.CharacterID, AccountID: c.AccountID, Level: c.Level, Pending: r.HasPendingLevelUp(c)}, found, err
}
func (p *bridgeRewardProbe) Load(ctx context.Context, id string) (r.CharacterRecord, bool, error) {
	p.full++
	if p.fullErr != nil {
		return r.CharacterRecord{}, false, p.fullErr
	}
	return p.CharacterRepository.Load(ctx, id)
}

func (p *bridgeRewardProbe) ListPendingLevelUpCharacters(ctx context.Context, after string, limit int) ([]string, error) {
	return p.CharacterRepository.(r.PendingLevelUpReader).ListPendingLevelUpCharacters(ctx, after, limit)
}

func TestRealPVFLevelUpRewardProbeDeliveryReplayAndRecovery(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires explicit PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, session, _, _, repos := levelUpNoticeFixture(t)
	s.initialEquipmentArchive = archive
	p := &bridgeRewardProbe{CharacterRepository: repos.Character}
	repos.Character = p
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	s.afterCurrentLevelUpRewardCommand(session)
	s.afterCurrentLevelUpRewardCommand(session)
	box, found, err := repos.Mailbox.Load(context.Background(), "77")
	if err != nil || !found || len(box.Mails) != 1 || p.full != 0 || p.probes != 2 {
		t.Fatal(box, found, err, p.full, p.probes)
	}
	if err = p.CharacterRepository.Save(context.Background(), r.CharacterRecord{CharacterID: "88", AccountID: "b", Name: "Recovered", Level: 86, Stats: map[string]int64{r.LevelUpEventKey(86): 1}}); err != nil {
		t.Fatal(err)
	}
	s.recoverCurrentLevelUpRewards(context.Background())
	s.recoverCurrentLevelUpRewards(context.Background())
	box, found, err = repos.Mailbox.Load(context.Background(), "88")
	if err != nil || !found || len(box.Mails) != 1 || p.full != 0 {
		t.Fatal("recovery or replay changed", box, found, err, p.full)
	}
}
func TestLevelUpRewardProbeCommandTailWithoutEventOmitsFullRead(t *testing.T) {
	s, session, _, _, repos := levelUpNoticeFixture(t)
	c, _, _ := repos.Character.Load(context.Background(), "77")
	// Saves preserve positive upgrade facts. Explicitly mark the fixture event
	// consumed, as the real reward transaction does, rather than erasing it.
	c.Stats = map[string]int64{"irrelevant": 999, r.LevelUpEventKey(85): -1}
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	p := &bridgeRewardProbe{CharacterRepository: repos.Character, fullErr: errors.New("full read forbidden")}
	repos.Character = p
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	for i := 0; i < 3; i++ {
		s.afterCurrentLevelUpRewardCommand(session)
	}
	if p.probes != 3 || p.full != 0 || s.levelUpRewards.loaded || len(s.levelUpRewards.retryAfter) != 0 {
		t.Fatal(p.probes, p.full, s.levelUpRewards.loaded, s.levelUpRewards.retryAfter)
	}
}
func TestLevelUpRewardProbeFailureKeepsRetryAndEvent(t *testing.T) {
	s, _, _, _, repos := levelUpNoticeFixture(t)
	p := &bridgeRewardProbe{CharacterRepository: repos.Character, probeErr: errors.New("probe failed"), fullErr: errors.New("full failed")}
	repos.Character = p
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	s.processCurrentLevelUpRewards(context.Background(), "77")
	if p.probes != 1 || p.full != 1 || s.levelUpRewards.retryAfter["77"].IsZero() {
		t.Fatal(p.probes, p.full, s.levelUpRewards.retryAfter)
	}
	c, _, _ := p.CharacterRepository.Load(context.Background(), "77")
	if !r.HasPendingLevelUp(c) {
		t.Fatal("failed probe consumed event")
	}
}

func TestLevelUpRewardProbeCommandTailCoversCurrentParty(t *testing.T) {
	s, session, _, _, repos := levelUpNoticeFixture(t)
	c, _, _ := repos.Character.Load(context.Background(), "77")
	c.Stats = map[string]int64{r.LevelUpEventKey(85): -1}
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(context.Background(), r.CharacterRecord{CharacterID: "88", AccountID: "b", Level: 20}); err != nil {
		t.Fatal(err)
	}
	manager := s.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{UserID: 77, SessionGeneration: 1}, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatal(created.Reason)
	}
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{UserID: 88, SessionGeneration: 2})
	if !joined.OK {
		t.Fatal(joined.Reason)
	}
	p := &bridgeRewardProbe{CharacterRepository: repos.Character, fullErr: errors.New("full read forbidden")}
	repos.Character = p
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	s.afterCurrentLevelUpRewardCommand(session)
	if p.probes != 2 || p.full != 0 || len(p.ids) != 2 || p.ids[0] == p.ids[1] ||
		!((p.ids[0] == "77" && p.ids[1] == "88") || (p.ids[0] == "88" && p.ids[1] == "77")) {
		t.Fatal("party coverage", p.probes, p.full, p.ids)
	}
}
func TestLevelUpRewardProbeStillRevokesLuckyAtCap(t *testing.T) {
	s, session, repos := luckyEffectsTestService(t, 10)
	s.luckyCharacterCatalog.Level1To89Only = true
	c, _, _ := repos.Character.Load(context.Background(), "99")
	c.Level = 89
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentLuckyCharacterState(session); err != nil {
		t.Fatal(err)
	}
	if !session.luckyPreparedActive {
		t.Fatal("lucky fixture inactive")
	}
	c.Level = 90
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	p := &bridgeRewardProbe{CharacterRepository: repos.Character}
	repos.Character = p
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	s.afterCurrentLevelUpRewardCommand(session)
	if session.luckyPreparedActive || p.probes != 1 {
		t.Fatal("cap revocation depended on reward mail", p.probes)
	}
}
