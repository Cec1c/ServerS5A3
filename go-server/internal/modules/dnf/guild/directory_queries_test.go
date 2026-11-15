package guild

import (
	"context"
	"errors"
	"reflect"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildDirectoryQueriesMatchSourceAndDetachResults(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	other := r.CharacterRecord{CharacterID: "2", AccountID: "b", Name: "other", Level: 90, Stats: map[string]int64{"server_group_id": 1}}
	if _, err := reg.Create(other, "Second", now); err != nil {
		t.Fatal(err)
	}
	applicant := r.CharacterRecord{CharacterID: "3", AccountID: "c", Name: "applicant", Level: 90, Stats: map[string]int64{"server_group_id": 1}}
	reg.Guilds[1].JoinMode = 0
	if err := reg.Apply(1, applicant, "hello", now); err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].MileageHistory = []MileageHistoryEntry{{At: now, CharacterID: owner.CharacterID, CharacterName: "member", Kind: MileageHistoryDungeon, Amount: 10}}
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		view, err := ReadDirectoryView(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{" Guild ", "guild", "SECOND", "missing"} {
			if view.NameAvailable(name) != reg.NameAvailable(name) {
				t.Fatal("name availability", name)
			}
			got, found := view.RosterByName(name)
			want, exists := reg.GuildByName(name)
			if found != exists || found && !reflect.DeepEqual(got, detachedRosterProjection(want)) {
				t.Fatal(name, got, found)
			}
			if found {
				got.Name = "poison"
				delete(got.Members, got.MasterCharacterID)
			}
		}
		for _, filter := range []SearchFilter{{Mode: SearchGuildName}, {Mode: SearchMasterName, Query: "other"}, {Mode: SearchTags, TagMask: 1}, {Mode: 255}} {
			for _, page := range []int{0, 1, 100} {
				total, got, err := view.SearchDirectoryRosters(filter, page, 1)
				wantTotal, want, wantErr := reg.SearchDirectory(filter, page, 1)
				if err != wantErr || total != wantTotal || !reflect.DeepEqual(got, detachedDirectoryRows(want)) {
					t.Fatal(filter, page, total, err)
				}
			}
		}
		if got := view.SearchRosters("", 0, 10); !reflect.DeepEqual(got, detachedDirectoryRows(reg.Search("", 0, 10))) {
			t.Fatal(got)
		}
		applications, err := view.Applications(owner)
		if err != nil || !reflect.DeepEqual(applications, SortedApplications(reg.Guilds[1])) {
			t.Fatal(applications, err)
		}
		applications[0].AccountID = "poison"
		applied := view.ApplicationRosters(r.GuildOwner{AccountID: applicant.AccountID, CharacterID: applicant.CharacterID})
		if len(applied) != 1 || applied[0].ID != 1 {
			t.Fatal(applied)
		}
		if got := view.ApplicationRosters(r.GuildOwner{AccountID: "foreign", CharacterID: applicant.CharacterID}); len(got) != 0 {
			t.Fatal(got)
		}
		history, found := view.MileageHistory(owner)
		if !found || !reflect.DeepEqual(history, reg.Guilds[1].MileageHistory) {
			t.Fatal(history, found)
		}
		history[0].Amount = 999
		var expected []DirectoryMember
		for _, m := range SortedMembers(reg.Guilds[1]) {
			if GradePermissionsFor(reg.Guilds[1], m.Grade)&CargoPermission != 0 {
				expected = append(expected, DirectoryMember{GuildID: 1, CharacterID: m.CharacterID, AccountID: m.AccountID, JoinedAt: m.JoinedAt, Grade: m.Grade})
			}
		}
		if !reflect.DeepEqual(view.CargoMembers(1), expected) {
			t.Fatal("cargo permission differs")
		}
	}
	if store.loads != 1 {
		t.Fatal("unchanged full source loads", store.loads)
	}
}

func TestGuildDirectoryQueriesRevalidateApplicationsAndPermissions(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	reg.Guilds[1].JoinMode = 0
	applicant := r.CharacterRecord{CharacterID: "2", AccountID: "b", Name: "applicant", Level: 90, Stats: map[string]int64{"server_group_id": 1}}
	if err := reg.Apply(1, applicant, "hello", now); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	old, err := ReadDirectoryView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.JoinDirect(1, applicant, now); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	latest, err := ReadDirectoryView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	applications, err := latest.Applications(owner)
	if err != nil || len(applications) != 0 {
		t.Fatal(applications, err)
	}
	if prior, err := old.Applications(owner); err != nil || len(prior) != 1 {
		t.Fatal(prior, err)
	}
	if _, err := latest.Applications(r.GuildOwner{AccountID: applicant.AccountID, CharacterID: applicant.CharacterID}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal(err)
	}
	if _, err := latest.Applications(r.GuildOwner{AccountID: "foreign", CharacterID: owner.CharacterID}); !errors.Is(err, ErrNotMember) {
		t.Fatal(err)
	}
	if _, found := latest.MileageHistory(r.GuildOwner{AccountID: "foreign", CharacterID: owner.CharacterID}); found {
		t.Fatal("foreign history")
	}
}
