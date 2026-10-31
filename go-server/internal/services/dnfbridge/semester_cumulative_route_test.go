package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func enableSemesterRoute(t *testing.T, s *Service, enabled bool) {
	t.Helper()
	g, _ := s.repositoryGroup()
	c, e := oe.NewControls(context.Background(), g.Settings, []oe.ControlDefinition{{ID: 350, Name: "epic", DefaultEnabled: true}, {ID: 216, Name: "semester", DefaultEnabled: enabled}})
	if e != nil {
		t.Fatal(e)
	}
	s.activityControls = c
}
func semesterRouteBody(index uint32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, 216)
	binary.LittleEndian.PutUint32(b[4:], index)
	return b
}
func TestSemesterRouteDefaultOFFAndNon216Compatibility(t *testing.T) {
	s, a, c, _ := seedSemesterAdapter(t)
	if e := s.initializeActivityControls(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"default", "nil", "explicitoff"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "nil" {
				s.activityControls = nil
			}
			if mode == "explicitoff" {
				enableSemesterRoute(t, s, false)
			}
			if s.currentSemesterCumulativeEnabled() {
				t.Fatal("default enabled")
			}
			h, e := s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0))
			if h || e != nil {
				t.Fatal(h, e)
			}
			id, _ := s.boundGameSessionCharacterSnapshot(a)
			if s.sendCurrentSemesterCumulative(a, id) == nil {
				t.Fatal("OFF projection")
			}
			if e = s.armCurrentSemesterCumulativeTick(a); e != nil {
				t.Fatal(e)
			}
			if len(c.write.Bytes()) != 0 || monthlyMailCount(t, s, "1") != 0 {
				t.Fatal("OFF writes")
			}
		})
	}
	enableSemesterRoute(t, s, true)
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b, 222)
	h, e := s.handleCurrentSemesterCumulativeRequest(a, b)
	if h || e != nil {
		t.Fatal(h, e)
	}
	if e = s.handleUpperCreatePostState(a, b); e != nil {
		t.Fatal(e)
	}
	p, _ := splitGameServerUpperPacket(t, c.write.Bytes())
	if p.Header.MsgID != 689 || hex.EncodeToString(p.Body) != "01de000000" {
		t.Fatal(p)
	}
}

func TestSemesterRouteActualDispatchClassAndFraming(t *testing.T) {
	for _, mode := range []string{"class0", "short", "long", "index", "unauth", "success"} {
		t.Run(mode, func(t *testing.T) {
			s, a, c, _ := seedSemesterAdapter(t)
			enableSemesterRoute(t, s, true)
			b := semesterRouteBody(0)
			class := uint8(1)
			switch mode {
			case "class0":
				class = 0
			case "short":
				b = b[:7]
			case "long":
				b = append(b, 0)
			case "index":
				b = semesterRouteBody(3)
			case "unauth":
				a.accountFromToken = false
			}
			raw, e := dnfproto.BuildChannelPacket(689, b, 0, class)
			if e != nil {
				t.Fatal(e)
			}
			// The client-request builder normalizes0 to1. This negative control
			// explicitly sets the real header byte; checksum starts at offset11.
			if mode == "class0" {
				raw[0] = 0
			}
			if e = s.handleGameUpper(a, raw); e != nil {
				t.Fatal(e)
			}
			if mode == "success" {
				p, rest := splitGameServerUpperPacket(t, c.write.Bytes())
				state, _ := splitGameServerUpperPacket(t, rest)
				if p.Body[0] != 1 || state.Header.MsgID != 654 || monthlyMailCount(t, s, "1") != 1 {
					t.Fatal(p, state)
				}
			} else if mode == "index" {
				p, _ := splitGameServerUpperPacket(t, c.write.Bytes())
				if hex.EncodeToString(p.Body) != "0001d800000001" || monthlyMailCount(t, s, "1") != 0 {
					t.Fatal(p)
				}
			} else if len(c.write.Bytes()) != 0 || monthlyMailCount(t, s, "1") != 0 {
				t.Fatal("bad request committed/emitted")
			}
		})
	}
}

type semesterRouteUnknownCommit struct{ repo.MailboxAssetUnitOfWork }

func (u semesterRouteUnknownCommit) WithinMailboxAssets(ctx context.Context, a, b string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	if e := u.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, a, b, fn); e != nil {
		return e
	}
	return errors.New("commit outcome unknown after durable write")
}
func TestSemesterRouteUnknownCommitDurableReconcileAndReplay(t *testing.T) {
	s, a, c, campaign := seedSemesterAdapter(t)
	enableSemesterRoute(t, s, true)
	g, _ := s.repositoryGroup()
	base := g.MailboxAssets
	g.MailboxAssets = semesterRouteUnknownCommit{base}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	h, e := s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0))
	if !h || e != nil {
		t.Fatal(h, e)
	}
	p, rest := splitGameServerUpperPacket(t, c.write.Bytes())
	state, rest := splitGameServerUpperPacket(t, rest)
	if p.Body[0] != 0 || state.Header.MsgID != 654 || binary.LittleEndian.Uint32(state.Body[7:]) != 1 || len(rest) == 0 || monthlyMailCount(t, s, "1") != 1 {
		t.Fatal(p, state, len(rest))
	}
	ch, _, _ := g.Character.Load(context.Background(), "1")
	if ch.Stats[oe.SemesterJournalKey(campaign)+":claim_bitmap"] != 1 {
		t.Fatal(ch)
	}
	g.MailboxAssets = base
	c.write.Reset()
	if _, e = s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0)); e != nil {
		t.Fatal(e)
	}
	p, _ = splitGameServerUpperPacket(t, c.write.Bytes())
	if p.Body[0] != 1 || monthlyMailCount(t, s, "1") != 1 {
		t.Fatal("replay duplicated", p)
	}
}

func TestSemesterRouteLateAbortAndIdentityReplacement(t *testing.T) {
	for _, mode := range []string{"abort", "generation", "staleidentity", "foreignaccount"} {
		t.Run(mode, func(t *testing.T) {
			s, a, c, campaign := seedSemesterAdapter(t)
			enableSemesterRoute(t, s, true)
			g, _ := s.repositoryGroup()
			id, _ := s.boundGameSessionCharacterSnapshot(a)
			switch mode {
			case "abort":
				g.MailboxAssets = epicBridgeAbortMailbox{g.MailboxAssets, errors.New("after callback rollback")}
			case "generation":
				g.MailboxAssets = semesterStaleMailbox{g.MailboxAssets, s, a}
			case "staleidentity":
				s.mu.Lock()
				a.characterGeneration++
				s.mu.Unlock()
			case "foreignaccount":
				ch, _, _ := g.Character.Load(nil, "1")
				ch.AccountID = "foreign-owner"
				if e := g.Character.Save(nil, ch); e != nil {
					t.Fatal(e)
				}
			}
			s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			if mode == "staleidentity" {
				if _, e := s.claimCurrentSemesterCumulativeForIdentity(a, id, 0); e == nil {
					t.Fatal("old identity accepted")
				}
			} else {
				if _, e := s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0)); e != nil {
					t.Fatal(e)
				}
			}
			ch, _, _ := g.Character.Load(nil, "1")
			if monthlyMailCount(t, s, "1") != 0 || ch.Stats[oe.SemesterJournalKey(campaign)+":claim_bitmap"] != 0 {
				t.Fatal("owner/rollback")
			}
			if mode == "generation" && len(c.write.Bytes()) != 0 {
				t.Fatal("published to changed owner")
			}
		})
	}
}

type semesterRouteMissingCampaign struct {
	repo.SettingsRepository
	writes int
}

func (u *semesterRouteMissingCampaign) Load(ctx context.Context, scope string) (repo.SettingsRecord, bool, error) {
	if scope == oe.EpicCampaignScope {
		return repo.SettingsRecord{}, false, nil
	}
	return u.SettingsRepository.Load(ctx, scope)
}
func (u *semesterRouteMissingCampaign) Save(ctx context.Context, r repo.SettingsRecord) error {
	u.writes++
	return u.SettingsRepository.Save(ctx, r)
}
func TestSemesterRouteClaimProjectionDoNotActivateCampaign(t *testing.T) {
	s, a, _, _ := seedSemesterAdapter(t)
	enableSemesterRoute(t, s, true)
	g, _ := s.repositoryGroup()
	missing := &semesterRouteMissingCampaign{SettingsRepository: g.Settings}
	g.Settings = missing
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	id, _ := s.boundGameSessionCharacterSnapshot(a)
	if s.sendCurrentSemesterCumulative(a, id) == nil {
		t.Fatal("missing campaign projected")
	}
	if _, e := s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0)); e != nil {
		t.Fatal(e)
	}
	if missing.writes != 0 || monthlyMailCount(t, s, "1") != 0 {
		t.Fatal("first claim activation")
	}
}

func TestSemesterRouteActualEightNaturalDaysAndThreshold(t *testing.T) {
	s, a, campaign := prepareSemesterActualReady(t)
	enableSemesterRoute(t, s, true)
	g, _ := s.repositoryGroup()
	q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	for day := 0; day < 8; day++ {
		if day > 0 {
			q.mu.Lock()
			q.now = q.now.Add(24 * time.Hour)
			q.mu.Unlock()
		}
		completeSemesterActualRun(t, s, a)
		ch, _, _ := g.Character.Load(nil, "99")
		if ch.Stats[semesterQualifiedKey(campaign)] != int64(day+1) {
			t.Fatal("natural journal", day, ch)
		}
		if day == 6 {
			if _, e := s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0)); e != nil {
				t.Fatal(e)
			}
			if monthlyMailCount(t, s, "99") != 0 {
				t.Fatal("7 day reward")
			}
		}
	}
	if _, e := s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0)); e != nil {
		t.Fatal(e)
	}
	if monthlyMailCount(t, s, "99") != 1 {
		t.Fatal("8 day reward missing")
	}
	if _, e := s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0)); e != nil {
		t.Fatal(e)
	}
	if monthlyMailCount(t, s, "99") != 1 {
		t.Fatal("duplicate reward")
	}
	ch, _, _ := g.Character.Load(nil, "99")
	if ch.Stats[semesterQualifiedKey(campaign)] != 8 || ch.Stats[oe.SemesterJournalKey(campaign)+":day_bitmap"] != 255 || ch.Stats[oe.SemesterJournalKey(campaign)+":claim_bitmap"] != 1 {
		t.Fatal(ch)
	}
}

func TestSemesterRouteBootstrapAndTimerLifecycle(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled-%t", enabled), func(t *testing.T) {
			s, a, c, _ := seedSemesterAdapter(t)
			enableSemesterRoute(t, s, enabled)
			s.options.joustEventDisabled = true
			catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
			if _, e := s.sendCurrentSpendTimeProtocolState(a, catalog, 0, 0, true); e != nil {
				t.Fatal(e)
			}
			data := c.write.Bytes()
			n := 0
			for len(data) > 0 {
				p, rest := splitGameServerUpperPacket(t, data)
				data = rest
				if p.Header.MsgID == 654 {
					n++
					if len(p.Body) != 11 || p.Body[0] != 24 {
						t.Fatal(p)
					}
				}
			}
			q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
			key := "dnf-semester216:" + a.connID
			q.mu.Lock()
			task := q.active[key]
			q.mu.Unlock()
			if enabled {
				if n != 1 || task == nil || task.delay <= 0 || task.delay > 30*time.Second {
					t.Fatal(n, task)
				}
				s.cancelCurrentSemesterCumulativeTick(a)
				q.mu.Lock()
				task = q.active[key]
				q.mu.Unlock()
				if task != nil {
					t.Fatal("timer not canceled")
				}
			} else if n != 0 || task != nil {
				t.Fatal("OFF bootstrap/timer", n, task)
			}
		})
	}
}

func TestSemesterRouteConcurrentReservationKeepsOneClaim(t *testing.T) {
	s, a, _, _ := seedSemesterAdapter(t)
	enableSemesterRoute(t, s, true)
	id, _ := s.boundGameSessionCharacterSnapshot(a)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.claimCurrentSemesterCumulativeForIdentity(a, id, 0); results <- e }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reservation deadlock")
	}
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	if monthlyMailCount(t, s, "1") != 1 {
		t.Fatal("duplicate concurrent mail")
	}
	s.mu.Lock()
	left := len(s.characterAssetReservations)
	s.mu.Unlock()
	if left != 0 {
		t.Fatal("reservation leak")
	}
}

func TestSemesterRouteProjectionEndsReservationBeforeSlowSocket(t *testing.T) {
	s, a, _, _ := seedSemesterAdapter(t)
	enableSemesterRoute(t, s, true)
	id, _ := s.boundGameSessionCharacterSnapshot(a)
	slow := &epicReservationSlowConn{started: make(chan struct{}), resume: make(chan struct{})}
	a.conn = slow
	done := make(chan error, 1)
	go func() { done <- s.sendCurrentSemesterCumulative(a, id) }()
	select {
	case <-slow.started:
	case <-time.After(5 * time.Second):
		t.Fatal("projection not reached")
	}
	s.mu.Lock()
	n := len(s.characterAssetReservations)
	s.mu.Unlock()
	if n != 0 {
		close(slow.resume)
		t.Fatal("socket kept read reservation")
	}
	replacement := &gameSession{selectedCharacterID: id.character, characterGeneration: id.generation + 1, accountID: a.accountID}
	promoted := make(chan struct{})
	go func() { s.promoteResidentGameSession(replacement, id.character); close(promoted) }()
	select {
	case <-promoted:
	case <-time.After(time.Second):
		close(slow.resume)
		t.Fatal("bind blocked by socket")
	}
	close(slow.resume)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("socket did not finish")
	}
}

func TestSemesterRouteAllExisting689DiscriminatorsRemainUnchanged(t *testing.T) {
	s, a, c, _ := seedSemesterAdapter(t)
	enableSemesterRoute(t, s, true)
	for event, size := range map[uint32]int{222: 16, 349: 12, 2099: 16, 2208: 16, 2384: 16, 2427: 12, 2428: 8, 2443: 4} {
		b := make([]byte, size)
		binary.LittleEndian.PutUint32(b, event)
		c.write.Reset()
		raw, e := dnfproto.BuildChannelPacket(689, b, 0, 1)
		if e != nil {
			t.Fatal(e)
		}
		// Preserve the full existing dispatcher too: owner2208 is already
		// intercepted by weekly and intentionally has no unproved ACK689.
		enableSemesterRoute(t, s, false)
		a.upperSeq = 0
		if e = s.handleGameUpper(a, raw); e != nil {
			t.Fatal(event, e)
		}
		before := append([]byte(nil), c.write.Bytes()...)
		c.write.Reset()
		enableSemesterRoute(t, s, true)
		// Compare at the same legitimate sequence; otherwise a second send
		// changes seq/checksum even when every response field is identical.
		a.upperSeq = 0
		if e = s.handleGameUpper(a, raw); e != nil {
			t.Fatal(event, e)
		}
		if !bytes.Equal(before, c.write.Bytes()) {
			t.Fatal("changed existing full dispatcher", event, before, c.write.Bytes())
		}
		if handled, e := s.handleCurrentSemesterCumulativeRequest(a, b); handled || e != nil {
			t.Fatal("claimed non216", event, handled, e)
		}
	}
	if monthlyMailCount(t, s, "1") != 0 {
		t.Fatal("other event claimed216")
	}
}

func TestSemesterRouteExpiredAndMissingCatalogFailClosed(t *testing.T) {
	for _, mode := range []string{"expired", "noarchive"} {
		t.Run(mode, func(t *testing.T) {
			s, a, _, campaign := seedSemesterAdapter(t)
			enableSemesterRoute(t, s, true)
			if mode == "expired" {
				q := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
				q.mu.Lock()
				q.now = campaign.Ends
				q.mu.Unlock()
			} else {
				s.initialEquipmentMu.Lock()
				s.initialEquipmentArchive = nil
				s.initialEquipmentLoadErr = errors.New("no semester resource")
				s.initialEquipmentMu.Unlock()
			}
			id, _ := s.boundGameSessionCharacterSnapshot(a)
			if s.sendCurrentSemesterCumulative(a, id) == nil {
				t.Fatal("unavailable resource/season projected")
			}
			if _, e := s.handleCurrentSemesterCumulativeRequest(a, semesterRouteBody(0)); e != nil {
				t.Fatal(e)
			}
			if monthlyMailCount(t, s, "1") != 0 {
				t.Fatal("expired/catalog absent reward")
			}
		})
	}
}

func TestSemesterRouteLegacyCommandNormalizedClass1AndOFF(t *testing.T) {
	for _, mode := range []string{"off", "noncommand", "on"} {
		t.Run(mode, func(t *testing.T) {
			s, a, c, _ := seedSemesterAdapter(t)
			enableSemesterRoute(t, s, mode != "off")
			cmd := byte(1)
			if mode == "noncommand" {
				cmd = 0
			}
			if e := s.handleGameCommand(a, cmd, 689, semesterRouteBody(0)); e != nil {
				t.Fatal(e)
			}
			if mode == "on" {
				if monthlyMailCount(t, s, "1") != 1 {
					t.Fatal("normalized legacy command not routed")
				}
				p, _ := splitGameServerUpperPacket(t, c.write.Bytes())
				if p.Header.MsgID != 689 || p.Body[0] != 1 {
					t.Fatal(p)
				}
			} else if monthlyMailCount(t, s, "1") != 0 || len(c.write.Bytes()) != 0 {
				t.Fatal("OFF/noncommand committed")
			}
		})
	}
}
