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

func applicationRegistry(t *testing.T) (Registry, *Guild, time.Time) {
	t.Helper()
	reg, g, _, _ := textRegistry(t)
	now := g.UpdatedAt.Add(time.Hour)
	g.JoinMode = 0
	if err := reg.Apply(g.ID, r.CharacterRecord{CharacterID: "3", AccountID: "a3", Name: "申请者", Level: 90, Stats: map[string]int64{"grow_type": 33}}, "申请", now); err != nil {
		t.Fatal(err)
	}
	return reg, g, now.Add(time.Hour)
}

func TestGuildApplicationConcurrentLastSlotAndRollback(t *testing.T) {
	ctx, repos := context.Background(), guildTestGroup(t, 4)
	now := time.Unix(1700000000, 0)
	reg := NewRegistry()
	master, _, _ := repos.Character.Load(ctx, "1")
	g, err := reg.Create(master, "测试公会", now)
	if err != nil {
		t.Fatal(err)
	}
	member, _, _ := repos.Character.Load(ctx, "2")
	if _, err := reg.JoinDirect(g.ID, member, now); err != nil {
		t.Fatal(err)
	}
	g.JoinMode, g.MemberLimit = 0, 3
	for _, id := range []string{"3", "4"} {
		c, _, _ := repos.Character.Load(ctx, id)
		if err := reg.Apply(g.ID, c, "申请", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	owner := r.GuildOwner{CharacterID: "1", AccountID: "a1"}
	decide := func(id string, uow r.GuildUnitOfWork) error {
		return Update(ctx, uow, []r.GuildOwner{owner, {CharacterID: id, AccountID: "a" + id}}, now.Add(time.Hour), func(_ r.Group, current *Registry) error {
			_, err := current.DecideApplication(1, "1", id, true, now.Add(time.Hour))
			return err
		})
	}
	before, _, _ := repos.Settings.Load(ctx, RegistryScope)
	boom := errors.New("rollback after decision")
	if err := decide("3", lateFailGuildTx{base: repos.Guild, fail: boom}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	after, _, _ := repos.Settings.Load(ctx, RegistryScope)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(b) != string(a) {
		t.Fatal("failed transaction persisted decision")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"3", "4"} {
		wg.Go(func() { results <- decide(id, repos.Guild) })
	}
	wg.Wait()
	close(results)
	succeeded, full := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrMemberLimit) {
			full++
		} else {
			t.Fatal(err)
		}
	}
	current, err := Load(ctx, repos.Settings)
	if err != nil || succeeded != 1 || full != 1 || len(current.Guilds[1].Members) != 3 || len(current.Guilds[1].Applications) != 1 {
		t.Fatalf("last slot: success=%d full=%d state=%+v err=%v", succeeded, full, current, err)
	}
	for _, id := range []string{"1", "2", "3", "4"} {
		c, _, _ := repos.Character.Load(ctx, id)
		if c.Stats["gold"] != 900000 {
			t.Fatal("decision changed character assets")
		}
	}
}

func TestGuildApplicationFailureLeavesRegistryUnchanged(t *testing.T) {
	for _, name := range []string{"full", "already_joined", "ordinary_member", "nonmember", "missing_application"} {
		t.Run(name, func(t *testing.T) {
			reg, g, now := applicationRegistry(t)
			actor, target, want := "1", "3", ErrPermissionDenied
			switch name {
			case "full":
				g.MemberLimit = len(g.Members)
				want = ErrMemberLimit
			case "already_joined":
				// Retain an old application to exercise the domain guard itself.
				reg.Guilds[2] = &Guild{ID: 2, Members: map[string]Member{"3": {CharacterID: "3", AccountID: "a3"}}}
				want = ErrAlreadyMember
			case "ordinary_member":
				actor = "2"
			case "nonmember":
				actor = "99"
			case "missing_application":
				target, want = "99", ErrApplicationNotFound
			}
			before, err := json.Marshal(reg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reg.DecideApplication(g.ID, actor, target, true, now); !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			after, err := json.Marshal(reg)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("failed approval changed applications, membership or timestamps")
			}
		})
	}
}

func TestGuildApplicationDecisionsRemoveOnlyCommittedApplications(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(map[bool]string{false: "deny", true: "approve"}[approve], func(t *testing.T) {
			reg, g, now := applicationRegistry(t)
			other := &Guild{ID: 2, Applications: map[string]Application{"3": g.Applications["3"]}, Members: map[string]Member{}}
			reg.Guilds[2] = other
			if !approve {
				g.MemberLimit = len(g.Members)
			} // denial needs no free slot
			joined, err := reg.DecideApplication(g.ID, "1", "3", approve, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, found := g.Applications["3"]; found || !g.UpdatedAt.Equal(now) {
				t.Fatal("decision not committed")
			}
			if approve {
				if joined.AccountID != "a3" || joined.GrowType != "33" || joined.Grade != DefaultMemberGrade || len(other.Applications) != 0 {
					t.Fatal("approval identity or other applications incorrect")
				}
			} else if len(g.Members) != 2 || len(other.Applications) != 1 {
				t.Fatal("denial affected membership or another guild")
			}
			before, _ := json.Marshal(reg)
			if _, err := reg.DecideApplication(g.ID, "1", "3", approve, now.Add(time.Hour)); !errors.Is(err, ErrApplicationNotFound) {
				t.Fatal(err)
			}
			after, _ := json.Marshal(reg)
			if string(before) != string(after) {
				t.Fatal("retry changed committed state")
			}
		})
	}
}
