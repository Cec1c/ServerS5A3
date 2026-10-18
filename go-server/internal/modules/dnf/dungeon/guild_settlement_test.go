package dungeon

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func guildSettlementFixture(t *testing.T) (r.Group, SettlementCommand) {
	t.Helper()
	g, cmd := luckyStarSettlementFixture(t, "0")
	cmd.GuildOrdinaryClear = true
	cmd.UpdatedAt = time.Unix(1770000000, 0).UTC()
	seedGuildSettlement(t, g, cmd)
	return g, cmd
}

func seedGuildSettlement(t *testing.T, g r.Group, cmd SettlementCommand) {
	t.Helper()
	ctx := context.Background()
	ch, _, err := g.Character.Load(ctx, cmd.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	ch.Name = "GuildClear"
	ch.Stats["server_group"] = 0
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	reg := guild.NewRegistry()
	if _, err := reg.Create(ch, "ClearGuild", cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := guild.Save(ctx, g.Settings, reg, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
}

func TestGuildSettlementMileageDurableReplayAndMembership(t *testing.T) {
	ctx := context.Background()
	g, cmd := guildSettlementFixture(t)
	o, _ := NewOwner(g)
	first, err := o.CommitSettlement(ctx, cmd)
	if err != nil || first.GuildMileage.Amount != 10 || first.Character.Level != 2 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// A second clear replaces the old character-stat receipt.
	second := cmd
	second.CompletionKey = "clear-2"
	if _, err := o.CommitSettlement(ctx, second); err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Character.Load(ctx, cmd.CharacterID)
	// Recreate the domain owner, then leave the original guild and join another.
	reg, _ := guild.Load(ctx, g.Settings)
	member := reg.Guilds[1].Members[cmd.CharacterID]
	oldMaster := member
	oldMaster.CharacterID = "old-master"
	reg.Guilds[1].MasterCharacterID = oldMaster.CharacterID
	reg.Guilds[1].Members = map[string]guild.Member{oldMaster.CharacterID: oldMaster}
	reg.Guilds[2] = &guild.Guild{ID: 2, Name: "OtherGuild", MasterCharacterID: cmd.CharacterID, Members: map[string]guild.Member{cmd.CharacterID: member}}
	if err := guild.Save(ctx, g.Settings, reg, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	o, _ = NewOwner(g)
	replay, err := o.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed || replay.GuildMileage != first.GuildMileage || !reflect.DeepEqual(replay.Character, before) {
		t.Fatalf("old replay=%+v err=%v", replay, err)
	}
	reg, _ = guild.Load(ctx, g.Settings)
	if reg.Guilds[1].Mileage != 20 || reg.Guilds[2].Mileage != 0 || member.Mileage != 0 || member.ContributionPoints != 0 || reg.Guilds[1].Experience != 0 {
		t.Fatal("replay or unrelated reward changed")
	}
	second.CompletionKey = "clear-3"
	if _, err := o.CommitSettlement(ctx, second); err != nil {
		t.Fatal(err)
	}
	reg, _ = guild.Load(ctx, g.Settings)
	if reg.Guilds[2].Mileage != 10 {
		t.Fatal("new clear did not use current membership")
	}
}

func TestGuildSettlementNonmemberCannotClaimAfterJoining(t *testing.T) {
	ctx := context.Background()
	g, cmd := luckyStarSettlementFixture(t, "0")
	cmd.GuildOrdinaryClear = true
	o, _ := NewOwner(g)
	res, err := o.CommitSettlement(ctx, cmd)
	if err != nil || res.GuildMileage.Amount != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	seedGuildSettlement(t, g, cmd)
	res, err = o.CommitSettlement(ctx, cmd)
	reg, _ := guild.Load(ctx, g.Settings)
	if err != nil || !res.Replayed || reg.Guilds[1].Mileage != 0 {
		t.Fatalf("replay=%+v err=%v", res, err)
	}
}

func TestGuildSettlementMigratesLegacyReceiptWithoutRetroactiveAward(t *testing.T) {
	ctx := context.Background()
	g, cmd := guildSettlementFixture(t)
	o, _ := NewOwner(g)
	cmd.GuildOrdinaryClear = false
	if _, err := o.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.GuildOrdinaryClear = true
	result, err := o.CommitSettlement(ctx, cmd)
	if err != nil || !result.Replayed || result.GuildMileage.Amount != 0 {
		t.Fatalf("migration=%+v err=%v", result, err)
	}
	newClear := cmd
	newClear.CompletionKey = "new-clear"
	if _, err := o.CommitSettlement(ctx, newClear); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	reg, _ := guild.Load(ctx, g.Settings)
	if reg.Guilds[1].Mileage != 10 {
		t.Fatal("legacy clear claimed retroactive points")
	}
}

func TestGuildSettlementConcurrentMembersAndDuplicateCompletions(t *testing.T) {
	ctx := context.Background()
	g, cmd := guildSettlementFixture(t)
	reg, _ := guild.Load(ctx, g.Settings)
	character, _, _ := g.Character.Load(ctx, "c")
	skill, _, _ := g.Skill.Load(ctx, "c")
	inv, _, _ := g.Inventory.Load(ctx, "c")
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("m%d", i)
		ch := r.CloneCharacter(character)
		ch.CharacterID, ch.Name, ch.Slot = id, id, i+1
		sk := r.CloneSkill(skill)
		sk.CharacterID = id
		iv := r.CloneInventory(inv)
		iv.CharacterID = id
		for _, err := range []error{g.Character.Save(ctx, ch), g.Skill.Save(ctx, sk), g.Inventory.Save(ctx, iv)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		m := reg.Guilds[1].Members["c"]
		m.CharacterID, m.Name, m.Grade = id, id, guild.DefaultMemberGrade
		reg.Guilds[1].Members[id] = m
	}
	if err := guild.Save(ctx, g.Settings, reg, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		c := cmd
		c.CharacterID = fmt.Sprintf("m%d", i%4)
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner, _ := NewOwner(g)
			_, err := owner.CommitSettlement(ctx, c)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reg, _ = guild.Load(ctx, g.Settings)
	if reg.Guilds[1].Mileage != 40 {
		t.Fatalf("mileage=%d", reg.Guilds[1].Mileage)
	}
}

var errGuildSettlementLate = errors.New("late guild settlement failure")

type guildSettlementFault struct {
	r.CharacterSettlementUnitOfWork
	guild         r.GuildSettlementUnitOfWork
	commitUnknown bool
}

func (f guildSettlementFault) WithinGuildSettlement(ctx context.Context, owner r.GuildOwner, key string, apply func(r.Group) error) error {
	err := f.guild.WithinGuildSettlement(ctx, owner, key, func(tx r.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		if !f.commitUnknown {
			return errGuildSettlementLate
		}
		return nil
	})
	if err == nil && f.commitUnknown {
		return r.ErrGuildCommitUnknown
	}
	return err
}

func TestGuildSettlementRollbackAndUnknownCommitRecovery(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			ctx := context.Background()
			g, cmd := guildSettlementFixture(t)
			original := g.CharacterSettlement
			g.CharacterSettlement = guildSettlementFault{original, original.(r.GuildSettlementUnitOfWork), unknown}
			o, _ := NewOwner(g)
			_, err := o.CommitSettlement(ctx, cmd)
			want := errGuildSettlementLate
			if unknown {
				want = r.ErrGuildCommitUnknown
			}
			if !errors.Is(err, want) {
				t.Fatalf("err=%v", err)
			}
			reg, _ := guild.Load(ctx, g.Settings)
			ch, _, _ := g.Character.Load(ctx, cmd.CharacterID)
			_, found, _ := g.Settings.Load(ctx, r.GuildSettlementReceiptScope(r.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}, cmd.CompletionKey))
			if !unknown && (reg.Guilds[1].Mileage != 0 || ch.Stats["exp"] != 90 || found) {
				t.Fatal("failed transaction retained partial reward")
			}
			g.CharacterSettlement = original
			o, _ = NewOwner(g)
			res, err := o.CommitSettlement(ctx, cmd)
			if err != nil || res.Replayed != unknown {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			reg, _ = guild.Load(ctx, g.Settings)
			if reg.Guilds[1].Mileage != 10 {
				t.Fatal("retry did not converge to one award")
			}
		})
	}
}

func TestGuildSettlementEligibilityOverflowAndOwner(t *testing.T) {
	for _, mode := range []string{"not-ordinary", "not-recommended", "overflow", "owner", "member-owner"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g, cmd := guildSettlementFixture(t)
			reg, _ := guild.Load(ctx, g.Settings)
			switch mode {
			case "not-ordinary":
				cmd.GuildOrdinaryClear = false
			case "not-recommended":
				cmd.RecommendedDungeonClear = false
			case "overflow":
				reg.Guilds[1].Mileage = ^uint32(0) - 9
			case "owner":
				cmd.AccountID = "someone-else"
			case "member-owner":
				m := reg.Guilds[1].Members["c"]
				m.AccountID = "someone-else"
				reg.Guilds[1].Members["c"] = m
			}
			if err := guild.Save(ctx, g.Settings, reg, cmd.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			before, _, _ := g.Settings.Load(ctx, guild.RegistryScope)
			o, _ := NewOwner(g)
			_, err := o.CommitSettlement(ctx, cmd)
			if (err == nil) != (mode == "not-ordinary") {
				t.Fatalf("err=%v", err)
			}
			after, _, _ := g.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected grant changed registry")
			}
			ch, _, _ := g.Character.Load(ctx, "c")
			if mode != "not-ordinary" && ch.Stats["exp"] != 90 {
				t.Fatal("failed grant still credited EXP")
			}
		})
	}
}
