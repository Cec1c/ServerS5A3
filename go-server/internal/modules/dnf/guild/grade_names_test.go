package guild

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildGradeNamesBoundsReplayAndRoleIdentity(t *testing.T) {
	for _, name := range []string{strings.Repeat("a", 20), " " + strings.Repeat("中", 11), strings.Repeat("𠀀", 5) + "abc", "同名职位"} {
		t.Run(name, func(t *testing.T) {
			reg, g, master, _ := textRegistry(t)
			now := g.UpdatedAt.Add(time.Hour)
			members, _ := json.Marshal(g.Members)
			masterName := g.MasterName
			for grade := byte(1); grade <= 5; grade++ {
				if _, err := reg.SetGradeName(master, grade, name, now); err != nil {
					t.Fatal(err)
				}
				if g.GradeNames[grade] != name {
					t.Fatal("name not stored exactly")
				}
			}
			before, _ := json.Marshal(reg)
			if _, err := reg.SetGradeName(master, 1, name, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(reg)
			if string(before) != string(after) {
				t.Fatal("same name changed timestamp")
			}
			afterMembers, _ := json.Marshal(g.Members)
			if string(members) != string(afterMembers) || g.MasterName != masterName || g.MasterCharacterID != "1" {
				t.Fatal("role rename changed member identity")
			}
		})
	}
}

func TestGuildGradeNamesInvalidAndUnauthorizedPreserveState(t *testing.T) {
	for _, mode := range []string{"empty", "blank", "control", "bad_utf8", "utf16_limit", "byte_limit", "zero", "six", "foreign", "wrong_account", "all_bits_member", "wrong_master", "wrong_grade"} {
		t.Run(mode, func(t *testing.T) {
			reg, g, master, member := textRegistry(t)
			owner, grade, name, want := master, byte(4), "新称", ErrInvalidText
			switch mode {
			case "empty":
				name = ""
			case "blank":
				name = " \u3000 "
			case "control":
				name = "a\tb"
			case "bad_utf8":
				name = string([]byte{0xff})
			case "utf16_limit":
				name = strings.Repeat("a", 21)
			case "byte_limit":
				name = strings.Repeat("中", 12)
			case "zero":
				grade = 0
			case "six":
				grade = 6
			case "foreign":
				owner, want = r.GuildOwner{CharacterID: "99", AccountID: "a99"}, ErrNotMember
			case "wrong_account":
				owner.AccountID, want = "wrong", ErrNotMember
			case "all_bits_member":
				owner, want = member, ErrPermissionDenied
				g.GradePermissions = map[byte]uint32{4: GradePermissionMask}
			case "wrong_master":
				g.MasterCharacterID, want = "2", ErrPermissionDenied
			case "wrong_grade":
				m := g.Members["1"]
				m.Grade = 2
				g.Members["1"], want = m, ErrPermissionDenied
			}
			before, _ := json.Marshal(reg)
			if _, err := reg.SetGradeName(owner, grade, name, time.Now()); !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			after, _ := json.Marshal(reg)
			if string(before) != string(after) {
				t.Fatal("rejected rename mutated registry")
			}
		})
	}
}

func TestGuildGradeNamesRollbackAndConcurrentPermissions(t *testing.T) {
	ctx, repos := context.Background(), guildTestGroup(t, 2)
	now := time.Unix(1700000000, 0)
	reg, g, master, _ := textRegistry(t)
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	edit := func(uow r.GuildUnitOfWork, permission bool) error {
		return Update(ctx, uow, []r.GuildOwner{master}, now, func(_ r.Group, current *Registry) error {
			if permission {
				_, err := current.SetGradePermissions(master, 4, 16, now.Add(time.Hour))
				return err
			}
			_, err := current.SetGradeName(master, 4, "新成员名", now.Add(time.Hour))
			return err
		})
	}
	boom := errors.New("late rename failure")
	if err := edit(lateFailGuildTx{base: repos.Guild, fail: boom}, false); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	current, err := Load(ctx, repos.Settings)
	if err != nil || current.Guilds[g.ID].GradeNames[4] != g.GradeNames[4] {
		t.Fatal("failed rename persisted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, permission := range []bool{false, true} {
		wg.Go(func() { results <- edit(repos.Guild, permission) })
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, err = Load(ctx, repos.Settings)
	if err != nil || current.Guilds[g.ID].GradeNames[4] != "新成员名" || GradePermissionsFor(current.Guilds[g.ID], 4) != 16 {
		t.Fatal("concurrent name/mask edit lost committed state")
	}
}
