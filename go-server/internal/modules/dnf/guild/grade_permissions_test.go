package guild

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildGradePermissionsDefaultsZeroAndReplay(t *testing.T) {
	reg, g, master, _ := textRegistry(t)
	before, _ := json.Marshal(g)
	now := g.UpdatedAt.Add(time.Hour)
	if _, err := reg.SetGradePermissions(master, 2, DefaultGradePermissions(2), now); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(g)
	if string(before) != string(after) {
		t.Fatal("unchanged defaults mutated registry")
	}
	for grade := byte(2); grade <= 5; grade++ {
		if _, err := reg.SetGradePermissions(master, grade, GradePermissionMask, now); err != nil {
			t.Fatal(err)
		}
		if GradePermissionsFor(g, grade) != GradePermissionMask {
			t.Fatal("mask not stored")
		}
	}
	if _, err := reg.SetGradePermissions(master, 2, 0, now); err != nil {
		t.Fatal(err)
	}
	if mask, exists := g.GradePermissions[2]; !exists || mask != 0 {
		t.Fatal("zero fell back to deputy defaults")
	}
	before, _ = json.Marshal(g)
	if _, err := reg.SetGradePermissions(master, 2, 0, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, _ = json.Marshal(g)
	if string(before) != string(after) {
		t.Fatal("same request changed timestamp")
	}
	if g.Members["1"].Grade != 1 || g.Members["2"].Grade != 4 || len(g.Members) != 2 {
		t.Fatal("role definition changed members")
	}
}

func TestGuildGradePermissionsFailurePreservesRegistry(t *testing.T) {
	for _, mode := range []string{"nonmember", "wrong_account", "member_all_bits", "master_mismatch", "master_grade_mismatch", "target_master", "unknown_mask"} {
		t.Run(mode, func(t *testing.T) {
			reg, g, master, member := textRegistry(t)
			owner := master
			grade := byte(2)
			mask := uint32(0)
			want := ErrPermissionDenied
			switch mode {
			case "nonmember":
				owner = r.GuildOwner{CharacterID: "99", AccountID: "a99"}
				want = ErrNotMember
			case "wrong_account":
				owner.AccountID = "wrong"
				want = ErrNotMember
			case "member_all_bits":
				owner = member
				g.GradePermissions = map[byte]uint32{4: GradePermissionMask}
			case "master_mismatch":
				g.MasterCharacterID = "2"
			case "master_grade_mismatch":
				m := g.Members["1"]
				m.Grade = 2
				g.Members["1"] = m
			case "target_master":
				grade = 1
				want = ErrInvalidGradePermissions
			case "unknown_mask":
				mask = 1 << 31
				want = ErrInvalidGradePermissions
			}
			before, _ := json.Marshal(reg)
			if _, err := reg.SetGradePermissions(owner, grade, mask, time.Now()); !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			after, _ := json.Marshal(reg)
			if string(before) != string(after) {
				t.Fatal("failed edit changed registry")
			}
		})
	}
}

func TestGuildGradePermissionsRollbackAndConcurrentRoles(t *testing.T) {
	ctx, repos := context.Background(), guildTestGroup(t, 2)
	now := time.Unix(1700000000, 0)
	reg, g, master, _ := textRegistry(t)
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	edit := func(grade byte, mask uint32, uow r.GuildUnitOfWork) error {
		return Update(ctx, uow, []r.GuildOwner{master}, now, func(_ r.Group, current *Registry) error {
			_, err := current.SetGradePermissions(master, grade, mask, now.Add(time.Hour))
			return err
		})
	}
	boom := errors.New("late failure")
	if err := edit(2, 0, lateFailGuildTx{base: repos.Guild, fail: boom}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	current, err := Load(ctx, repos.Settings)
	if err != nil || GradePermissionsFor(current.Guilds[g.ID], 2) != DefaultGradePermissions(2) {
		t.Fatal("failed edit persisted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, grade := range []byte{2, 4} {
		wg.Go(func() { results <- edit(grade, uint32(1)<<grade, repos.Guild) })
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, err = Load(ctx, repos.Settings)
	if err != nil || current.Guilds[g.ID].GradePermissions[2] != 4 || current.Guilds[g.ID].GradePermissions[4] != 16 {
		t.Fatal("concurrent edits lost a role")
	}
}
