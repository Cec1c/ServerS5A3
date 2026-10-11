package adventuregroup

import (
	"context"
	"errors"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestCollectionRowsPreserveChoiceAndRejectIneligible(t *testing.T) {
	characters := []dnfrepo.CharacterRecord{
		{CharacterID: "31", Name: "first", Job: "0", Level: 90, Slot: 0, Stats: map[string]int64{"grow_type": 0x21}},
		{CharacterID: "32", Name: "chosen", Job: "0", Level: 90, Slot: 1, Stats: map[string]int64{"grow_type": 0x21}},
		{CharacterID: "33", Name: "low", Job: "0", Level: 89, Stats: map[string]int64{"grow_type": 0x22}},
		{CharacterID: "34", Name: "first-awakening", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 0x12}},
		{CharacterID: "35", Name: "unknown", Job: "0", Level: 90},
	}
	rows := CollectionRows(characters, nil, 90)
	if len(rows) != 1 || rows[0].Name != "" || rows[0].CharacterID != "31" {
		t.Fatalf("unselected rows=%+v", rows)
	}
	rows = CollectionRows(characters, map[string]string{"0_1": "32"}, 90)
	if len(rows) != 1 || rows[0].Name != "chosen" || rows[0].Stats["grow_type"] != 0x21 {
		t.Fatalf("chosen rows=%+v", rows)
	}
	rows = CollectionRows(characters, map[string]string{"0_1": "missing"}, 90)
	if len(rows) != 1 || rows[0].Name != "" {
		t.Fatal("stale selection must remain selectable")
	}
	if characters[0].Name != "first" {
		t.Fatal("projection mutated repository input")
	}
}

func TestSelectCollectionPersistsOwnedChoiceAndRejectsPeer(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Now()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:1", Metadata: map[string]string{"unrelated": "keep"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []dnfrepo.CharacterRecord{
		{CharacterID: "31", AccountID: "dnf:1", Job: "0", Name: "owned", Level: 90, Stats: map[string]int64{"grow_type": 0x21}},
		{CharacterID: "32", AccountID: "dnf:2", Job: "0", Name: "peer", Level: 90, Stats: map[string]int64{"grow_type": 0x21}},
		{CharacterID: "33", AccountID: "dnf:1", Job: "0", Name: "low", Level: 89, Stats: map[string]int64{"grow_type": 0x21}},
	} {
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	command := SelectCollectionCommand{AccountID: "dnf:1", CharacterID: "31", Job: 0, Branch: 1, ServerGroup: 1, DefaultServerGroup: 1, LevelCap: 90, UpdatedAt: now}
	if _, err := SelectCollection(ctx, repos, command); err != nil {
		t.Fatal(err)
	}
	account, _, _ := repos.Account.Load(ctx, "dnf:1")
	if CollectionSelections(account)["0_1"] != "31" || account.Metadata["unrelated"] != "keep" {
		t.Fatal("choice not durably stored or unrelated metadata replaced")
	}
	for _, id := range []string{"32", "33", "missing"} {
		command.CharacterID = id
		if _, err := SelectCollection(ctx, repos, command); !errors.Is(err, ErrCollectionRepresentative) {
			t.Fatalf("id%s accepted: %v", id, err)
		}
	}
	command.CharacterID = "31"
	command.Branch = 2
	if _, err := SelectCollection(ctx, repos, command); !errors.Is(err, ErrCollectionRepresentative) {
		t.Fatal("wrong transfer accepted")
	}
	command.Branch = 1
	command.ServerGroup = 2
	if _, err := SelectCollection(ctx, repos, command); !errors.Is(err, ErrCollectionRepresentative) {
		t.Fatal("wrong server group accepted")
	}
	account, _, _ = repos.Account.Load(ctx, "dnf:1")
	if CollectionSelections(account)["0_1"] != "31" {
		t.Fatal("rejected selection changed saved choice")
	}
}

func TestCollectionGroupsFollowPVFOrderAndCountDistinctTransfers(t *testing.T) {
	text := "[expert list]\n[group]\n0 `male`\n[info]\n`[swordman]` `second awakening` 1\n[info]\n`[swordman]` `second awakening` 2\n[/group]\n[group]\n1 `female`\n[info]\n`[at swordman]` `second awakening` 1\n[/group]\n[/expert list]"
	doc, err := dnfpvf.Parse("fixture.etc", text)
	if err != nil {
		t.Fatal(err)
	}
	config := RuntimeConfig{CollectionGroups: parseCollectionGroups(doc)}
	if g, ok := config.CollectionGroupFor(11, 1); !ok || g != 1 {
		t.Fatalf("female group=%d %v", g, ok)
	}
	characters := []dnfrepo.CharacterRecord{{CharacterID: "31", Job: "11", Name: "selected", Level: 90, Stats: map[string]int64{"grow_type": 0x21}}, {CharacterID: "32", Job: "0", Name: "unselected", Level: 90, Stats: map[string]int64{"grow_type": 0x21}}}
	counts := config.CollectionCounts(CollectionRows(characters, map[string]string{"11_1": "31"}, 90), 90)
	if counts[0] != 0 || counts[1] != 1 {
		t.Fatalf("counts=%v", counts)
	}
}
