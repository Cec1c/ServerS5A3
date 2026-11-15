package dnfbridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type guildPresenceConnectionWaitUOW struct {
	r.GuildUnitOfWork
	r.GuildReadUnitOfWork
	entered chan struct{}
	release <-chan struct{}
	reads   atomic.Int32
}

func (u *guildPresenceConnectionWaitUOW) WithinGuildRead(ctx context.Context, apply func(r.GuildReadView) error) error {
	u.reads.Add(1)
	u.entered <- struct{}{}
	select {
	case <-u.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return u.GuildReadUnitOfWork.WithinGuildRead(ctx, apply)
}

func guildPresenceConnectionWaitFixture(t *testing.T) (*Service, []boundGameSessionCharacter, chan struct{}, *guildPresenceConnectionWaitUOW) {
	t.Helper()
	s, peers, repos := guildRosterLargeFixture(t, 2)
	release := make(chan struct{})
	u := &guildPresenceConnectionWaitUOW{
		GuildUnitOfWork:     repos.Guild,
		GuildReadUnitOfWork: repos.Guild.(r.GuildReadUnitOfWork),
		entered:             make(chan struct{}, 2),
		release:             release,
	}
	repos.Guild = u
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	sources := make([]boundGameSessionCharacter, 0, len(peers))
	for _, peer := range peers {
		source, ok := s.boundGameSessionCharacterSnapshot(peer)
		if !ok {
			t.Fatal("source character missing")
		}
		sources = append(sources, source)
	}
	return s, sources, release, u
}

func startGuildPresenceConnectionWaitRead(s *Service, source boundGameSessionCharacter, contribution, hydrateMembers bool, shared *guildPresenceReadCache, ctx context.Context, started chan<- struct{}) <-chan error {
	done := make(chan error, 1)
	go func() {
		if started != nil {
			close(started)
		}
		_, err := s.readCurrentGuildDisplayShared(ctx, source, contribution, hydrateMembers, shared)
		done <- err
	}()
	return done
}

func waitGuildPresenceConnectionWaitRead(t *testing.T, done <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not finish", label)
		return nil
	}
}

func TestGuildPresenceSharedReadWaitsBeforeGuildRead(t *testing.T) {
	s, sources, release, u := guildPresenceConnectionWaitFixture(t)
	shared := &guildPresenceReadCache{}
	first := startGuildPresenceConnectionWaitRead(s, sources[0], false, true, shared, context.Background(), nil)
	waitTestSignal(t, u.entered, "first shared read did not enter guild read")

	secondStarted := make(chan struct{})
	second := startGuildPresenceConnectionWaitRead(s, sources[1], false, true, shared, context.Background(), secondStarted)
	waitTestSignal(t, secondStarted, "second shared read did not start")
	select {
	case err := <-second:
		t.Fatalf("second shared read finished while the first held the read: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := u.reads.Load(); got != 1 {
		t.Fatalf("second shared read entered guild read while waiting: %d reads", got)
	}

	close(release)
	if err := waitGuildPresenceConnectionWaitRead(t, first, "first shared read"); err != nil {
		t.Fatal(err)
	}
	if err := waitGuildPresenceConnectionWaitRead(t, second, "second shared read"); err != nil {
		t.Fatal(err)
	}
	if got := u.reads.Load(); got != 2 {
		t.Fatalf("shared reads after release=%d, want 2", got)
	}
}

func TestGuildPresenceSharedReadCancellationDoesNotStartGuildRead(t *testing.T) {
	s, sources, release, u := guildPresenceConnectionWaitFixture(t)
	shared := &guildPresenceReadCache{}
	first := startGuildPresenceConnectionWaitRead(s, sources[0], false, true, shared, context.Background(), nil)
	waitTestSignal(t, u.entered, "first shared read did not enter guild read")

	ctx, cancel := context.WithCancel(context.Background())
	secondStarted := make(chan struct{})
	second := startGuildPresenceConnectionWaitRead(s, sources[1], false, true, shared, ctx, secondStarted)
	waitTestSignal(t, secondStarted, "second shared read did not start")
	select {
	case err := <-second:
		t.Fatalf("second shared read finished while the first held the read: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	close(release)

	if err := waitGuildPresenceConnectionWaitRead(t, first, "first shared read"); err != nil {
		t.Fatal(err)
	}
	if err := waitGuildPresenceConnectionWaitRead(t, second, "canceled shared read"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled shared read error=%v, want context.Canceled", err)
	}
	if got := u.reads.Load(); got != 1 {
		t.Fatalf("canceled waiter entered guild read: %d reads", got)
	}
}

func TestGuildPresenceNonSharedReadPathsDoNotSerialize(t *testing.T) {
	for _, tc := range []struct {
		name         string
		withShared   bool
		contribution bool
		hydrate      bool
	}{
		{name: "no_shared", hydrate: true},
		{name: "contribution", withShared: true, contribution: true, hydrate: true},
		{name: "no_hydrate", withShared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sources, release, u := guildPresenceConnectionWaitFixture(t)
			var shared *guildPresenceReadCache
			if tc.withShared {
				shared = &guildPresenceReadCache{}
			}
			first := startGuildPresenceConnectionWaitRead(s, sources[0], tc.contribution, tc.hydrate, shared, context.Background(), nil)
			waitTestSignal(t, u.entered, "first read did not enter guild read")
			second := startGuildPresenceConnectionWaitRead(s, sources[1], tc.contribution, tc.hydrate, shared, context.Background(), nil)
			waitTestSignal(t, u.entered, "second non-shared read was serialized")
			if got := u.reads.Load(); got != 2 {
				t.Fatalf("concurrent non-shared reads=%d, want 2", got)
			}
			close(release)
			if err := waitGuildPresenceConnectionWaitRead(t, first, "first read"); err != nil {
				t.Fatal(err)
			}
			if err := waitGuildPresenceConnectionWaitRead(t, second, "second read"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
