package guild

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func teleportFixture(t *testing.T, count int) (r.Group, TeleportQuery, time.Time) {
	t.Helper()
	repos, purchase, now := memberExpansionFixture(t)
	ctx := context.Background()
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Benefits = map[byte]Benefit{7: {Type: 7, Value: 2}}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	q := TeleportQuery{RequestID: fmt.Sprintf("%064x", 101), GuildID: 1, Initiator: purchase.Owner, Destination: TeleportLocation{Town: 40, Area: 4, X: 500, Y: 300, Direction: 5}, MinimumLevel: 20, Revalidate: func() bool { return true }}
	q.JoinedAt = reg.Guilds[1].Members[purchase.Owner.CharacterID].JoinedAt
	for i := 1; i <= count; i++ {
		owner := r.GuildOwner{CharacterID: fmt.Sprint(i), AccountID: fmt.Sprint("a", i)}
		actor, found, _ := repos.Character.Load(ctx, owner.CharacterID)
		if !found {
			actor = r.CharacterRecord{CharacterID: owner.CharacterID, AccountID: owner.AccountID, Name: fmt.Sprint("角色", i)}
		}
		actor.Level = 50
		actor.Stats = map[string]int64{"town_id": 38, "area_id": 0, "pos_x": 100, "pos_y": 200, "direction": 5, "gold": 1234, "area_state": 3}
		if err := repos.Character.Save(ctx, actor); err != nil {
			t.Fatal(err)
		}
		q.Participants = append(q.Participants, TeleportParticipant{Owner: owner, From: CharacterTeleportLocation(actor)})
	}
	return repos, q, now
}

func TestGuildTeleportAtomicAllMembersReplayAndReconnect(t *testing.T) {
	for _, n := range []int{1, 2, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			repos, q, now := teleportFixture(t, n)
			ctx := context.Background()
			if _, err := CommitTeleport(ctx, repos.Guild, q, now); err != nil {
				t.Fatal(err)
			}
			for _, p := range q.Participants {
				c, _, err := repos.Character.Load(ctx, p.Owner.CharacterID)
				if err != nil || CharacterTeleportLocation(c) != q.Destination || c.Stats["gold"] != 1234 {
					t.Fatal("partial move", c, err)
				}
			}
			reg, err := Load(ctx, repos.Settings)
			if err != nil || reg.Guilds[1].Benefits[7].Value != 1 || len(reg.Teleports) != 1 {
				t.Fatal("wrong use/receipt", reg, err)
			}
			// After a later move, the original request must not move anyone again.
			c, _, _ := repos.Character.Load(ctx, q.Initiator.CharacterID)
			c.Stats["pos_x"] = 999
			_ = repos.Character.Save(ctx, c)
			if _, err := CommitTeleport(ctx, repos.Guild, q, now.Add(time.Second)); err != nil {
				t.Fatal("replay", err)
			}
			c, _, _ = repos.Character.Load(ctx, q.Initiator.CharacterID)
			if c.Stats["pos_x"] != 999 {
				t.Fatal("old receipt moved player again")
			}
			reg, _ = Load(ctx, repos.Settings)
			if reg.Guilds[1].Benefits[7].Value != 1 {
				t.Fatal("replay consumed use")
			}
		})
	}
}

type lateFailTeleportUOW struct{ r.GuildUnitOfWork }

func (u lateFailTeleportUOW) WithinGuildTravel(ctx context.Context, owners []r.GuildOwner, apply func(r.Group) error) error {
	return u.GuildUnitOfWork.(r.GuildTravelUnitOfWork).WithinGuildTravel(ctx, owners, func(tx r.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return errors.New("receipt stage failed")
	})
}

func TestGuildTeleportRejectsWithoutChargeOrPartialLocations(t *testing.T) {
	for _, mode := range []string{"late_failure", "stale_location", "wrong_account", "low_level", "no_uses", "wrong_guild", "duplicate_owner", "five_owners", "stale_session", "late_session"} {
		t.Run(mode, func(t *testing.T) {
			repos, q, now := teleportFixture(t, 4)
			ctx := context.Background()
			uow := repos.Guild
			switch mode {
			case "late_failure":
				uow = lateFailTeleportUOW{uow}
			case "stale_location":
				q.Participants[3].From.X++
			case "wrong_account":
				q.Participants[3].Owner.AccountID = "other"
			case "low_level":
				q.MinimumLevel = 90
			case "no_uses":
				reg, _ := Load(ctx, repos.Settings)
				reg.Guilds[1].Benefits[7] = Benefit{Type: 7}
				_ = Save(ctx, repos.Settings, reg, now)
			case "wrong_guild":
				q.GuildID = 2
			case "duplicate_owner":
				q.Participants[3] = q.Participants[2]
			case "five_owners":
				q.Participants = append(q.Participants, q.Participants[0])
			case "stale_session":
				q.Revalidate = func() bool { return false }
			case "late_session":
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 3 }
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if _, err := CommitTeleport(ctx, uow, q, now); err == nil {
				t.Fatal("unexpected commit")
			}
			after, _, _ := repos.Settings.Load(ctx, RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("registry changed")
			}
			for i := 1; i <= 4; i++ {
				c, _, _ := repos.Character.Load(ctx, fmt.Sprint(i))
				if c.Stats["town_id"] != 38 || c.Stats["pos_x"] != 100 {
					t.Fatal("partial location", c)
				}
			}
		})
	}
}

func TestGuildTeleportConcurrentReplayConsumesOnce(t *testing.T) {
	repos, q, now := teleportFixture(t, 4)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := CommitTeleport(context.Background(), repos.Guild, q, now); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reg, err := Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Benefits[7].Value != 1 || len(reg.Teleports) != 1 {
		t.Fatal("concurrent repeat", reg, err)
	}
}

func TestGuildTravelKeepsOrdinaryTwoOwnerBoundary(t *testing.T) {
	repos, q, _ := teleportFixture(t, 4)
	owners := []r.GuildOwner{}
	for _, p := range q.Participants {
		owners = append(owners, p.Owner)
	}
	called := false
	if err := repos.Guild.WithinGuild(context.Background(), owners, func(r.Group) error { called = true; return nil }); !errors.Is(err, r.ErrGuildTransaction) || called {
		t.Fatal("ordinary scope widened", err)
	}
	if err := repos.Guild.(r.GuildTravelUnitOfWork).WithinGuildTravel(context.Background(), owners, func(tx r.Group) error { _, _, err := tx.Character.Load(context.Background(), "999"); return err }); !errors.Is(err, r.ErrGuildTransaction) {
		t.Fatal("unlisted character available", err)
	}
}
