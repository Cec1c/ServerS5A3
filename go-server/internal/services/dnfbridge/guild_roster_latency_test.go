package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
	"longheng.io/server/internal/platform/metrics"
)

type guildRosterCountCharacter struct {
	dnfrepo.CharacterRepository
	loads      atomic.Int64
	identities atomic.Int64
}

func (r *guildRosterCountCharacter) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	r.loads.Add(1)
	return r.CharacterRepository.Load(ctx, id)
}

func (r *guildRosterCountCharacter) LoadCharacterIdentity(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, error) {
	r.identities.Add(1)
	return dnfrepo.LoadCharacterIdentity(ctx, r.CharacterRepository, id)
}

func TestGuildRosterSnapshotReadUsesRequestedBatch(t *testing.T) {
	for _, tc := range []struct {
		opcode uint16
		full   bool
	}{
		{uint16(dnfenum.CmdPacketGuildMemerList), true},
		{uint16(dnfenum.CmdPacketGuildMemerListNext), false},
		{uint16(dnfenum.CmdPacketGuildAllMemberList), true},
		{uint16(dnfenum.CmdPacketGuildAllMemberListNext), false},
	} {
		t.Run(fmt.Sprint(tc.opcode), func(t *testing.T) {
			s, peers, repos := guildRosterLargeFixture(t, 1)
			characters := &guildRosterDisplayCharacters{}
			u := &guildRosterReadUOW{
				GuildUnitOfWork:     repos.Guild,
				GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork),
				characters:          characters,
			}
			count := &guildRosterCountCharacter{CharacterRepository: repos.Character}
			repos.Guild, repos.Character = u, count
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			peers[0].conn.(*bufferConn).write.Reset()
			guildDispatch(t, s, peers[0], tc.opcode, nil)
			body := guildReply(t, peers[0], tc.opcode)
			if len(body) < 2 || body[0] != 1 {
				t.Fatalf("op%d failed: %x", tc.opcode, body)
			}
			if u.reads != 1 || u.writerCalls != 0 || characters.queries != 1 || len(characters.ids) != 1 {
				t.Fatalf("op%d reads=%d writer=%d queries=%d batches=%d", tc.opcode, u.reads, u.writerCalls, characters.queries, len(characters.ids))
			}
			got := characters.ids[0]
			if tc.full {
				if len(got) != 190 {
					t.Fatalf("op%d full snapshot IDs=%d want 190", tc.opcode, len(got))
				}
				seen := make(map[string]bool, len(got))
				for _, id := range got {
					if seen[id] {
						t.Fatalf("op%d duplicate snapshot ID %q", tc.opcode, id)
					}
					seen[id] = true
				}
				for i := 1; i <= 190; i++ {
					if !seen[fmt.Sprint(i)] {
						t.Fatalf("op%d snapshot missing member %d", tc.opcode, i)
					}
				}
				_, at, ok := readCurrentRawDstr(body, 5)
				if !ok || len(body) < at+2 || binary.LittleEndian.Uint16(body[at:at+2]) != 190 {
					t.Fatalf("op%d full snapshot emitted %x", tc.opcode, body)
				}
			} else {
				if !reflect.DeepEqual(got, []string{"1"}) {
					t.Fatalf("op%d continuation IDs=%v want requester only", tc.opcode, got)
				}
				if len(body) < 7 || binary.LittleEndian.Uint16(body[5:7]) != 0 {
					t.Fatalf("op%d continuation emitted member rows: %x", tc.opcode, body)
				}
			}
			if count.loads.Load() != 0 || count.identities.Load() != 0 {
				t.Fatalf("op%d complete=%d identity=%d preloaded outside snapshot", tc.opcode, count.loads.Load(), count.identities.Load())
			}
		})
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

type guildRosterIdentityCharacter struct {
	*guildRosterCountCharacter
	identities     atomic.Int64
	identityBefore func()
	identityFilter func(*dnfrepo.CharacterIdentity)
	identityErr    error
}

func (r *guildRosterIdentityCharacter) LoadCharacterIdentity(ctx context.Context, id string) (dnfrepo.CharacterIdentity, bool, error) {
	r.identities.Add(1)
	if r.identityBefore != nil {
		r.identityBefore()
	}
	if r.identityErr != nil {
		return dnfrepo.CharacterIdentity{}, false, r.identityErr
	}
	value, found, err := dnfrepo.LoadCharacterIdentity(ctx, r.CharacterRepository, id)
	if r.identityFilter != nil {
		r.identityFilter(&value)
	}
	return value, found, err
}

func TestGuildRosterIdentityReadKeepsOwnershipAndGenerationFence(t *testing.T) {
	for _, mode := range []string{"valid", "account", "character", "generation", "query"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := guildPresenceFixture(t)
			p := peers[0]
			r := &guildRosterIdentityCharacter{guildRosterCountCharacter: &guildRosterCountCharacter{CharacterRepository: repos.Character}}
			switch mode {
			case "account":
				r.identityFilter = func(v *dnfrepo.CharacterIdentity) { v.AccountID = "different" }
			case "character":
				r.identityFilter = func(v *dnfrepo.CharacterIdentity) { v.CharacterID = "different" }
			case "generation":
				r.identityBefore = func() { s.mu.Lock(); advanceGameSessionCharacterGeneration(p); s.mu.Unlock() }
			case "query":
				r.identityErr = errors.New("identity query failed")
			}
			repos.Character = r
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			_, err := s.currentGuildOwnedSelectedIdentity(p)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if r.loads.Load() != 0 || r.identities.Load() != 1 {
				t.Fatalf("full=%d identity=%d", r.loads.Load(), r.identities.Load())
			}
		})
	}
}

type guildRosterDisplayCharacters struct {
	base    dnfrepo.GuildCharacterReader
	ids     [][]string
	queries int
	before  func()
	fail    error
	filter  func(map[string]dnfrepo.GuildCharacter)
}

func (r *guildRosterDisplayCharacters) LoadGuildCharacters(ctx context.Context, ids []string) (map[string]dnfrepo.GuildCharacter, error) {
	r.queries++
	r.ids = append(r.ids, append([]string(nil), ids...))
	if r.before != nil {
		r.before()
	}
	if r.fail != nil {
		return nil, r.fail
	}
	if r.base == nil {
		return nil, errors.New("guild display snapshot has no character reader")
	}
	rows, err := r.base.LoadGuildCharacters(ctx, ids)
	if err != nil {
		return nil, err
	}
	if r.filter != nil {
		r.filter(rows)
	}
	return rows, nil
}

type guildRosterReadUOW struct {
	dnfrepo.GuildUnitOfWork
	dnfrepo.GuildReadUnitOfWork
	characters  *guildRosterDisplayCharacters
	reads       int
	writerCalls int
	afterRead   func()
}

func (u *guildRosterReadUOW) WithinGuild(context.Context, []dnfrepo.GuildOwner, func(dnfrepo.Group) error) error {
	u.writerCalls++
	return errors.New("guild display attempted writer lock")
}

func (u *guildRosterReadUOW) WithinGuildRead(ctx context.Context, fn func(dnfrepo.GuildReadView) error) error {
	u.reads++
	if u.characters == nil {
		return errors.New("guild display spy has no character reader")
	}
	err := u.GuildReadUnitOfWork.WithinGuildRead(ctx, func(view dnfrepo.GuildReadView) error {
		u.characters.base = view.Characters
		view.Characters = u.characters
		return fn(view)
	})
	if u.afterRead != nil {
		u.afterRead()
	}
	return err
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

func TestGuildRosterSnapshotFiltersNonActorMembersAndSkipsWrites(t *testing.T) {
	for _, mode := range []string{"missing", "account", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := guildPresenceFixture(t)
			ctx := context.Background()
			baseCharacters := repos.Character
			beforeRegistry, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			beforeMember, _, _ := baseCharacters.Load(ctx, "2")
			characters := &guildRosterDisplayCharacters{
				filter: func(rows map[string]dnfrepo.GuildCharacter) {
					row := rows["2"]
					switch mode {
					case "missing":
						delete(rows, "2")
						return
					case "account":
						row.AccountID = "different"
					case "deleted":
						row.DeleteFlag = 1
					}
					rows["2"] = row
				},
			}
			u := &guildRosterReadUOW{
				GuildUnitOfWork:     repos.Guild,
				GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork),
				characters:          characters,
			}
			count := &guildRosterCountCharacter{CharacterRepository: baseCharacters}
			repos.Guild, repos.Character = u, count
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			source, ok := s.boundGameSessionCharacterSnapshot(peers[0])
			if !ok {
				t.Fatal("source character missing")
			}
			got, err := s.readCurrentGuildDisplay(ctx, source, false, true)
			if err != nil || got == nil {
				t.Fatalf("snapshot err=%v guild=%v", err, got)
			}
			if _, found := got.Members["1"]; !found {
				t.Fatal("valid requester filtered from snapshot")
			}
			if _, found := got.Members["2"]; found {
				t.Fatalf("invalid %s member survived snapshot", mode)
			}
			if u.reads != 1 || u.writerCalls != 0 || characters.queries != 1 || len(characters.ids) != 1 || len(characters.ids[0]) != 2 {
				t.Fatalf("reads=%d writer=%d queries=%d batches=%v", u.reads, u.writerCalls, characters.queries, characters.ids)
			}
			if count.loads.Load() != 0 || count.identities.Load() != 0 {
				t.Fatalf("complete=%d identity=%d outside snapshot", count.loads.Load(), count.identities.Load())
			}
			afterRegistry, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			afterMember, _, _ := baseCharacters.Load(ctx, "2")
			if !reflect.DeepEqual(beforeRegistry, afterRegistry) || !reflect.DeepEqual(beforeMember, afterMember) {
				t.Fatal("filtered display snapshot persisted data")
			}
		})
	}
}

func TestGuildRosterSnapshotRejectsRequesterAndQueryFailures(t *testing.T) {
	for _, mode := range []string{"missing", "account", "character", "deleted", "query"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := guildPresenceFixture(t)
			ctx := context.Background()
			baseCharacters := repos.Character
			beforeRegistry, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			beforeActor, _, _ := baseCharacters.Load(ctx, "1")
			characters := &guildRosterDisplayCharacters{}
			if mode == "query" {
				characters.fail = errors.New("guild character query failed")
			} else {
				characters.filter = func(rows map[string]dnfrepo.GuildCharacter) {
					actor := rows["1"]
					switch mode {
					case "missing":
						delete(rows, "1")
						return
					case "account":
						actor.AccountID = "different"
					case "character":
						actor.CharacterID = "different"
					case "deleted":
						actor.DeleteFlag = 1
					}
					rows["1"] = actor
				}
			}
			u := &guildRosterReadUOW{
				GuildUnitOfWork:     repos.Guild,
				GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork),
				characters:          characters,
			}
			count := &guildRosterCountCharacter{CharacterRepository: baseCharacters}
			repos.Guild, repos.Character = u, count
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			peers[0].conn.(*bufferConn).write.Reset()
			guildDispatch(t, s, peers[0], uint16(dnfenum.CmdPacketGuildAllMemberList), nil)
			body := guildReply(t, peers[0], uint16(dnfenum.CmdPacketGuildAllMemberList))
			if !bytes.Equal(body, []byte{0, currentGuildGenericError}) {
				t.Fatalf("mode=%s failure ACK=%x", mode, body)
			}
			if u.reads != 1 || u.writerCalls != 0 || characters.queries != 1 || len(characters.ids) != 1 || len(characters.ids[0]) != 2 {
				t.Fatalf("mode=%s reads=%d writer=%d queries=%d batches=%v", mode, u.reads, u.writerCalls, characters.queries, characters.ids)
			}
			if count.loads.Load() != 0 || count.identities.Load() != 0 {
				t.Fatalf("mode=%s complete=%d identity=%d outside snapshot", mode, count.loads.Load(), count.identities.Load())
			}
			afterRegistry, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			afterActor, _, _ := baseCharacters.Load(ctx, "1")
			if !reflect.DeepEqual(beforeRegistry, afterRegistry) || !reflect.DeepEqual(beforeActor, afterActor) {
				t.Fatalf("mode=%s failure persisted data", mode)
			}
		})
	}
}

func TestGuildRosterSnapshotDoesNotPersistCharacterProjection(t *testing.T) {
	s, peers, repos := guildRosterLargeFixture(t, 1)
	ctx := context.Background()
	baseCharacters := repos.Character
	beforeRegistry, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
	changed, found, err := baseCharacters.Load(ctx, "2")
	if err != nil || !found {
		t.Fatalf("load fixture character: found=%v err=%v", found, err)
	}
	changed.Name, changed.Level, changed.Job, changed.Stats["grow_type"] = "renamed", 91, "2", 17
	if err := baseCharacters.Save(ctx, changed); err != nil {
		t.Fatal(err)
	}
	persisted, found, err := baseCharacters.Load(ctx, "2")
	if err != nil || !found {
		t.Fatalf("reload fixture character: found=%v err=%v", found, err)
	}
	characters := &guildRosterDisplayCharacters{}
	u := &guildRosterReadUOW{
		GuildUnitOfWork:     repos.Guild,
		GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork),
		characters:          characters,
	}
	count := &guildRosterCountCharacter{CharacterRepository: baseCharacters}
	repos.Guild, repos.Character = u, count
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	source, ok := s.boundGameSessionCharacterSnapshot(peers[0])
	if !ok {
		t.Fatal("source character missing")
	}
	display, err := s.readCurrentGuildDisplay(ctx, source, false, true)
	if err != nil || display == nil {
		t.Fatalf("snapshot err=%v guild=%v", err, display)
	}
	member, found := display.Members["2"]
	if !found || member.Name != "renamed" || member.Level != 91 || member.Job != "2" || member.GrowType != "17" {
		t.Fatalf("displayed member=%+v found=%v", member, found)
	}
	if u.reads != 1 || u.writerCalls != 0 || characters.queries != 1 || len(characters.ids) != 1 || len(characters.ids[0]) != 190 {
		t.Fatalf("reads=%d writer=%d queries=%d batches=%v", u.reads, u.writerCalls, characters.queries, characters.ids)
	}
	if count.loads.Load() != 0 || count.identities.Load() != 0 {
		t.Fatalf("complete=%d identity=%d outside snapshot", count.loads.Load(), count.identities.Load())
	}
	afterRegistry, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
	afterCharacter, _, _ := baseCharacters.Load(ctx, "2")
	if !reflect.DeepEqual(beforeRegistry, afterRegistry) || !reflect.DeepEqual(persisted, afterCharacter) {
		t.Fatal("display projection was written back to persistent records")
	}
}

func TestGuildRosterSnapshotDropsRetiredGeneration(t *testing.T) {
	s, peers, repos, _ := guildPresenceFixture(t)
	characters := &guildRosterDisplayCharacters{}
	u := &guildRosterReadUOW{
		GuildUnitOfWork:     repos.Guild,
		GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork),
		characters:          characters,
		afterRead: func() {
			s.mu.Lock()
			advanceGameSessionCharacterGeneration(peers[0])
			s.mu.Unlock()
		},
	}
	count := &guildRosterCountCharacter{CharacterRepository: repos.Character}
	repos.Guild, repos.Character = u, count
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	peers[0].conn.(*bufferConn).write.Reset()
	guildDispatch(t, s, peers[0], uint16(dnfenum.CmdPacketGuildAllMemberList), nil)
	if peers[0].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("reply sent after guild roster generation advanced")
	}
	if u.reads != 1 || u.writerCalls != 0 || characters.queries != 1 || count.loads.Load() != 0 || count.identities.Load() != 0 {
		t.Fatalf("reads=%d writer=%d queries=%d complete=%d identity=%d", u.reads, u.writerCalls, characters.queries, count.loads.Load(), count.identities.Load())
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
	narrow := &guildRosterDisplayCharacters{}
	var reads atomic.Int64
	narrow.before = func() {
		if reads.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	repos.Guild = &guildRosterReadUOW{GuildUnitOfWork: repos.Guild, GuildReadUnitOfWork: repos.Guild.(dnfrepo.GuildReadUnitOfWork), characters: narrow}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	peer, _ := s.boundGameSessionCharacterSnapshot(p)
	s.requestCurrentGuildPresenceRefresh(peer, 1)
	waitTestSignal(t, entered, "summary did not start")
	for i := 0; i < 20; i++ {
		s.requestCurrentGuildPresenceRefresh(peer, 1)
	}
	var atOrdinary atomic.Int64
	if err := s.tryPostGameSessionCharacterEvent(p, "ordinary-between-refreshes", peer.character, peer.generation, func() error { atOrdinary.Store(reads.Load()); return nil }); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		if err := s.callGameSession(ctx, p, "drain", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 2 || atOrdinary.Load() != 1 {
		t.Fatalf("summary runs=%d ordinary saw=%d", reads.Load(), atOrdinary.Load())
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
