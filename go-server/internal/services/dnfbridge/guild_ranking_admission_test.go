package dnfbridge

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func TestGuildRankingQueryNeverWaitsForGlobalSampler(t *testing.T) {
	s, peers, _, _ := rankingBridgeFixture(t)
	gate, wake := s.currentGuildRankingChannels()
	gate <- struct{}{}
	defer func() { <-gate }()
	done := make(chan error, 1)
	go func() { done <- s.handleCurrentGuildRanking(peers[0], []byte{1, 0}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("query waited for global sample")
	}
	if len(wake) != 1 {
		t.Fatal("expired query did not schedule sample")
	}
	for i := 0; i < 100; i++ {
		s.requestCurrentGuildRankingRefresh()
	}
	if len(wake) != 1 {
		t.Fatal("duplicate samples queued")
	}
	if body := guildReply(t, peers[0], 1503); len(body) == 0 || body[0] != 0 {
		t.Fatalf("stale scores disclosed: %x", body)
	}
}

func TestGuildRankingWaitingRefreshCanCancel(t *testing.T) {
	s, _, _, _ := rankingBridgeFixture(t)
	gate, _ := s.currentGuildRankingChannels()
	gate <- struct{}{}
	defer func() { <-gate }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.refreshCurrentGuildRanking(ctx, r.GuildOwner{}); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled refresh retained lock waiter")
	}
}
