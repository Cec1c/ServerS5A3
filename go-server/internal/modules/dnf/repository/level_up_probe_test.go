package repository_test

import (
	"context"
	"errors"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type rewardProbeStore struct {
	r.CharacterRepository
	probe   r.LevelUpRewardProbe
	err     error
	found   bool
	loads   int
	loadErr error
}

func (s *rewardProbeStore) LoadLevelUpRewardProbe(context.Context, string) (r.LevelUpRewardProbe, bool, error) {
	return s.probe, s.found, s.err
}
func (s *rewardProbeStore) Load(ctx context.Context, id string) (r.CharacterRecord, bool, error) {
	s.loads++
	if s.loadErr != nil {
		return r.CharacterRecord{}, false, s.loadErr
	}
	return s.CharacterRepository.Load(ctx, id)
}
func TestLevelUpRewardProbeOptionalProjectionAndFailureCompatibility(t *testing.T) {
	ctx := context.Background()
	base := memory.NewMemoryGroup().Character
	if err := base.Save(ctx, r.CharacterRecord{CharacterID: "77", AccountID: "a", Level: 90, Stats: map[string]int64{r.LevelUpEventKey(85): 1}}); err != nil {
		t.Fatal(err)
	}
	s := &rewardProbeStore{CharacterRepository: base, probe: r.LevelUpRewardProbe{CharacterID: "77", AccountID: "a", Level: 90}, found: true}
	p, found, err := r.ProbeLevelUpRewards(ctx, s, "77")
	if err != nil || !found || p.Pending || s.loads != 0 {
		t.Fatal(p, found, err, s.loads)
	}
	s.found = false
	if _, found, err = r.ProbeLevelUpRewards(ctx, s, "77"); err != nil || found || s.loads != 0 {
		t.Fatal(found, err, s.loads)
	}
	s.err = errors.New("projection unavailable")
	p, found, err = r.ProbeLevelUpRewards(ctx, s, "77")
	if err != nil || !found || !p.Pending || p.Level != 90 || s.loads != 1 {
		t.Fatal(p, found, err, s.loads)
	}
	s.loadErr = errors.New("backing read failed")
	if _, _, err = r.ProbeLevelUpRewards(ctx, s, "77"); !errors.Is(err, s.loadErr) {
		t.Fatal("failed reads cannot become absence", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	loads := s.loads
	if _, _, err = r.ProbeLevelUpRewards(canceled, s, "77"); !errors.Is(err, context.Canceled) || s.loads != loads {
		t.Fatal(err, s.loads)
	}
	p, found, err = r.ProbeLevelUpRewards(ctx, base, "77")
	if err != nil || !found || !p.Pending {
		t.Fatal("legacy store fallback", p, found, err)
	}
}
