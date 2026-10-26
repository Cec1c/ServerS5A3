package guild

import (
	"context"
	"errors"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func guardianGrowthText(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("GUILD_ESSENCE_EXTRACTED_ETC"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	text := "[guardian exp]\n"
	for level := 1; level < 50; level++ {
		gp := 0
		if level >= 20 {
			gp = 604
		}
		text += fmt.Sprintf("%d %d %d ", level, level*18, gp)
	}
	return text + "\n[/guardian exp]\n[guardian skill]\n1 1500 0 2 15000 200 3 50000 1000 4 100000 5000\n[/guardian skill]"
}
func guardianGrowthFixture(t *testing.T) *GuardianGrowthCatalog {
	t.Helper()
	c, err := ParseGuardianGrowthCatalog(guardianGrowthText(t))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestGuardianGrowthLevelAndSkillBoundaries(t *testing.T) {
	c := guardianGrowthFixture(t)
	for _, gpFirst := range []bool{false, true} {
		row := Guardian{ID: 1, Level: 1, Skills: [5]uint8{1, 1, 1, 1, 1}}
		for level := uint8(1); level < 50; level++ {
			required := c.levels[level]
			actions := []uint32{0, 1}
			if gpFirst {
				actions = []uint32{1, 0}
			}
			for _, action := range actions {
				amount := required.AP
				if action == 1 {
					amount = required.GP
				}
				if amount == 0 {
					continue
				}
				first := amount / 2
				if first > 0 {
					next, cost, err := c.growth(row, action, 0, first)
					if err != nil || next.Level != level || cost.AP+cost.GP != first {
						t.Fatal(level, action, next, cost, err)
					}
					row = next
				}
				next, cost, err := c.growth(row, action, first, amount-first)
				if err != nil || cost.AP+cost.GP != amount-first {
					t.Fatal(level, action, next, cost, err)
				}
				row = next
			}
			if row.Level != level+1 || row.APProgress != 0 || row.GPProgress != 0 {
				t.Fatal("level completion", level, row)
			}
		}
		if _, _, err := c.growth(row, 0, 0, 1); err == nil {
			t.Fatal("level50 accepted investment")
		}
	}
	for slot := uint32(0); slot < 5; slot++ {
		row := Guardian{ID: 1, Level: uint8(slot*10 + 1), Skills: [5]uint8{1, 1, 1, 1, 1}}
		if slot > 0 {
			locked := row
			locked.Level--
			if _, _, err := c.growth(locked, slot+2, 1, 2); err == nil {
				t.Fatal("locked skill", slot)
			}
		}
		for level := uint32(1); level < 5; level++ {
			next, cost, err := c.growth(row, slot+2, level, level+1)
			if err != nil || next.Skills[slot] != uint8(level+1) || cost != c.skills[level] {
				t.Fatal(slot, level, next, cost, err)
			}
			row = next
		}
		if _, _, err := c.growth(row, slot+2, 5, 6); err == nil {
			t.Fatal("max skill")
		}
	}
	row := Guardian{ID: 1, Level: 1, Skills: [5]uint8{1, 1, 1, 1, 1}}
	for _, args := range [][3]uint32{{0, 1, 1}, {0, 0, 19}, {0, 0, 0}, {0, 0, 0xffffffff}, {1, 0, 1}, {2, 0, 2}, {2, 1, 3}, {3, 1, 2}, {9, 0, 1}} {
		if _, _, err := c.growth(row, args[0], args[1], args[2]); err == nil {
			t.Fatal("invalid growth", args)
		}
	}
	text := guardianGrowthText(t)
	for _, bad := range []string{"", strings.Replace(text, "1 18 0", "1 -18 0", 1), text + "\n[guardian skill]\n1 1 0", strings.Replace(text, "2 15000 200", "1 15000 200", 1)} {
		if _, err := ParseGuardianGrowthCatalog(bad); err == nil {
			t.Fatal("invalid growth table")
		}
	}
}

func guardianGrowthTransactions(t *testing.T, repos r.Group) {
	t.Helper()
	ctx := context.Background()
	growth := guardianGrowthFixture(t)
	for _, mode := range []string{"success", "stale", "over", "locked", "insufficient", "permission", "late", "after_save"} {
		t.Run(mode, func(t *testing.T) {
			q, cat, now := guardianTransactionSeed(t, repos)
			if _, err := ApplyGuardian(ctx, repos.Guild, q, cat, now); err != nil {
				t.Fatal(err)
			}
			q.Growth = growth
			q.Action = 0
			q.Value = 18
			q.RequestID = fmt.Sprintf("%064x", 2)
			reg, err := Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			uow := repos.Guild
			switch mode {
			case "stale":
				q.Observed = 1
			case "over":
				q.Value = 19
			case "locked":
				q.Action = 3
				q.Observed = 1
				q.Value = 2
			case "insufficient":
				reg.Guilds[1].Essence.AP = 17
			case "permission":
				reg.Guilds[1].GradePermissions = map[byte]uint32{DefaultMasterGrade: 0}
			case "late":
				uow = lateFailGuildTx{uow, errors.New("growth late commit failure")}
			case "after_save":
				calls := 0
				q.Revalidate = func() bool { calls++; return calls < 3 }
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, RegistryScope)
			receipt, err := ApplyGuardian(ctx, uow, q, cat, now)
			if mode != "success" {
				if err == nil || receipt.GuildID != 0 {
					t.Fatal("invalid growth accepted", receipt, err)
				}
				after, _, _ := repos.Settings.Load(ctx, RegistryScope)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("failed growth changed persistent state")
				}
				return
			}
			if err != nil || receipt.Guardian.Level != 2 || receipt.BalanceAfter.AP != 99982 {
				t.Fatal(receipt, err)
			}
			replay, err := ApplyGuardian(ctx, repos.Guild, q, cat, now.Add(time.Hour))
			if err != nil || !reflect.DeepEqual(replay, receipt) {
				t.Fatal("durable recovery", replay, err)
			}
			q.Value = 17
			if _, err := ApplyGuardian(ctx, repos.Guild, q, cat, now); !errors.Is(err, ErrPurchaseIdentity) {
				t.Fatal("receipt body conflict", err)
			}
			// A later skill upgrade must survive recovery of the older investment.
			q.Action = 2
			q.Observed = 1
			q.Value = 2
			q.RequestID = fmt.Sprintf("%064x", 3)
			skill, err := ApplyGuardian(ctx, repos.Guild, q, cat, now)
			if err != nil || skill.Guardian.Skills[0] != 2 || skill.BalanceAfter.AP != 98482 {
				t.Fatal(skill, err)
			}
			q.Action = 0
			q.Observed = 0
			q.Value = 18
			q.RequestID = fmt.Sprintf("%064x", 2)
			if _, err := ApplyGuardian(ctx, repos.Guild, q, cat, now); err != nil {
				t.Fatal(err)
			}
			current, err := Load(ctx, repos.Settings)
			if err != nil || current.Guilds[1].Guardians.Owned[0].Skills[0] != 2 || current.Guilds[1].Essence.AP != 98482 {
				t.Fatal("old replay overwrote later state", err)
			}
		})
	}
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("concurrent_same_%t", same), func(t *testing.T) {
			q, cat, now := guardianTransactionSeed(t, repos)
			if _, err := ApplyGuardian(ctx, repos.Guild, q, cat, now); err != nil {
				t.Fatal(err)
			}
			q.Growth = growth
			q.Action = 2
			q.Observed = 1
			q.Value = 2
			var wg sync.WaitGroup
			results := make(chan error, 12)
			for i := range 12 {
				wg.Go(func() {
					req := q
					key := i + 2
					if same {
						key = 2
					}
					req.RequestID = fmt.Sprintf("%064x", key)
					_, err := ApplyGuardian(ctx, repos.Guild, req, cat, now)
					results <- err
				})
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				}
			}
			want := 1
			if same {
				want = 12
			}
			if success != want {
				t.Fatal("concurrent outcomes", success, want)
			}
			reg, err := Load(ctx, repos.Settings)
			if err != nil || reg.Guilds[1].Essence.AP != 98500 || reg.Guilds[1].Guardians.Owned[0].Skills[0] != 2 || len(reg.GuardianReceipts) != 2 {
				t.Fatal("concurrent debit", err)
			}
		})
	}
}
func TestGuardianGrowthTransactions(t *testing.T) {
	guardianGrowthTransactions(t, guildTestGroup(t, 2))
}
