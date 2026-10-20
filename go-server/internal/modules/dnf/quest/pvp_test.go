package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type pvpFailCommit struct {
	dnfrepo.CharacterSettlementUnitOfWork
}

func (u pvpFailCommit) WithinCharacterSettlement(ctx context.Context, id string, apply func(dnfrepo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx dnfrepo.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return errors.New("injected commit failure")
	})
}

func TestPVPWinProgressTransactionAndReplay(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "16", AccountID: "a", Stats: map[string]int64{"exp": 555, "pvp_rank_point": 1200}}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	definition := Definition{Type: "[pvp quest]", SubType: 1, IntData: []int64{3, 0}, Path: "n_quest/pvp/daily_pvp_02.qst"}
	catalog := &Catalog{byID: map[int64]Definition{6688: definition, 6689: definition, 6690: definition, 6691: definition, 6692: definition}}
	if got := definitionInitialTrigger(definition, nil); got != 3 {
		t.Fatalf("initial trigger=%d", got)
	}
	invalid := definition
	invalid.SubType = 99
	if got := definitionInitialTrigger(invalid, nil); got != 1 {
		t.Fatalf("unsupported subtype trigger=%d", got)
	}
	record := dnfrepo.QuestRecord{CharacterID: "16", States: map[int64]dnfrepo.QuestState{
		6688: {Status: "active", ProgressValue: 3},
		6689: {Status: "completed", ProgressValue: 0},
		6691: {Status: "active", ProgressValue: -1},
		6692: {Status: "active", ProgressValue: 4},
	}, Progress: map[int64]dnfrepo.QuestState{6689: {Status: "active", ProgressValue: 3}}}
	if err := repos.Quest.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	owner, _ := NewOwner(repos)
	at := time.Unix(1700000000, 0).UTC()
	failed := repos
	failed.CharacterSettlement = pvpFailCommit{repos.CharacterSettlement}
	failingOwner, _ := NewOwner(failed)
	if result, err := failingOwner.ApplyPVPWin(ctx, catalog, "a", "16", "match1", at); err == nil || result.SnapshotRequired {
		t.Fatal("failed transaction exposed a committed result")
	}
	after, _, _ := repos.Quest.Load(ctx, "16")
	charAfter, _, _ := repos.Character.Load(ctx, "16")
	if !reflect.DeepEqual(after.States, record.States) || !reflect.DeepEqual(charAfter.Stats, character.Stats) {
		t.Fatal("failed commit retained progress or receipt")
	}
	for _, key := range []string{"match1", "match1", "match2", "match3", "match4"} {
		if _, err := owner.ApplyPVPWin(ctx, catalog, "a", "16", key, at); err != nil {
			t.Fatal(err)
		}
		if key == "match1" {
			current, _, _ := repos.Quest.Load(ctx, "16")
			if current.States[6688].ProgressValue != 2 {
				t.Fatal("replayed win decremented twice")
			}
		}
	}
	after, _, _ = repos.Quest.Load(ctx, "16")
	state := after.States[6688]
	if state.ProgressValue != 0 || state.Status != "active" || state.Extra["reward_state"] != "pending" ||
		after.States[6689].Status != "completed" || after.Progress[6689].ProgressValue != 3 ||
		after.States[6691].ProgressValue != -1 || after.States[6692].ProgressValue != 4 || len(after.States) != len(record.States) {
		t.Fatalf("incorrect terminal or unrelated quest states: %+v", after)
	}
	// Accepting another quest after a commit must not make a replay count again.
	after.States[6690] = dnfrepo.QuestState{Status: "active", ProgressValue: 3}
	if err := repos.Quest.Save(ctx, after); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyPVPWin(ctx, catalog, "a", "16", "match4", at); err != nil {
		t.Fatal(err)
	}
	after, _, _ = repos.Quest.Load(ctx, "16")
	if after.States[6690].ProgressValue != 3 {
		t.Fatal("old match advanced a newly accepted quest")
	}
	if _, err := owner.ApplyPVPWin(ctx, catalog, "other-account", "16", "match5", at); err == nil {
		t.Fatal("wrong account accepted")
	}
	charAfter, _, _ = repos.Character.Load(ctx, "16")
	if charAfter.Stats["exp"] != 555 || charAfter.Stats["pvp_rank_point"] != 1200 {
		t.Fatal("quest progress modified character experience or RP")
	}
}
