package dnfbridge

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type rankingDeletedReadFailure struct {
	r.CharacterRepository
	id string
}

func (c rankingDeletedReadFailure) Load(ctx context.Context, id string) (r.CharacterRecord, bool, error) {
	if id == c.id {
		return r.CharacterRecord{}, false, errors.New("deleted record read failed")
	}
	return c.CharacterRepository.Load(ctx, id)
}

func TestGuildRankingDeletedMemberPublicationAndRetry(t *testing.T) {
	for _, ids := range [][]string{{"2"}, {"1"}, {"1", "2", "3"}} {
		t.Run("deleted_"+ids[0]+"_count_"+string(rune('0'+len(ids))), func(t *testing.T) {
			s, _, repos, q := rankingBridgeFixture(t)
			s.gameSessions = map[uint16]*gameSession{}
			ctx := context.Background()
			if _, err := s.refreshCurrentGuildRanking(ctx, r.GuildOwner{}); err != nil {
				t.Fatal(err)
			}
			q.now = q.now.Add(currentGuildRankingInterval)
			for _, id := range ids {
				c, _, _ := repos.Character.Load(ctx, id)
				c.Stats["delete_flag"] = 1
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			regBefore, _ := guild.Load(ctx, repos.Settings)
			realCharacters := repos.Character
			repos.Character = rankingDeletedReadFailure{realCharacters, ids[0]}
			s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			if _, err := s.refreshCurrentGuildRanking(ctx, r.GuildOwner{}); err == nil {
				t.Fatal("read failure published")
			}
			afterFailure, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, afterFailure) {
				t.Fatal("failure overwrote previous ranking")
			}
			repos.Character = realCharacters
			reg, err := s.refreshCurrentGuildRanking(ctx, r.GuildOwner{})
			if err != nil {
				t.Fatal(err)
			}
			if len(reg.Ranking.Current.Rows) != 2 || !reg.Ranking.Current.SampledAt.Equal(q.now) {
				t.Fatal("ranking not published")
			}
			if !reflect.DeepEqual(reg.Guilds, regBefore.Guilds) {
				t.Fatal("sampling changed guild membership/presence")
			}
			var countPoints float32
			for _, row := range reg.Ranking.Current.Rows {
				countPoints += row.Factors.Members
			}
			if math.Abs(float64(countPoints)-float64(3-len(ids))*0.1) > 0.000001 {
				t.Fatal("deleted membership counted", countPoints)
			}
			saved, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if _, err := s.refreshCurrentGuildRanking(ctx, r.GuildOwner{}); err != nil {
				t.Fatal(err)
			}
			again, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(saved, again) {
				t.Fatal("fresh sample republished")
			}
		})
	}
}

// Opt-in private replay: files stay outside version control. The same production
// loaders, sampler and publication transaction run against an isolated memory
// repository. No source database or player records are written.
func TestGuildRankingExtractedSamplerSnapshot(t *testing.T) {
	path, root := os.Getenv("GUILD_RANKING_SNAPSHOT"), os.Getenv("GUILD_RANKING_PVF_DIR")
	if path == "" || root == "" {
		t.Skip("requires private snapshot and extracted PVF directory")
	}
	var input struct {
		CheckedAt  time.Time           `json:"checkedAt"`
		Registry   guild.Registry      `json:"registry"`
		Characters []r.CharacterRecord `json:"characters"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	s, _, _, q := rankingBridgeFixture(t)
	q.now = input.CheckedAt
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	for _, c := range input.Characters {
		if err = repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err = guild.Save(ctx, repos.Settings, input.Registry, q.now); err != nil {
		t.Fatal(err)
	}
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	s.gameSessions = map[uint16]*gameSession{}
	source := dungeonDropCatalogTestSource{"monster/monster.lst": "", "equipment/equipment.lst": "", "stackable/stackable.lst": ""}
	for _, name := range []string{guild.RankingRulesPath, guild.DungeonCatalogPath, "etc/guild/guilddungeonreward.etc"} {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		source[name] = string(b)
		if _, err = pvf.Parse(name, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	s.pvfItemCatalog, err = newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.guildRankingRules, s.guildDungeonCatalog = nil, nil
	rules, err := s.currentGuildRankingRules()
	if err != nil {
		t.Fatal("ranking rules:", err)
	}
	catalog, err := s.currentGuildDungeonCatalog()
	if err != nil {
		t.Fatal("dungeon catalog:", err)
	}
	facts, err := guild.SampleRankingMembers(ctx, repos.Character, input.Registry, s.adventureGroupTable, defaultCharacterSlots, nil, nil)
	if err != nil {
		t.Fatal("member sampling:", err)
	}
	var eligible, deleted int
	for _, f := range facts {
		if f.Character.Stats["delete_flag"] != 0 {
			deleted++
		} else {
			eligible++
		}
	}
	if _, err = guild.CalculateRankingPeriod(input.Registry, facts, rules, catalog, q.now); err != nil {
		t.Fatal("calculation:", err)
	}
	reg, err := s.refreshCurrentGuildRanking(ctx, r.GuildOwner{})
	if err != nil {
		t.Fatal("publication:", err)
	}
	if reg.Ranking == nil || reg.Ranking.Current == nil || len(reg.Ranking.Current.Rows) != len(input.Registry.Guilds) || !reg.Ranking.Current.SampledAt.Equal(q.now) {
		t.Fatal("incomplete ranking")
	}
	if !reflect.DeepEqual(reg.Guilds, input.Registry.Guilds) {
		t.Fatal("replay changed guild data")
	}
	for _, c := range input.Characters {
		after, found, err := repos.Character.Load(ctx, c.CharacterID)
		if err != nil || !found || !reflect.DeepEqual(c, after) {
			t.Fatal("replay changed character data")
		}
	}
	// Compare all member categories with a copy containing only proven active
	// memberships. This reference never goes into persistence.
	for id, g := range input.Registry.Guilds {
		active := *g
		active.Members = make(map[string]guild.Member)
		for cid, m := range g.Members {
			if facts[cid].Character.Stats["delete_flag"] == 0 {
				active.Members[cid] = m
			}
		}
		want, err := guild.RankingMemberFactors(&active, facts, rules, q.now)
		if err != nil {
			t.Fatal(err)
		}
		got := reg.Ranking.Current.Rows[id].Factors
		if got.Members != want.Members || got.WeeklyActive != want.WeeklyActive || got.Adventure != want.Adventure || got.PVP != want.PVP || got.RaidClear != want.RaidClear {
			t.Fatal("deleted member affected scoring")
		}
	}
	t.Logf("guilds=%d memberships=%d eligible=%d deleted=%d characters=%d rows=%d sampledAt=%s; adventure table=test fixture; online=none", len(input.Registry.Guilds), len(facts), eligible, deleted, len(input.Characters), len(reg.Ranking.Current.Rows), reg.Ranking.Current.SampledAt.Format(time.RFC3339))
	if output := os.Getenv("GUILD_RANKING_REPLAY_OUTPUT"); output != "" {
		b, err := json.MarshalIndent(reg.Ranking, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
