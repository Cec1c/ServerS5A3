package dnfbridge

import (
	"context"
	"crypto/sha256"
	"fmt"
	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

type presenceVersionSettings struct {
	r.SettingsRepository
	namespace *r.GuildSnapshotNamespace
}

func (s presenceVersionSettings) GuildSnapshotNamespace() *r.GuildSnapshotNamespace {
	return s.namespace
}
func (s presenceVersionSettings) LoadGuildSnapshotRevision(ctx context.Context) (string, bool, error) {
	v, found, err := s.Load(ctx, guild.RegistryScope)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(v.Values["state"]))), found, err
}

type presenceVersionReadUOW struct {
	r.GuildUnitOfWork
	r.GuildReadUnitOfWork
	namespace *r.GuildSnapshotNamespace
	counts    *guildRosterDisplayCharacters
}

func (u *presenceVersionReadUOW) WithinGuildRead(ctx context.Context, fn func(r.GuildReadView) error) error {
	return u.GuildReadUnitOfWork.WithinGuildRead(ctx, func(v r.GuildReadView) error {
		v.Settings = presenceVersionSettings{v.Settings, u.namespace}
		u.counts.base = v.Characters
		v.Characters = u.counts
		return fn(v)
	})
}

func TestGuildPresenceSharedDisplayHydratesOnceAndRejectsRemovedMember(t *testing.T) {
	s, peers, repos := guildRosterLargeFixture(t, 2)
	counts := &guildRosterDisplayCharacters{}
	repos.Guild = &presenceVersionReadUOW{repos.Guild, repos.Guild.(r.GuildReadUnitOfWork), &r.GuildSnapshotNamespace{}, counts}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	shared := &guildPresenceReadCache{}
	ctx := context.Background()
	for i, peer := range peers {
		bound, _ := s.boundGameSessionCharacterSnapshot(peer)
		g, err := s.readCurrentGuildDisplayShared(ctx, bound, false, true, shared)
		if err != nil || g == nil || len(g.Members) != 190 {
			t.Fatal(i, err)
		}
	}
	if len(counts.ids) != 2 || len(counts.ids[0]) != 190 || len(counts.ids[1]) != 1 {
		t.Fatalf("broadcast repeated member batch: %v", counts.ids)
	}
	registry, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, _, ok := registry.GuildForOwner(r.GuildOwner{CharacterID: "2", AccountID: peers[1].accountID})
	if !ok {
		t.Fatal("member fixture missing")
	}
	delete(g.Members, "2")
	if err := guild.Save(ctx, repos.Settings, registry, time.Now()); err != nil {
		t.Fatal(err)
	}
	bound, _ := s.boundGameSessionCharacterSnapshot(peers[1])
	stale, err := s.readCurrentGuildDisplayShared(ctx, bound, false, true, shared)
	if err != nil || stale != nil {
		t.Fatal("removed member received cached guild", err)
	}
}

func TestLuckyInactiveCapDoesNotWaitForAssetReservation(t *testing.T) {
	s, p, _, _, _ := levelUpNoticeFixture(t)
	s.luckyCharacterCatalog = &currentLuckyCatalog{Level1To89Only: true}
	p.luckyPreparedActive.Store(false)
	bound, _ := s.boundGameSessionCharacterSnapshot(p)
	reservation, err := s.reserveCurrentCharacterAssets(context.Background(), bound, p.accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		s.refreshCurrentLuckyCharacterAfterProgression(ctx, r.CharacterRecord{CharacterID: "77", AccountID: p.accountID, Level: 90})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("inactive lucky projection waited for occupied assets")
	}
}

func TestDisabledPacketLoggingFastPathDoesNotAllocateOrAdvanceSequence(t *testing.T) {
	s, p := &Service{}, &gameSession{}
	s.packetLogDisabled.Store(true)
	n := testing.AllocsPerRun(100, func() {
		s.logGamePacket(p, "SEND", "game-upper", nil, "msg_id", uint16(22))
		s.logGameEvent(p, "ordinary-event", "value", 1)
	})
	if n != 0 || p.packetSeq != 0 {
		t.Fatal("disabled packet logging did work", n, p.packetSeq)
	}
	if !packetEventHasDiagnostic("game-dungeon-party-peer-actor-send") || !packetEventHasDiagnostic("game-dungeon-select-blocked") || packetEventHasDiagnostic("ordinary-event") {
		t.Fatal("diagnostic routing changed")
	}
}

func BenchmarkDisabledPacketLogging(b *testing.B) {
	for _, disabled := range []bool{false, true} {
		b.Run(fmt.Sprint(disabled), func(b *testing.B) {
			s, p := &Service{}, &gameSession{}
			s.packetLogDisabled.Store(disabled)
			b.ReportAllocs()
			for b.Loop() {
				s.logGamePacket(p, "SEND", "game-upper", nil, "msg_id", uint16(22))
				s.logGameEvent(p, "ordinary-event", "value", 1)
			}
		})
	}
}

func BenchmarkUncontendedWireLock(b *testing.B) {
	s, p := &Service{}, &gameSession{}
	p.gameTransportContext()
	b.ReportAllocs()
	for b.Loop() {
		if err := s.lockGameWire(p, nil); err != nil {
			b.Fatal(err)
		}
		p.wireMu.Unlock()
	}
}
