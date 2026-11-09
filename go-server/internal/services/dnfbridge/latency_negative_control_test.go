package dnfbridge

import (
	"context"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

type latencyRankingCallProbe struct {
	r.CharacterRepository
	calls int
}

func (p *latencyRankingCallProbe) ListByAccount(context.Context, string, int) ([]r.CharacterRecord, error) {
	p.calls++
	return nil, fmt.Errorf("global sampling must not execute on client query")
}
func TestGuildRankingClientQueryDoesNotSampleAccounts(t *testing.T) {
	s, peers, repos, _ := rankingBridgeFixture(t)
	probe := &latencyRankingCallProbe{CharacterRepository: repos.Character}
	repos.Character = probe
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	if err := s.handleCurrentGuildRanking(peers[0], []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 0 {
		t.Fatalf("client query executed %d account samples", probe.calls)
	}
}
