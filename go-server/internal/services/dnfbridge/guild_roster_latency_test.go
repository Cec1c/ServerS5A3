package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"encoding/binary"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
	"longheng.io/server/internal/platform/metrics"
)

type guildRosterCountCharacter struct {
	dnfrepo.CharacterRepository
	loads atomic.Int64
}

func (r *guildRosterCountCharacter) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.loads.Add(1)
	return r.CharacterRepository.Load(ctx, id)
}

func TestGuildRosterContinuationDoesNotLoadMembers(t *testing.T) {
	for _, opcode := range []uint16{139, 141} {
		s, peers, repos, _ := guildPresenceFixture(t)
		count := &guildRosterCountCharacter{CharacterRepository: repos.Character}
		repos.Character = count
		s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
		if err := s.handleCurrentGuildMemberListForOpcode(peers[0], opcode); err != nil {
			t.Fatal(err)
		}
		// The requester's ownership check remains; the continuation has no rows.
		if got := count.loads.Load(); got != 1 {
			t.Fatalf("op%d complete character reads=%d want requester only (1)", opcode, got)
		}
	}
}

func TestGuildPresenceRepeatedRefreshHasOneQueuedEvent(t *testing.T) {
	s, peers, _, _ := guildPresenceFixture(t)
	peer := peers[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer.ctx = ctx
	if err := s.startGameSessionEvents(ctx, peer); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, peer)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	if err := s.tryPostGameSessionCharacterEvent(peer, "test-roster-gate", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "roster gate")
	for i := 0; i < 20; i++ {
		s.queueCurrentGuildPresenceRefresh(ctx, 1)
	}
	if got := peer.events.loop.QueueLengths().Medium; got != 1 {
		t.Fatalf("20 presence changes queued %d refreshes, want 1", got)
	}
}

type guildRosterSummaryCharacter struct {
	*guildRosterCountCharacter
	reads  atomic.Int64
	before func()
	fail   error
	filter func(map[string]dnfrepo.CharacterGuildSummary)
}

func (r *guildRosterSummaryCharacter) LoadCharacterGuildSummaries(ctx context.Context, ids []string) (map[string]dnfrepo.CharacterGuildSummary, error) {
	r.reads.Add(1)
	if r.before != nil {
		r.before()
	}
	if r.fail != nil {
		return nil, r.fail
	}
	rows, err := dnfrepo.LoadCharacterGuildSummaries(ctx, r.CharacterRepository, ids)
	if r.filter != nil {
		r.filter(rows)
	}
	return rows, err
}

func guildRosterLargeFixture(t *testing.T, online int) (*Service, []*gameSession, dnfrepo.Group) {
	t.Helper()
	s, _, repos, now := guildPresenceFixture(t)
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := r.Guilds[1]
	delete(r.Guilds, 2) // fixture character 3 must not belong to two guilds
	prototype := g.Members["2"]
	s.gameSessions = map[uint16]*gameSession{}
	var peers []*gameSession
	for i := 1; i <= 190; i++ {
		id, account := fmt.Sprint(i), fmt.Sprint("a", i)
		c := dnfrepo.CharacterRecord{CharacterID: id, AccountID: account, Name: fmt.Sprint("member", i), Job: "1", Level: 90, Stats: map[string]int64{"grow_type": 33}}
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		m := prototype
		m.CharacterID, m.AccountID, m.Name, m.Level, m.Job, m.GrowType = id, account, c.Name, c.Level, c.Job, "33"
		if i == 1 {
			m.Grade = dnfguild.DefaultMasterGrade
		}
		g.Members[id] = m
		if i <= online {
			p := &gameSession{conn: &bufferConn{}, connID: fmt.Sprint("roster-", i), accountID: account, selectedCharacterID: uint16(i), characterGeneration: uint64(i), townSceneReadyCharacterID: uint16(i)}
			s.gameSessions[uint16(i)] = p
			peers = append(peers, p)
		}
	}
	if err := dnfguild.Save(context.Background(), repos.Settings, r, now); err != nil {
		t.Fatal(err)
	}
	s.performance = newBridgePerformanceMetrics(metrics.New())
	return s, peers, repos
}

func TestGuildRosterBatchMatchesLegacyProjectionAndDoesNotPersist(t *testing.T) {
	for _, opcode := range []uint16{67, 139, 140, 141} {
		t.Run(fmt.Sprint(opcode), func(t *testing.T) {
			s, peers, repos := guildRosterLargeFixture(t, 1)
			ctx := context.Background()
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			c, _, _ := repos.Character.Load(ctx, "2")
			c.Name, c.Level, c.Job, c.Stats["grow_type"] = "renamed", 91, "2", 17
			if err := repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			legacy := &guildRosterCountCharacter{CharacterRepository: repos.Character}
			base := repos
			repos.Character = legacy
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			guildDispatch(t, s, peers[0], opcode, nil)
			want := append([]byte(nil), guildReply(t, peers[0], opcode)...)
			peers[0].conn.(*bufferConn).write.Reset()
			narrow := &guildRosterSummaryCharacter{guildRosterCountCharacter: &guildRosterCountCharacter{CharacterRepository: base.Character}}
			repos.Character = narrow
			guildDispatch(t, s, peers[0], opcode, nil)
			if !bytes.Equal(guildReply(t, peers[0], opcode), want) {
				t.Fatal("member/cursor packet differs from complete-load projection")
			}
			wantReads := int64(0)
			if opcode == 67 || opcode == 140 {
				wantReads = 1
			}
			if narrow.reads.Load() != wantReads || narrow.loads.Load() != 2 {
				t.Fatalf("batch=%d complete=%d", narrow.reads.Load(), narrow.loads.Load())
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			unchanged, _, _ := base.Character.Load(ctx, "2")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(c, unchanged) {
				t.Fatal("display read persisted data")
			}
		})
	}
}

func TestGuildRosterBatchFailureMissingAndWrongOwnerKeepRegistryFields(t *testing.T) {
	for _, mode := range []string{"failure", "missing", "owner", "id"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := guildPresenceFixture(t)
			guildDispatch(t, s, peers[0], 140, nil)
			want := append([]byte(nil), guildReply(t, peers[0], 140)...)
			peers[0].conn.(*bufferConn).write.Reset()
			narrow := &guildRosterSummaryCharacter{guildRosterCountCharacter: &guildRosterCountCharacter{CharacterRepository: repos.Character}}
			if mode == "failure" {
				narrow.fail = errors.New("summary unavailable")
			} else {
				narrow.filter = func(rows map[string]dnfrepo.CharacterGuildSummary) {
					row := rows["2"]
					row.Name = "must not be visible"
					switch mode {
					case "missing":
						delete(rows, "2")
						return
					case "owner":
						row.AccountID = "other"
					case "id":
						row.CharacterID = "different"
					}
					rows["2"] = row
				}
			}
			repos.Character = narrow
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			guildDispatch(t, s, peers[0], 140, nil)
			if !bytes.Equal(guildReply(t, peers[0], 140), want) || narrow.loads.Load() != 2 {
				t.Fatal("failed/mismatched batch changed membership or retried full member loads")
			}
		})
	}
}

func TestGuildPresenceRunningChangesYieldOneFollowup(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	p := peers[0]
	p.guild.memberListRequested = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	narrow := &guildRosterSummaryCharacter{guildRosterCountCharacter: &guildRosterCountCharacter{CharacterRepository: repos.Character}}
	narrow.before = func() {
		if narrow.reads.Load() == 1 {
			close(entered)
			<-release
		}
	}
	repos.Character = narrow
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	peer, _ := s.boundGameSessionCharacterSnapshot(p)
	s.requestCurrentGuildPresenceRefresh(peer, 1)
	waitTestSignal(t, entered, "summary did not start")
	for i := 0; i < 20; i++ {
		s.requestCurrentGuildPresenceRefresh(peer, 1)
	}
	var atOrdinary atomic.Int64
	if err := s.tryPostGameSessionCharacterEvent(p, "ordinary-between-refreshes", peer.character, peer.generation, func() error { atOrdinary.Store(narrow.reads.Load()); return nil }); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		if err := s.callGameSession(ctx, p, "drain", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if narrow.reads.Load() != 2 || atOrdinary.Load() != 1 {
		t.Fatalf("summary runs=%d ordinary saw=%d", narrow.reads.Load(), atOrdinary.Load())
	}
}

func TestGuildPresenceTwentyWavesFor24SessionsStayBounded(t *testing.T) {
	s, peers, _ := guildRosterLargeFixture(t, 24)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	for _, p := range peers {
		p.ctx = ctx
		if err := s.startGameSessionEvents(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stopTestGameSessionEvents(t, s, p) })
		entered := make(chan struct{})
		if err := s.tryPostGameSessionCharacterEvent(p, "gate", p.selectedCharacterID, p.characterGeneration, func() error { close(entered); <-release; return nil }); err != nil {
			t.Fatal(err)
		}
		waitTestSignal(t, entered, "24-session gate")
	}
	for i := 0; i < 20; i++ {
		s.queueCurrentGuildPresenceRefresh(ctx, 1)
	}
	for _, p := range peers {
		if got := p.events.loop.QueueLengths().Medium; got != 1 {
			t.Fatalf("session %s queue=%d", p.connID, got)
		}
	}
	if got := s.performance.guildRoster[guildRefreshCoalesced].Load(); got != 24*19 {
		t.Fatalf("coalesced=%d", got)
	}
	once.Do(func() { close(release) })
	for _, p := range peers {
		if err := s.callGameSession(ctx, p, "drain", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.performance.guildRoster[guildRefreshExecuted].Load(); got != 24 {
		t.Fatalf("executed=%d", got)
	}
}

func TestGuildPresenceStaleDiscardCannotClearNewToken(t *testing.T) {
	s, peers, _, _ := guildPresenceFixture(t)
	p := peers[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	if err := s.tryPostGameSessionCharacterEvent(p, "gate", p.selectedCharacterID, p.characterGeneration, func() error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "stale token gate")
	old, _ := s.boundGameSessionCharacterSnapshot(p)
	s.requestCurrentGuildPresenceRefresh(old, 1)
	p.guild.mu.Lock()
	oldToken := p.guild.presenceRefresh
	p.guild.mu.Unlock()
	s.mu.Lock()
	advanceGameSessionCharacterGeneration(p)
	s.mu.Unlock()
	next, _ := s.boundGameSessionCharacterSnapshot(p)
	s.requestCurrentGuildPresenceRefresh(next, 1)
	p.guild.mu.Lock()
	newToken := p.guild.presenceRefresh
	p.guild.mu.Unlock()
	s.discardCurrentGuildPresenceRefresh(oldToken)
	p.guild.mu.Lock()
	got := p.guild.presenceRefresh
	p.guild.mu.Unlock()
	if got != newToken || newToken == oldToken {
		t.Fatal("old callback removed new pending state")
	}
	once.Do(func() { close(release) })
	if err := s.callGameSession(ctx, p, "drain", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	p.guild.mu.Lock()
	got = p.guild.presenceRefresh
	p.guild.mu.Unlock()
	if got != nil {
		t.Fatal("executed token remains pending")
	}
}

func TestGuildPresenceCancelledContextReleasesQueuedToken(t *testing.T) {
	s, peers, _, _ := guildPresenceFixture(t)
	p := peers[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	if err := s.tryPostGameSessionCharacterEvent(p, "gate", p.selectedCharacterID, p.characterGeneration, func() error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "cancel gate")
	peer, _ := s.boundGameSessionCharacterSnapshot(p)
	s.requestCurrentGuildPresenceRefresh(peer, 1)
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.guild.mu.Lock()
		empty := p.guild.presenceRefresh == nil
		p.guild.mu.Unlock()
		if empty {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancelled context retained refresh skipped before processor")
}

func TestGuildPresenceFullQueueRetriesBoundedlyAndRecovers(t *testing.T) {
	s, peers, _, _ := guildPresenceFixture(t)
	p := peers[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	if err := s.tryPostGameSessionCharacterEvent(p, "gate", p.selectedCharacterID, p.characterGeneration, func() error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "full queue gate")
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := s.tryPostGameSessionCharacterEvent(p, "filler", p.selectedCharacterID, p.characterGeneration, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	peer, _ := s.boundGameSessionCharacterSnapshot(p)
	for i := 0; i < 20; i++ {
		s.requestCurrentGuildPresenceRefresh(peer, 1)
	}
	p.guild.mu.Lock()
	token := p.guild.presenceRefresh
	p.guild.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		p.guild.mu.Lock()
		exhausted := p.guild.presenceRefresh == token && token.retries == 3 && token.timer == nil && !token.queued
		p.guild.mu.Unlock()
		if exhausted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue retries not bounded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := p.events.loop.MetricsSnapshot().Dropped[eventloop.PriorityMedium]; got != 4 {
		t.Fatalf("full queue attempts=%d want initial plus 3 retries", got)
	}
	once.Do(func() { close(release) })
	if err := s.callGameSession(ctx, p, "drain", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	s.requestCurrentGuildPresenceRefresh(peer, 1)
	if err := s.callGameSession(ctx, p, "drain recovery", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	p.guild.mu.Lock()
	remaining := p.guild.presenceRefresh
	p.guild.mu.Unlock()
	if remaining != nil || !p.events.active.Load() {
		t.Fatal("exhausted dirty refresh did not recover on next observation")
	}
}

func TestGuildPresenceExitClearsQueuedRefreshAndStopsFurtherFrames(t *testing.T) {
	s, peers, _, _ := guildPresenceFixture(t)
	p := peers[0]
	p.guild.memberListRequested = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	if err := s.tryPostGameSessionCharacterEvent(p, "gate", p.selectedCharacterID, p.characterGeneration, func() error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "exit gate")
	peer, _ := s.boundGameSessionCharacterSnapshot(p)
	s.requestCurrentGuildPresenceRefresh(peer, 1)
	if err := s.handleCurrentChannelExit(p, []byte{0}, "guild-test"); err != nil {
		t.Fatal(err)
	}
	written := p.conn.(*bufferConn).write.Len()
	s.requestCurrentGuildPresenceRefresh(peer, 1)
	once.Do(func() { close(release) })
	if err := s.callGameSession(ctx, p, "exit drain", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := p.conn.(*bufferConn).write.Len(); got != written {
		t.Fatalf("after EXIT bytes %d -> %d", written, got)
	}
	p.guild.mu.Lock()
	remaining, closed := p.guild.presenceRefresh, p.guild.presenceRefreshClosed
	p.guild.mu.Unlock()
	if remaining != nil || !closed {
		t.Fatal("EXIT did not retire optional refresh")
	}
}

func TestGuildRosterRefreshBurstKeepsActualDeathAndExperienceOnce(t *testing.T) {
	s, p, repos, packets := prepareDungeonDeathBurstTest(t, true, 5)
	c, found, err := repos.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatal("fixture character", err)
	}
	c.Name = "战斗成员"
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	p.accountID = c.AccountID
	p.characterGeneration = 1
	s.gameSessions = map[uint16]*gameSession{99: p}
	r := dnfguild.NewRegistry()
	g, err := r.Create(c, "战斗回归", s.gameplayNow())
	if err != nil {
		t.Fatal(err)
	}
	if err := dnfguild.Save(context.Background(), repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	p.guild.memberListRequested = true
	s.performance = newBridgePerformanceMetrics(metrics.New())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	if err := s.tryPostGameSessionCharacterEvent(p, "gate", 99, 1, func() error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "combat gate")
	peer, _ := s.boundGameSessionCharacterSnapshot(p)
	for i := 0; i < 20; i++ {
		s.requestCurrentGuildPresenceRefresh(peer, g.ID)
	}
	result := make(chan error, 1)
	if err := s.tryPostGameSessionCharacterEvent(p, "combat-death", 99, 1, func() error {
		err := s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false)
		result <- err
		return err
	}); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("coalesced refresh held actual death")
	}
	if err := s.callGameSession(ctx, p, "drain", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	deaths := map[uint32]int{}
	for _, packet := range splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes()) {
		if packet.Header.MsgID == 38 {
			deaths[binary.LittleEndian.Uint32(packet.Body)]++
		}
	}
	if len(deaths) != 3 {
		t.Fatalf("death frames=%v", deaths)
	}
	for _, n := range deaths {
		if n != 1 {
			t.Fatal("duplicate death frame")
		}
	}
	after, _, err := repos.Character.Load(ctx, "99")
	if err != nil {
		t.Fatal(err)
	}
	if after.Stats[currentHonorExpertProgressExperienceStatKey] != 30 {
		t.Fatalf("accepted deaths experience=%d, want 30", after.Stats[currentHonorExpertProgressExperienceStatKey])
	}
	if err := s.callGameSession(ctx, p, "replay", func() error { return s.handleCurrentDungeonDeathBurstPackets(p, packets[:3], false) }); err != nil {
		t.Fatal(err)
	}
	replayed, _, _ := repos.Character.Load(ctx, "99")
	if !reflect.DeepEqual(after, replayed) {
		t.Fatal("death replay granted twice")
	}
	if got := s.performance.series[perfMonsterEnqueueToDeath].count.Load(); got < 3 {
		t.Fatalf("actual enqueue-to-death samples=%d", got)
	}
}
