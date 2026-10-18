package inventory

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func contractClaimFixture(t *testing.T) (r.Group, *Owner, GuildContractClaim, time.Time) {
	t.Helper()
	repos, o, old, now := mileageItemFixture(t, 1)
	ctx := context.Background()
	reg, _ := guild.Load(ctx, repos.Settings)
	reg.Guilds[1].Benefits = map[byte]guild.Benefit{21: {Type: 21, Value: 300, ExpiresAt: now.Add(72 * time.Hour)}}
	if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	q := GuildContractClaim{Owner: old.Owner, RequestID: fmt.Sprintf("%064x", 1), Catalog: guild.ContentCatalog{ContractRewardItemID: 490001385, ContractRewardCount: 1}, Reward: alignedcmd.MagicBoxRewardItem{ItemID: 490001385, Kind: "stackable", StackLimit: 1, SlotStart: 88, SlotEnd: 89, PVFPath: "stackable/contract.stk"}, Revalidate: func() bool { return true }}
	return repos, o, q, now
}

func TestGuildContractClaimDailyBoundaryReplayAndGuildSwitch(t *testing.T) {
	repos, o, q, now := contractClaimFixture(t)
	ctx := context.Background()
	got, err := o.ClaimGuildContract(ctx, q, now)
	if err != nil || len(got.Slots) != 1 || got.Slots[0] != 88 || !got.DayEnd.Equal(itemexpiration.NextDailyDeleteAt(now)) {
		t.Fatal(got, err)
	}
	bag, _, _ := repos.Inventory.Load(ctx, "1")
	item := bag.Slots["0:88"]
	if item.ItemID != 490001385 || item.Count != 1 || !item.ExpireAt.Equal(got.DayEnd) || item.Extra[itemexpiration.DailyDeleteDeadlineKey] == "" {
		t.Fatal(item)
	}
	delete(bag.Slots, "0:88") // The player consumed the box before a lost ACK is retried.
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	o, _ = NewOwner(repos)
	if replay, err := o.ClaimGuildContract(ctx, q, now.Add(time.Hour)); err != nil || !reflect.DeepEqual(replay, got) {
		t.Fatal("replay", replay, err)
	}
	bag, _, _ = repos.Inventory.Load(ctx, "1")
	if bag.Slots["0:88"].Count != 0 {
		t.Fatal("replay restored consumed box")
	}
	q.RequestID = fmt.Sprintf("%064x", 2)
	for _, at := range []time.Time{now.Add(-time.Minute), got.DayEnd.Add(-time.Nanosecond)} {
		if _, err := o.ClaimGuildContract(ctx, q, at); err != guild.ErrContractAlreadyClaimed {
			t.Fatal("same service day", err)
		}
	}
	reg, _ := guild.Load(ctx, repos.Settings)
	g := reg.Guilds[1]
	delete(reg.Guilds, 1)
	g.ID = 2
	reg.Guilds[2] = g
	reg.NextID = 3
	if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ClaimGuildContract(ctx, q, now); err != guild.ErrContractAlreadyClaimed {
		t.Fatal("guild switch granted second box", err)
	}
	if _, err := o.ClaimGuildContract(ctx, q, got.DayEnd); err != nil {
		t.Fatal("06:00 did not reset", err)
	}
	q.RequestID = fmt.Sprintf("%064x", 1)
	if _, err := o.ClaimGuildContract(ctx, q, got.DayEnd); err != guild.ErrPurchaseIdentity {
		t.Fatal("old request accepted after guild switch", err)
	}
}

func TestGuildContractClaimFailuresRollback(t *testing.T) {
	for _, mode := range []string{"full", "inventory_save", "registry_save", "stale", "inactive", "foreign", "not_member", "reward", "token"} {
		t.Run(mode, func(t *testing.T) {
			repos, o, q, now := contractClaimFixture(t)
			ctx := context.Background()
			reg, _ := guild.Load(ctx, repos.Settings)
			bag, _, _ := repos.Inventory.Load(ctx, "1")
			switch mode {
			case "full":
				for _, key := range []string{"0:88", "0:89"} {
					bag.Slots[key] = r.ItemStack{ItemID: 3033, Count: 1}
				}
			case "inventory_save":
				o.guild = mileageFailingUow{repos.Guild, false}
			case "registry_save":
				o.guild = mileageFailingUow{repos.Guild, true}
			case "stale":
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 2 }
			case "inactive":
				reg.Guilds[1].Benefits[21] = guild.Benefit{Type: 21, Value: 300, ExpiresAt: now}
			case "foreign":
				q.Owner.AccountID = "foreign"
			case "not_member":
				reg = guild.NewRegistry()
			case "reward":
				q.Reward.ItemID++
			case "token":
				q.RequestID = "body_nonce"
			}
			if err := repos.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if got, err := o.ClaimGuildContract(ctx, q, now); err == nil || got.GuildID != 0 {
				t.Fatal(got, err)
			}
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			current, _, _ := repos.Inventory.Load(ctx, "1")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bag, current) {
				t.Fatal("failed claim changed inventory or receipt")
			}
		})
	}
}

func TestGuildContractClaimConcurrency(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			repos, o, q, now := contractClaimFixture(t)
			ctx := context.Background()
			start, done := make(chan struct{}), make(chan error, 12)
			for i := range 12 {
				go func(i int) {
					req := q
					if !same {
						req.RequestID = fmt.Sprintf("%064x", i+1)
					}
					<-start
					_, err := o.ClaimGuildContract(ctx, req, now)
					done <- err
				}(i)
			}
			close(start)
			success := 0
			for range 12 {
				err := <-done
				if err == nil {
					success++
				} else if err != guild.ErrContractAlreadyClaimed {
					t.Fatal(err)
				}
			}
			want := 1
			if same {
				want = 12
			}
			if success != want {
				t.Fatal(success, want)
			}
			reg, _ := guild.Load(ctx, repos.Settings)
			bag, _, _ := repos.Inventory.Load(ctx, "1")
			if len(reg.ContractClaims) != 1 || bag.Slots["0:88"].Count != 1 || bag.Slots["0:89"].Count != 0 {
				t.Fatal("concurrent double grant")
			}
		})
	}
}

type contractUnknownCommit struct{ r.GuildUnitOfWork }

func (u contractUnknownCommit) WithinGuildInventory(ctx context.Context, owner r.GuildOwner, apply func(r.Group) error) error {
	if err := u.GuildUnitOfWork.(r.GuildInventoryUnitOfWork).WithinGuildInventory(ctx, owner, apply); err != nil {
		return err
	}
	return r.ErrGuildCommitUnknown
}

func TestGuildContractClaimUnknownCommitAndEachMember(t *testing.T) {
	repos, o, q, now := contractClaimFixture(t)
	ctx := context.Background()
	o.guild = contractUnknownCommit{repos.Guild}
	if got, err := o.ClaimGuildContract(ctx, q, now); err != r.ErrGuildCommitUnknown || got.GuildID != 0 {
		t.Fatal("unknown commit reported success", got, err)
	}
	o, _ = NewOwner(repos)
	if _, err := o.ClaimGuildContract(ctx, q, now.Add(24*time.Hour)); err != nil {
		t.Fatal("receipt reconciliation", err)
	}
	bag, _, _ := repos.Inventory.Load(ctx, "1")
	if bag.Slots["0:88"].Count != 1 || bag.Slots["0:89"].Count != 0 {
		t.Fatal("old request crossed service days and granted again")
	}
	actor := r.CharacterRecord{CharacterID: "2", AccountID: "a2", Name: "普通成员", Level: 90}
	if err := repos.Character.Save(ctx, actor); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "2", Slots: map[string]r.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	reg, _ := guild.Load(ctx, repos.Settings)
	if _, err := reg.JoinDirect(1, actor, now); err != nil {
		t.Fatal(err)
	}
	if err := guild.Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	q.Owner = r.GuildOwner{AccountID: "a2", CharacterID: "2"}
	q.RequestID = fmt.Sprintf("%064x", 2)
	if _, err := o.ClaimGuildContract(ctx, q, now); err != nil {
		t.Fatal("ordinary member unable to claim", err)
	}
	bag, _, _ = repos.Inventory.Load(ctx, "2")
	if bag.Slots["0:88"].ItemID != 490001385 {
		t.Fatal("member reward missing")
	}
}
