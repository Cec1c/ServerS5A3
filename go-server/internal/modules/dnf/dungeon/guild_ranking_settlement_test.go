package dungeon

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func rankingSettlementFixture(t *testing.T) (r.Group, SettlementCommand) {
	t.Helper()
	group, cmd := guildSettlementFixture(t)
	ctx := context.Background()
	reg, err := guild.Load(ctx, group.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.Guilds[1]
	peer := g.Members[cmd.CharacterID]
	peer.CharacterID, peer.Name, peer.Grade = "ranking-peer", "RankingPeer", guild.DefaultMemberGrade
	g.Members[peer.CharacterID] = peer
	if err := guild.Save(ctx, group.Settings, reg, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	cmd.GuildRanking = &guild.RankingPartyClear{DungeonID: 247, StartedAt: cmd.UpdatedAt, Members: []r.GuildOwner{{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}, {AccountID: peer.AccountID, CharacterID: peer.CharacterID}}, Rules: &guild.RankingRules{PartyClears: map[uint32]guild.RankingPoints{247: {Points: 5, NewbiePoints: 7.5}}}, Revalidate: func() bool { return true }}
	cmd.UpdatedAt = cmd.UpdatedAt.Add(time.Minute)
	return group, cmd
}

func TestGuildRankingSettlementPermanentReplayAndConcurrency(t *testing.T) {
	ctx := context.Background()
	group, cmd := rankingSettlementFixture(t)
	owner, _ := NewOwner(group)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := owner.CommitSettlement(ctx, cmd); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reg, _ := guild.Load(ctx, group.Settings)
	if reg.Guilds[1].RankingActivity.PartyClear != 5 || reg.Guilds[1].Mileage != 10 {
		t.Fatal("duplicate award", reg.Guilds[1])
	}
	second := cmd
	second.CompletionKey = "ranking-second"
	if _, err := owner.CommitSettlement(ctx, second); err != nil {
		t.Fatal(err)
	}
	before, _, _ := group.Character.Load(ctx, cmd.CharacterID)
	// After another completion replaced the short character receipt, the old
	// permanent receipt still wins, even when the old party is no longer valid.
	owner, _ = NewOwner(group)
	cmd.GuildRanking.Revalidate = func() bool { return false }
	got, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || !got.Replayed || got.GuildRanking.Points != 5 || !reflect.DeepEqual(got.Character, before) {
		t.Fatal(got, err)
	}
	reg, _ = guild.Load(ctx, group.Settings)
	if reg.Guilds[1].RankingActivity.PartyClear != 10 {
		t.Fatal("replay changed points")
	}
}

func TestGuildRankingSettlementEligibilityAndLateRollback(t *testing.T) {
	for _, mode := range []string{"foreign_peer", "joined_after_entry", "missing_actor", "duplicate_peer", "expired_session", "late_revalidation", "late_save"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			group, cmd := rankingSettlementFixture(t)
			before, _, _ := group.Character.Load(ctx, cmd.CharacterID)
			reg, _ := guild.Load(ctx, group.Settings)
			wantErr := true
			switch mode {
			case "foreign_peer":
				cmd.GuildRanking.Members[1].AccountID = "foreign"
				wantErr = false
			case "joined_after_entry":
				m := reg.Guilds[1].Members["ranking-peer"]
				m.JoinedAt = cmd.UpdatedAt
				reg.Guilds[1].Members["ranking-peer"] = m
				wantErr = false
			case "missing_actor":
				cmd.GuildRanking.Members[0].CharacterID = "absent"
			case "duplicate_peer":
				cmd.GuildRanking.Members[1] = cmd.GuildRanking.Members[0]
			case "expired_session":
				cmd.GuildRanking.Revalidate = func() bool { return false }
			case "late_revalidation":
				calls := 0
				cmd.GuildRanking.Revalidate = func() bool { calls++; return calls == 1 }
			}
			if err := guild.Save(ctx, group.Settings, reg, cmd.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			originalRegistry, _, _ := group.Settings.Load(ctx, guild.RegistryScope)
			if mode == "late_save" {
				group.CharacterSettlement = rankingLateFailSettlement{group.CharacterSettlement.(r.GuildSettlementUnitOfWork)}
			}
			owner, _ := NewOwner(group)
			got, err := owner.CommitSettlement(ctx, cmd)
			if (err != nil) != wantErr {
				t.Fatal(got, err)
			}
			reg, _ = guild.Load(ctx, group.Settings)
			if reg.Guilds[1].RankingActivity != nil {
				t.Fatal("ineligible clear received points")
			}
			if wantErr {
				after, _, _ := group.Character.Load(ctx, cmd.CharacterID)
				afterRegistry, _, _ := group.Settings.Load(ctx, guild.RegistryScope)
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(originalRegistry, afterRegistry) {
					t.Fatal("failed ranking settlement committed rewards")
				}
			}
		})
	}
}

type rankingLateFailSettlement struct{ r.GuildSettlementUnitOfWork }

func (s rankingLateFailSettlement) WithinCharacterSettlement(context.Context, string, func(r.Group) error) error {
	return errors.New("wrong transaction")
}
func (s rankingLateFailSettlement) WithinGuildSettlement(ctx context.Context, o r.GuildOwner, key string, fn func(r.Group) error) error {
	return s.GuildSettlementUnitOfWork.WithinGuildSettlement(ctx, o, key, func(tx r.Group) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("late persistence failure")
	})
}
