package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func p1GuildRoster(members int) *dnfguild.Guild {
	g := &dnfguild.Guild{
		ID:          42,
		Name:        "并发公会",
		Level:       10,
		MemberLimit: 5000,
		Members:     make(map[string]dnfguild.Member, members),
	}
	for i := 1; i <= members; i++ {
		id := strconv.Itoa(i)
		g.Members[id] = dnfguild.Member{
			CharacterID: id,
			AccountID:   "acc:" + id,
			Name:        fmt.Sprintf("成员%d", i),
			Memo:        "备注",
			Level:       90,
			Job:         "1",
			GrowType:    "0",
			Grade:       4,
		}
	}
	return g
}

func TestGuildPresenceSharedRosterChunksBuildsOnceAcrossConcurrentViewers(t *testing.T) {
	const viewers = 16
	g := p1GuildRoster(939)
	now := time.Unix(1_700_000_000, 0).UTC()
	online := map[string]byte{"1": 11}
	cache := &guildPresenceReadCache{g: g}

	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	var builds atomic.Int32
	cache.rosterChunksBuilder = func(guild *dnfguild.Guild, opcode uint16, full, cursor bool, online map[string]byte, sampledAt time.Time, region byte) []currentGuildPacketChunk {
		builds.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate
		return buildCurrentGuildMemberListChunks(guild, opcode, full, cursor, online, sampledAt, region)
	}

	results := make([][]currentGuildPacketChunk, viewers)
	errs := make(chan error, viewers)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			chunks := cache.sharedRosterChunks(nil, g, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, online, now, 1)
			if len(chunks) != 4 {
				errs <- fmt.Errorf("viewer %d got %d chunks, want 4", i, len(chunks))
				return
			}
			results[i] = chunks
		}(i)
	}
	close(begin)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("no roster build started")
	}
	// The first builder is blocked. Every other viewer must join that build
	// instead of starting another projection for the same snapshot.
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) && builds.Load() == 1 {
		time.Sleep(2 * time.Millisecond)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("concurrent viewers started %d roster builds, want 1", got)
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("roster builds after completion=%d, want 1", got)
	}
	for i := 1; i < viewers; i++ {
		if len(results[i]) != len(results[0]) || &results[i][0] != &results[0][0] {
			t.Fatalf("viewer %d received a different projection allocation", i)
		}
	}
	reference := buildCurrentGuildMemberListChunks(g, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, online, now, 1)
	for i := range results {
		if len(results[i]) != len(reference) {
			t.Fatalf("viewer %d chunk count=%d want %d", i, len(results[i]), len(reference))
		}
		for chunk := range reference {
			if results[i][chunk].opcode != reference[chunk].opcode || !bytes.Equal(results[i][chunk].body, reference[chunk].body) {
				t.Fatalf("viewer %d chunk %d diverged from the direct projection", i, chunk)
			}
		}
	}
	hit := cache.sharedRosterChunks(nil, g, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, online, now, 1)
	if &hit[0] != &results[0][0] {
		t.Fatal("cache hit rebuilt an unchanged roster")
	}
}

func TestGuildPresenceSharedRosterChunksDoesNotCacheAcrossSnapshotChange(t *testing.T) {
	g1 := p1GuildRoster(2)
	g2 := p1GuildRoster(3)
	now := time.Unix(1_700_000_100, 0).UTC()
	online := map[string]byte{"1": 11}
	cache := &guildPresenceReadCache{g: g1}

	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	cache.rosterChunksBuilder = func(guild *dnfguild.Guild, opcode uint16, full, cursor bool, online map[string]byte, sampledAt time.Time, region byte) []currentGuildPacketChunk {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
		return buildCurrentGuildMemberListChunks(guild, opcode, full, cursor, online, sampledAt, region)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		cache.sharedRosterChunks(nil, g1, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, online, now, 1)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("roster build did not start")
	}
	cache.mu.Lock()
	cache.g, cache.rosterChunks = g2, nil
	cache.mu.Unlock()
	close(gate)
	<-done

	cache.mu.Lock()
	stale := cache.rosterChunks
	cache.mu.Unlock()
	if stale != nil {
		t.Fatal("roster body for the replaced guild snapshot was cached")
	}
	out := cache.sharedRosterChunks(nil, g2, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, online, now, 1)
	if len(out) != 1 {
		t.Fatalf("replacement snapshot chunks=%d want 1", len(out))
	}
	cache.mu.Lock()
	cached := cache.rosterChunks
	cache.mu.Unlock()
	if cached == nil || &cached[0] != &out[0] {
		t.Fatal("replacement snapshot was not cached")
	}
}

func TestCurrentGuildNameEncoderMatchesFreshEncoders(t *testing.T) {
	names := []string{
		"第一",
		"",
		"第三",
		"ÿ坏名字",
		"长名字长名字长名字长名字",
		"备注",
	}
	shared := newCurrentGuildNameEncoder()
	for _, name := range names {
		var w packetWriter
		shared.writeDstr(&w, name)
		raw, _, ok := readCurrentRawDstr(w.bytes(), 0)
		if !ok {
			t.Fatalf("encoded %q is not a valid DSTR", name)
		}
		want, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(name))
		if err != nil {
			want = []byte(name)
		}
		if !bytes.Equal(raw, want) {
			t.Fatalf("shared encoder for %q = %x, want %x", name, raw, want)
		}
	}
}

func TestGuildMemberListChunkRowsMatchIndependentMemberProjections(t *testing.T) {
	members := []dnfguild.Member{
		{CharacterID: "1", AccountID: "a1", Name: "第一", Memo: "备注一", Level: 90, Job: "1", Grade: 1},
		{CharacterID: "2", AccountID: "a2", Name: "ÿ坏名字", Memo: "", Level: 90, Job: "1", Grade: 2},
		{CharacterID: "3", AccountID: "a3", Name: "第三", Memo: "备注三", Level: 90, Job: "1", Grade: 3},
	}
	g := &dnfguild.Guild{ID: 9, Name: "行内容公会", Level: 3, MemberLimit: 100, Members: map[string]dnfguild.Member{}}
	for _, member := range members {
		g.Members[member.CharacterID] = member
	}
	now := time.Unix(1_700_000_200, 0).UTC()
	ordered := dnfguild.SortedMembers(g)
	body := buildCurrentGuildMemberListChunkBody(g, true, true, uint16(len(ordered)), ordered, map[string]byte{}, now, 1)
	offset := 4
	if _, next, ok := readCurrentRawDstr(body, offset); !ok {
		t.Fatal("guild name DSTR is malformed")
	} else {
		offset = next + 4 // total and row count
	}
	if offset > len(body)-4 {
		t.Fatal("member rows are missing")
	}
	for _, member := range ordered {
		single := &dnfguild.Guild{ID: g.ID, Name: g.Name, Level: g.Level, MemberLimit: g.MemberLimit, Members: map[string]dnfguild.Member{member.CharacterID: member}}
		singleBody := buildCurrentGuildMemberListChunkBody(single, true, true, 1, []dnfguild.Member{member}, map[string]byte{}, now, 1)
		start := 4
		if _, next, ok := readCurrentRawDstr(singleBody, start); !ok {
			t.Fatalf("member %s guild name DSTR is malformed", member.CharacterID)
		} else {
			start = next + 4
		}
		row := singleBody[start : len(singleBody)-4]
		end := offset + len(row)
		if end > len(body)-4 || !bytes.Equal(body[offset:end], row) {
			t.Fatalf("member %s row changed when sharing one encoder", member.CharacterID)
		}
		offset = end
	}
	if offset != len(body)-4 {
		t.Fatalf("member rows consumed %d of %d bytes", offset, len(body)-4)
	}
}

type p1CountingWeddingStore struct {
	dnfrepo.CharacterRepository
	loads atomic.Int32
}

func (s *p1CountingWeddingStore) LoadCharacterWedding(ctx context.Context, id string) (dnfrepo.CharacterWeddingSnapshot, bool, error) {
	s.loads.Add(1)
	return dnfrepo.LoadCharacterWedding(ctx, s.CharacterRepository, id)
}

func TestGuildRosterWeddingPhaseStaysAuthoritativeAcrossRequests(t *testing.T) {
	f, repos, viewer := setupP1WeddingGuildRosterFixture(t)
	s := f.service
	counter := &p1CountingWeddingStore{CharacterRepository: repos.Character}
	repos.Character = counter
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }

	request := func() (frames int, loads int32) {
		before := counter.loads.Load()
		f.targetConn.write.Reset()
		if err := s.handleCurrentGuildMemberListForOpcode(f.target, uint16(dnfenum.CmdPacketGuildAllMemberList)); err != nil {
			t.Fatalf("roster request failed: %v", err)
		}
		return countSilentMode8Frames(t, f.targetConn.write.Bytes()), counter.loads.Load() - before
	}

	frames, loads := request()
	if frames != 1 || loads < 1 {
		t.Fatalf("reserved viewer: prime frames=%d wedding loads=%d, want frame plus an authoritative read", frames, loads)
	}

	// The viewer phase is authoritative per roster request. A cached phase with
	// no durable invalidation would miss this Single transition and prime peers
	// for a relationship that no longer exists.
	resetCurrentGuildPresenceRefresh(f.target, false)
	viewer.Stats["wedding_v1_phase"] = int64(wedding.Single)
	delete(viewer.Stats, "wedding_v1_partner")
	delete(viewer.Stats, "wedding_v1_ceremony_at")
	delete(viewer.Stats, "wedding_v1_ceremony_channel")
	delete(viewer.Stats, "wedding_v1_ceremony_area")
	if err := dnfrepo.SaveCharacterFields(context.Background(), repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	frames, loads = request()
	if frames != 0 || loads < 1 {
		t.Fatalf("single viewer: prime frames=%d wedding loads=%d, want no frame plus an authoritative read", frames, loads)
	}

	resetCurrentGuildPresenceRefresh(f.target, false)
	viewer.Stats["wedding_v1_phase"] = int64(wedding.Reserved)
	viewer.Stats["wedding_v1_partner"] = 30
	viewer.Stats["wedding_v1_ceremony_at"] = s.gameplayNow().Unix() + 300
	viewer.Stats["wedding_v1_ceremony_channel"] = int64(currentTownPresenceChannelID(f.target))
	viewer.Stats["wedding_v1_ceremony_area"] = 8
	if err := dnfrepo.SaveCharacterFields(context.Background(), repos.Character, viewer, dnfrepo.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	frames, loads = request()
	if frames != 1 || loads < 1 {
		t.Fatalf("re-reserved viewer: prime frames=%d wedding loads=%d, want frame plus an authoritative read", frames, loads)
	}
}
