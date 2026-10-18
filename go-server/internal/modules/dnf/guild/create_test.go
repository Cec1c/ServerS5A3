package guild

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func guildTestGroup(t *testing.T, n int) r.Group {
	t.Helper()
	g := memory.NewMemoryGroup()
	for i := 1; i <= n; i++ {
		id := fmt.Sprint(i)
		if err := g.Character.Save(context.Background(), r.CharacterRecord{CharacterID: id, AccountID: "a" + id, Name: "角色" + id, Level: 90, Job: "0", Stats: map[string]int64{"gold": 900000, "sp": 123, "grow_type": 1}}); err != nil {
			t.Fatal(err)
		}
	}
	return g
}
func guildRequest(creator, founder int, name string, operation int) CreateRequest {
	return CreateRequest{Creator: r.GuildOwner{AccountID: fmt.Sprint("a", creator), CharacterID: fmt.Sprint(creator)}, Founder: r.GuildOwner{AccountID: fmt.Sprint("a", founder), CharacterID: fmt.Sprint(founder)}, Name: name, Promotion: "共同冒险", OperationID: fmt.Sprintf("%032x", operation)}
}
func TestCreateGuildPersistsBothMembersAndDebitsOnce(t *testing.T) {
	ctx := context.Background()
	g := guildTestGroup(t, 2)
	now := time.Now()
	req := guildRequest(1, 2, "测试公会", 1)
	first, err := CreateWithFoundingMember(ctx, g.Guild, req, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replay || first.GoldAfter != 600000 || len(first.Guild.Members) != 2 {
		t.Fatalf("result=%+v", first)
	}
	for i := 0; i < 3; i++ {
		again, err := CreateWithFoundingMember(ctx, g.Guild, req, now)
		if err != nil || !again.Replay || again.Guild.ID != first.Guild.ID {
			t.Fatalf("replay=%+v err=%v", again, err)
		}
	}
	loaded, err := Load(ctx, g.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Guilds) != 1 || len(loaded.Creations) != 1 {
		t.Fatalf("registry=%+v", loaded)
	}
	for id, wantGold := range map[string]int64{"1": 600000, "2": 900000} {
		ch, _, err := g.Character.Load(ctx, id)
		if err != nil || ch.Stats["gold"] != wantGold || ch.Stats["sp"] != 123 || ch.Level != 90 {
			t.Fatalf("character=%+v err=%v", ch, err)
		}
	}
}

func TestGuildKeepsSocialGroupAndRejectsUnrepresentableIdentity(t *testing.T) {
	ctx := context.Background()
	for _, group := range []int64{0, 7, -1, 250, 256} {
		t.Run(fmt.Sprint(group), func(t *testing.T) {
			g := guildTestGroup(t, 2)
			c, _, _ := g.Character.Load(ctx, "2")
			c.Stats["server_group_id"] = group
			if err := g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			result, err := CreateWithFoundingMember(ctx, g.Guild, guildRequest(1, 2, "测试公会", 1), time.Now())
			if group < 0 || group >= 250 {
				if !errors.Is(err, ErrCharacterNotFound) {
					t.Fatalf("invalid group accepted: %v", err)
				}
				creator, _, _ := g.Character.Load(ctx, "1")
				if creator.Stats["gold"] != 900000 {
					t.Fatal("invalid identity charged")
				}
			} else if err != nil || result.Guild.Members["2"].ServerGroup != byte(group) {
				t.Fatalf("social group lost: %+v %v", result, err)
			}
		})
	}
}

type lateFailGuildTx struct {
	base r.GuildUnitOfWork
	fail error
}

func (u lateFailGuildTx) WithinGuild(ctx context.Context, owners []r.GuildOwner, apply func(r.Group) error) error {
	return u.base.WithinGuild(ctx, owners, func(g r.Group) error {
		if err := apply(g); err != nil {
			return err
		}
		return u.fail
	})
}

func TestCreateGuildFailureRollsBackGoldMembershipAndReceipt(t *testing.T) {
	ctx := context.Background()
	g := guildTestGroup(t, 2)
	boom := errors.New("late failure")
	if _, err := CreateWithFoundingMember(ctx, lateFailGuildTx{g.Guild, boom}, guildRequest(1, 2, "测试公会", 1), time.Now()); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	ch, _, _ := g.Character.Load(ctx, "1")
	reg, err := Load(ctx, g.Settings)
	if err != nil || ch.Stats["gold"] != 900000 || len(reg.Guilds) != 0 || len(reg.Creations) != 0 {
		t.Fatalf("gold=%d registry=%+v err=%v", ch.Stats["gold"], reg, err)
	}
}

func TestCreateGuildConcurrentNameAndFounderExclusion(t *testing.T) {
	for _, sameFounder := range []bool{false, true} {
		t.Run(fmt.Sprint(sameFounder), func(t *testing.T) {
			g := guildTestGroup(t, 4)
			requests := []CreateRequest{guildRequest(1, 2, "公会甲", 1), guildRequest(3, 4, "公会甲", 2)}
			if sameFounder {
				requests[1] = guildRequest(3, 2, "公会乙", 2)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, req := range requests {
				wg.Add(1)
				go func(q CreateRequest) {
					defer wg.Done()
					<-start
					_, err := CreateWithFoundingMember(context.Background(), g.Guild, q, time.Now())
					results <- err
				}(req)
			}
			close(start)
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else if !errors.Is(err, ErrNameExists) && !errors.Is(err, ErrAlreadyMember) {
					t.Fatal(err)
				}
			}
			reg, _ := Load(context.Background(), g.Settings)
			a, _, _ := g.Character.Load(context.Background(), "1")
			b, _, _ := g.Character.Load(context.Background(), "3")
			if success != 1 || len(reg.Guilds) != 1 || a.Stats["gold"]+b.Stats["gold"] != 1500000 {
				t.Fatalf("success=%d registry=%+v gold=%d", success, reg, a.Stats["gold"]+b.Stats["gold"])
			}
		})
	}
}

func TestCreateGuildRefusesChangedOwnerBalanceAndConsent(t *testing.T) {
	for _, scenario := range []string{"owner", "gold", "consent", "self", "receipt"} {
		t.Run(scenario, func(t *testing.T) {
			g := guildTestGroup(t, 2)
			req := guildRequest(1, 2, "测试公会", 1)
			switch scenario {
			case "owner":
				req.Founder.AccountID = "another"
			case "gold":
				ch, _, _ := g.Character.Load(context.Background(), "1")
				ch.Stats["gold"] = 1
				_ = g.Character.Save(context.Background(), ch)
			case "consent":
				req.Revalidate = func() bool { return false }
			case "self":
				req.Founder = req.Creator
			case "receipt":
				req.OperationID = ""
			}
			if _, err := CreateWithFoundingMember(context.Background(), g.Guild, req, time.Now()); err == nil {
				t.Fatal("accepted invalid creation")
			}
			reg, _ := Load(context.Background(), g.Settings)
			if len(reg.Guilds) != 0 {
				t.Fatal("unexpected guild")
			}
		})
	}
}

func TestGuildTransactionRejectsOtherCharactersAndSettings(t *testing.T) {
	g := guildTestGroup(t, 3)
	ctx := context.Background()
	err := g.Guild.WithinGuild(ctx, []r.GuildOwner{{AccountID: "a1", CharacterID: "1"}}, func(tx r.Group) error {
		if _, _, e := tx.Character.Load(ctx, "2"); !errors.Is(e, r.ErrGuildTransaction) {
			t.Fatalf("cross character: %v", e)
		}
		if e := tx.Character.Save(ctx, r.CharacterRecord{CharacterID: "1", AccountID: "a2"}); !errors.Is(e, r.ErrGuildTransaction) {
			t.Fatalf("owner rewrite: %v", e)
		}
		if _, _, e := tx.Settings.Load(ctx, "unrelated"); !errors.Is(e, r.ErrGuildTransaction) {
			t.Fatalf("settings read: %v", e)
		}
		if e := tx.Settings.Save(ctx, r.SettingsRecord{Scope: "unrelated"}); !errors.Is(e, r.ErrGuildTransaction) {
			t.Fatalf("settings write: %v", e)
		}
		if tx.Account != nil || tx.Inventory != nil || tx.Quest != nil {
			t.Fatal("unrelated repositories exposed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
