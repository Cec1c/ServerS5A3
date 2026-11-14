package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/adventuregroup"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

type summaryOnlyCharacters struct {
	r.CharacterRepository
	calls int
}

func (c *summaryOnlyCharacters) ListAccountCharacterLevels(context.Context, string, int) ([]r.AccountCharacterLevel, error) {
	c.calls++
	return []r.AccountCharacterLevel{{Level: 1}, {Level: 2}, {Level: 90, Deleted: true}}, nil
}

func TestAdventureSummaryUsesNarrowProjectionAndNestedMemo(t *testing.T) {
	chars := &summaryOnlyCharacters{}
	s := &Service{options: options{accountID: "a"}, adventureGroupTable: loadAdventureGroupTestTables(t), repositoryProvider: func() (r.Group, bool) { return r.Group{Character: chars}, true }}
	ctx, memo := withEquipMode0RepositoryMemo(context.Background())
	ctx, nested := withEquipMode0RepositoryMemo(ctx)
	if memo != nested {
		t.Fatal("nested appearance builders discarded memo")
	}
	want, err := s.adventureGroupTable.Calculate([]adventuregroup.Character{{Level: 1}, {Level: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got, err := s.currentAccountAdventureGroupSummary(ctx, r.CharacterRecord{AccountID: "a"}, true)
		if err != nil || got != want {
			t.Fatal(got, want, err)
		}
	}
	if chars.calls != 1 {
		t.Fatalf("roster reads=%d", chars.calls)
	}
	memo.invalidate()
	if _, err := s.currentAccountAdventureGroupSummary(ctx, r.CharacterRecord{}, false); err != nil {
		t.Fatal(err)
	}
	if chars.calls != 2 {
		t.Fatal("invalidation did not reload")
	}
}
