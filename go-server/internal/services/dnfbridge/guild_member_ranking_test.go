package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/raid"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func memberRankingWire(guildID uint32, region byte, name string) []byte {
	body := make([]byte, 5)
	binary.LittleEndian.PutUint32(body, guildID)
	body[4] = region
	return append(body, guildWireString(name)...)
}

func TestGuildMemberRankingQueryRejectsForeignGuild(t *testing.T) {
	s, p, _ := guildTextFixture(t)
	s.options.guildRankingEnabled = true
	guildDispatch(t, s, p[0], 1505, memberRankingWire(2, 0, "foreign"))
	if got := guildReply(t, p[0], 1505); !reflect.DeepEqual(got, []byte{0, 100}) {
		t.Fatalf("member query reply %x", got)
	}
}

func TestGuildMemberRankingActualIncomeQueryAndNativePackets(t *testing.T) {
	s, p, repos, q := rankingBridgeFixture(t)
	ctx := context.Background()
	rules := &guild.MemberRankingRules{RaidClears: [3]float64{1, 2, 2}, SameGuildRaidBonus: 3, MileageRate: 1, PartyClears: map[uint32]guild.RankingPoints{247: {Points: 6}}}
	s.guildRankingRules.MemberRanking = rules
	reg, _ := guild.Load(ctx, repos.Settings)
	target := reg.Guilds[1].Members["2"]
	request := memberRankingWire(1, target.ServerGroup, target.Name)
	packets := map[string]string{}
	dispatch := func(name string, want [3]float32) {
		t.Helper()
		before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
		guildDispatch(t, s, p[0], 1505, request)
		body := guildReply(t, p[0], 1505)
		if len(body) != 17 || body[0] != 1 || binary.LittleEndian.Uint32(body[1:]) != 0 {
			t.Fatalf("reply %x", body)
		}
		for i, v := range want {
			if got := math.Float32frombits(binary.LittleEndian.Uint32(body[5+4*i:])); got != v {
				t.Fatal(name, i, got, v)
			}
		}
		after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("query mutated state")
		}
		packets[name] = hex.EncodeToString(body)
	}
	dispatch("empty", [3]float32{})
	master := reg.Guilds[1].Members["1"]
	cmd := raid.WeeklyClearCommand{Mode: 2, Limit: 2, InstanceID: "00112233445566778899aabbccddeeff", CompletedAt: q.now, Members: []raid.ClearMember{{CharacterID: "1", AccountID: master.AccountID}, {CharacterID: "2", AccountID: target.AccountID}}, Policy: raid.WeeklyPolicy{ResetWeekday: time.Thursday, ResetHour: 6}}
	s.raidWeeklyIntents = map[string]*currentRaidWeeklyIntent{cmd.InstanceID: {Command: cmd}}
	completed, errors := s.commitCurrentRaidWeeklyIntents(q.now)
	if len(errors) != 0 || len(completed) != 1 {
		t.Fatal(completed, errors)
	}
	owner := r.GuildOwner{AccountID: target.AccountID, CharacterID: target.CharacterID}
	if err := repos.Guild.WithinGuild(ctx, []r.GuildOwner{owner}, func(tx r.Group) error {
		_, err := guild.GrantDungeonMileage(ctx, tx.Settings, owner, q.now)
		if err != nil {
			return err
		}
		_, err = guild.ApplyRankingPartyClear(ctx, tx.Settings, owner, guild.RankingPartyClear{DungeonID: 247, StartedAt: q.now.Add(-time.Millisecond), Members: []r.GuildOwner{owner, {AccountID: master.AccountID, CharacterID: master.CharacterID}}, Rules: s.guildRankingRules, Revalidate: func() bool { return true }}, q.now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dispatch("earned", [3]float32{5, 10, 6})
	if _, err := guild.CommitRankedRaidClear(ctx, repos, cmd); err != nil {
		t.Fatal(err)
	}
	dispatch("replayed", [3]float32{5, 10, 6})
	q.now = guild.RankingMonthStart(q.now).AddDate(0, 1, 0)
	dispatch("new_month", [3]float32{})
	if path := os.Getenv("GUILD_MEMBER_RANKING_WIRE_OUTPUT"); path != "" {
		b, _ := json.MarshalIndent(packets, "", "  ")
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if p[1].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("query broadcast peer")
	}
}

func TestGuildMemberRankingQueryIdentityAndShape(t *testing.T) {
	for _, mode := range []string{"short", "extra", "wrong_region", "other_member", "disabled", "stale", "account", "not_member", "missing_rules"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, _ := rankingBridgeFixture(t)
			ctx := context.Background()
			reg, _ := guild.Load(ctx, repos.Settings)
			m := reg.Guilds[1].Members["2"]
			body := memberRankingWire(1, m.ServerGroup, m.Name)
			switch mode {
			case "short":
				body = body[:4]
			case "extra":
				body = append(body, 0)
			case "wrong_region":
				body[4]++
			case "other_member":
				body = memberRankingWire(1, m.ServerGroup, "missing")
			case "disabled":
				s.options.guildRankingEnabled = false
			case "stale":
				s.gameSessions[1] = p[2]
			case "account":
				p[0].accountID = "foreign"
			case "not_member":
				delete(reg.Guilds, 1)
				if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			guildDispatch(t, s, p[0], 1505, body)
			got := guildReply(t, p[0], 1505)
			if len(got) != 2 || got[0] != 0 {
				t.Fatalf("invalid request accepted %x", got)
			}
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed query changed state")
			}
		})
	}
}
