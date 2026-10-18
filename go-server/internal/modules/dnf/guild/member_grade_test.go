package guild

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildMemberGradeReplayAndPreservation(t *testing.T) {
	reg, g, master, member := textRegistry(t)
	before := g.Members[member.CharacterID]
	now := g.UpdatedAt.Add(time.Hour)
	for _, grade := range []byte{2, 3, 4} {
		if _, err := reg.ChangeMemberGrade(master, member, grade, now); err != nil {
			t.Fatal(err)
		}
		want := before
		want.Grade = grade
		if !reflect.DeepEqual(g.Members[member.CharacterID], want) || g.MasterCharacterID != master.CharacterID {
			t.Fatal("rank changed another member field or master")
		}
		encoded, _ := json.Marshal(reg)
		if _, err := reg.ChangeMemberGrade(master, member, grade, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(reg)
		if string(encoded) != string(after) {
			t.Fatal("same rank replay changed registry")
		}
	}
}

func TestGuildMemberGradePermissionAndIdentity(t *testing.T) {
	for _, mode := range []string{"granted", "revoked", "default_deputy", "wrong_actor_account", "wrong_target_account", "nonmember", "foreign", "self", "master", "master_grade", "invalid_zero", "invalid_one", "invalid_five"} {
		t.Run(mode, func(t *testing.T) {
			reg, g, master, member := textRegistry(t)
			third := r.CharacterRecord{CharacterID: "3", AccountID: "a3", Name: "三号"}
			if _, err := reg.JoinDirect(g.ID, third, g.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			actor, target, grade := master, member, byte(2)
			want := ErrPermissionDenied
			switch mode {
			case "granted", "revoked", "default_deputy":
				actor, target = member, r.GuildOwner{CharacterID: "3", AccountID: "a3"}
				if mode == "granted" {
					g.GradePermissions = map[byte]uint32{4: 1 << 11}
					want = nil
				} else if mode == "revoked" {
					g.GradePermissions = map[byte]uint32{4: 0}
				} else {
					m := g.Members["2"]
					m.Grade = 2
					g.Members["2"] = m
				}
			case "wrong_actor_account":
				actor.AccountID, want = "old", ErrNotMember
			case "wrong_target_account":
				target.AccountID, want = "old", ErrNotMember
			case "nonmember":
				target.CharacterID, want = "99", ErrNotMember
			case "foreign":
				delete(g.Members, "3")
				if _, err := reg.Create(third, "另外公会", g.UpdatedAt); err != nil {
					t.Fatal(err)
				}
				target, want = r.GuildOwner{CharacterID: "3", AccountID: "a3"}, ErrNotMember
			case "self":
				target, want = actor, ErrMemberGradeSelf
			case "master":
				actor, target, want = member, master, ErrMemberGradeMaster
				g.GradePermissions = map[byte]uint32{4: 1 << 11}
			case "master_grade":
				m := g.Members["2"]
				m.Grade = 1
				g.Members["2"] = m
				want = ErrMemberGradeMaster
			case "invalid_zero":
				grade, want = 0, ErrInvalidMemberGrade
			case "invalid_one":
				grade, want = 1, ErrInvalidMemberGrade
			case "invalid_five":
				grade, want = 5, ErrInvalidMemberGrade
			}
			before, _ := json.Marshal(reg)
			_, err := reg.ChangeMemberGrade(actor, target, grade, time.Now())
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			if err != nil {
				after, _ := json.Marshal(reg)
				if string(before) != string(after) {
					t.Fatal("rejection changed registry")
				}
			} else if g.Members[target.CharacterID].Grade != grade {
				t.Fatal("granted bit11 ignored")
			}
		})
	}
}

func TestGuildMemberGradeRollbackAndConcurrentPermissionChange(t *testing.T) {
	ctx, repos := context.Background(), guildTestGroup(t, 3)
	reg, g, master, member := textRegistry(t)
	third := r.GuildOwner{CharacterID: "3", AccountID: "a3"}
	c, _, _ := repos.Character.Load(ctx, third.CharacterID)
	if _, err := reg.JoinDirect(g.ID, c, g.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	g.GradePermissions = map[byte]uint32{4: 1 << 11}
	now := g.UpdatedAt.Add(time.Hour)
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	edit := func(uow r.GuildUnitOfWork) error {
		return Update(ctx, uow, []r.GuildOwner{member, third}, now, func(_ r.Group, current *Registry) error {
			_, err := current.ChangeMemberGrade(member, third, 2, now)
			return err
		})
	}
	boom := errors.New("late member rank failure")
	if err := edit(lateFailGuildTx{base: repos.Guild, fail: boom}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	loaded, err := Load(ctx, repos.Settings)
	if err != nil || loaded.Guilds[g.ID].Members["3"].Grade != 4 {
		t.Fatal("rollback failed")
	}
	var wg sync.WaitGroup
	var rankErr, revokeErr error
	wg.Go(func() { rankErr = edit(repos.Guild) })
	wg.Go(func() {
		revokeErr = Update(ctx, repos.Guild, []r.GuildOwner{master}, now, func(_ r.Group, current *Registry) error {
			_, err := current.SetGradePermissions(master, 4, 0, now)
			return err
		})
	})
	wg.Wait()
	if revokeErr != nil || (rankErr != nil && !errors.Is(rankErr, ErrPermissionDenied)) {
		t.Fatalf("rank=%v revoke=%v", rankErr, revokeErr)
	}
	loaded, err = Load(ctx, repos.Settings)
	wantGrade := byte(2)
	if rankErr != nil {
		wantGrade = 4
	}
	if err != nil || GradePermissionsFor(loaded.Guilds[g.ID], 4) != 0 || loaded.Guilds[g.ID].Members["3"].Grade != wantGrade {
		t.Fatal("concurrent rank/permission changes lost state")
	}
	if err := edit(repos.Guild); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("revoked permission reused: %v", err)
	}
}
