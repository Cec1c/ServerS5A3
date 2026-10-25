package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/party"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func rankingBridgeFixture(t *testing.T) (*Service, []*gameSession, r.Group, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	s, peers, repos := guildTextFixture(t)
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = s.gameplayNow().Add(time.Second)
	s.gameplayTimers = q
	s.options.guildRankingEnabled = true
	s.adventureGroupTable = loadAdventureGroupTestTables(t)
	s.guildRankingRules = &guild.RankingRules{Members: guild.RankingRate{Maximum: 100, Rate: 0.1}, WeeklyActive: guild.RankingRate{Maximum: 90, Rate: 0.1}, Dungeon: guild.RankingRate{Maximum: 120, Rate: 0.1}, Contents: guild.RankingRate{Maximum: 60, Rate: 10}, MinimumConquestPoint: 10, Conquests: []guild.RankingConquest{{First: 8500, Second: 8500, Points: 20}}, Adventure: []guild.RankingRange{{Min: 1, Max: 30, Points: 3}}, PVP: []guild.RankingRange{{Min: 0, Max: 21, Points: 1}}, RaidClears: map[string]guild.RankingPoints{"anton": {Points: 1}, "luke": {Points: 2}, "fiendwar": {Points: 2}}, PartyClears: map[uint32]guild.RankingPoints{247: {Points: 5}}}
	c, err := guild.ParseDungeonCatalog("[dungeon]\n[dungeon index] 8500\n[gauge limit] 100\n[gauge on clear] 1\n[conquest gauge ratio] 50\n[reward]\n[gauge ratio] 100\n[item] 10154439 3\n[contribute] 20\n[/gauge ratio]\n[/reward]\n[/dungeon]")
	if err != nil {
		t.Fatal(err)
	}
	s.guildDungeonCatalog = c
	return s, peers, repos, q
}

func TestGuildRankingQueryRoutesPersistsAndRetainsActualMonths(t *testing.T) {
	s, peers, repos, q := rankingBridgeFixture(t)
	ctx := context.Background()
	beforeCharacter, _, _ := repos.Character.Load(ctx, "1")
	guildDispatch(t, s, peers[0], 1503, []byte{1, 0})
	body := guildReply(t, peers[0], 1503)
	if len(body) != 109 || body[0] != 1 || binary.LittleEndian.Uint32(body[5:]) != 1 {
		t.Fatalf("ranking reply=%x", body)
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	rows := reg.Ranking.View(1, q.now)
	if rows[0].Factors.Members != 0.2 || rows[0].Factors.WeeklyActive <= 0 || rows[0].Factors.WeeklyActive > 0.2 || rows[1] != (guild.RankingRow{}) || rows[2] != (guild.RankingRow{}) {
		t.Fatal(rows)
	}
	if reg.Ranking.View(2, q.now)[0].Rank != 2 {
		t.Fatal("all guilds were not ranked together")
	}
	if path := os.Getenv("GUILD_RANKING_HANDLER_WIRE_OUTPUT"); path != "" {
		b, err := json.MarshalIndent(struct {
			BodyHex string
			Rows    [3]guild.RankingRow
		}{hex.EncodeToString(body[1:]), rows}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	guildDispatch(t, s, peers[0], 1503, []byte{1, 0})
	after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("fresh query recomputed or wrote ranking")
	}
	q.now = guild.RankingMonthStart(q.now).AddDate(0, 1, 0)
	guildDispatch(t, s, peers[0], 1503, []byte{1, 0})
	reg, _ = guild.Load(ctx, repos.Settings)
	if reg.Ranking.View(1, q.now)[1] != rows[0] {
		t.Fatal("monthly result lost")
	}
	// Rebuild the bridge cache on the same repository and reread history.
	s.guildRankingRules = nil
	guildDispatch(t, s, peers[0], 1503, []byte{1, 0})
	if b := guildReply(t, peers[0], 1503); len(b) != 109 || b[0] != 1 {
		t.Fatal("persisted snapshot required catalog reload")
	}
	afterCharacter, _, _ := repos.Character.Load(ctx, "1")
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) {
		t.Fatal("ranking query wrote character assets")
	}
	if peers[1].conn.(*bufferConn).write.Len() != 0 || peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("query broadcast another guild result")
	}
}

func TestGuildRankingQueryRejectsIdentityAndSamplingFailures(t *testing.T) {
	for _, mode := range []string{"disabled", "short", "extra", "other_character", "stale_session", "wrong_account", "not_member", "missing_roster", "missing_table", "source_changed", "save_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := rankingBridgeFixture(t)
			ctx := context.Background()
			body := []byte{1, 0}
			switch mode {
			case "disabled":
				s.options.guildRankingEnabled = false
			case "short":
				body = body[:1]
			case "extra":
				body = append(body, 0)
			case "other_character":
				body = []byte{3, 0}
			case "stale_session":
				s.gameSessions[1] = peers[2]
			case "wrong_account":
				peers[0].accountID = "foreign"
			case "not_member":
				reg, _ := guild.Load(ctx, repos.Settings)
				delete(reg.Guilds, 1)
				if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "missing_roster":
				repos.Character = rankingFailRoster{repos.Character}
			case "missing_table":
				s.adventureGroupTable = nil
			case "source_changed":
				repos.Guild = rankingFailPublication{base: repos.Guild, err: guild.ErrRankingSnapshotChanged}
			case "save_failure":
				repos.Guild = rankingFailPublication{base: repos.Guild, err: errors.New("late publication failure")}
			}
			s.repositoryProvider = func() (r.Group, bool) { return repos, true }
			before, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			guildDispatch(t, s, peers[0], 1503, body)
			if reply := guildReply(t, peers[0], 1503); len(reply) == 109 || len(reply) == 0 || reply[0] != 0 {
				t.Fatalf("disclosed rank: %x", reply)
			}
			after, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed query overwrote snapshot")
			}
		})
	}
}

type rankingFailRoster struct{ r.CharacterRepository }

func (rankingFailRoster) ListByAccount(context.Context, string, int) ([]r.CharacterRecord, error) {
	return nil, errors.New("roster read failed")
}

type rankingFailPublication struct {
	base r.GuildUnitOfWork
	err  error
}

func (f rankingFailPublication) WithinGuild(ctx context.Context, owners []r.GuildOwner, fn func(r.Group) error) error {
	return f.base.WithinGuild(ctx, owners, func(tx r.Group) error {
		if err := fn(tx); err != nil {
			return err
		}
		return f.err
	})
}

func rankingPartyFixture(t *testing.T) (*Service, []*gameSession, *runtimeDungeonState) {
	t.Helper()
	s, peers, _, q := rankingBridgeFixture(t)
	manager := s.runtimePartyManagerForService()
	member := func(p *gameSession) party.RuntimePartyMember {
		return party.RuntimePartyMember{UserID: p.selectedCharacterID, SessionGeneration: p.characterGeneration, State: alignedcmd.PartyMemberState{UserID: p.selectedCharacterID}}
	}
	created := manager.Create(member(peers[0]), alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK || !manager.Join(created.Party.ID, member(peers[1])).OK {
		t.Fatal("party fixture")
	}
	instance := &dungeonPartyInstance{key: dungeonPartyRunKey{PartyID: created.Party.ID}, sessions: map[uint16]*gameSession{1: peers[0], 2: peers[1]}, scenes: map[uint16]*worldmap.DungeonSession{1: {}, 2: {}}}
	runtime := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 247}, startedAt: q.now, partyInstance: instance}
	return s, peers, runtime
}

func TestGuildRankingClearFreezesCanonicalRunAndRejectsReplacement(t *testing.T) {
	s, peers, runtime := rankingPartyFixture(t)
	if clear, err := s.currentGuildRankingClear(peers[0], runtime); err != nil || clear != nil {
		t.Fatal("uncleared instance received points", clear, err)
	}
	if err := s.freezeCurrentGuildRankingRun(peers[0], runtime); err != nil {
		t.Fatal(err)
	}
	clear, err := s.currentGuildRankingClear(peers[0], runtime)
	if err != nil || clear == nil || len(clear.Members) != 2 || !clear.Revalidate() {
		t.Fatal(clear, err)
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	delete(instance.sessions, 2)
	delete(instance.scenes, 2)
	instance.mu.Unlock()
	s.runtimePartyManagerForService().Leave(2, peers[1].characterGeneration)
	if !clear.Revalidate() {
		t.Fatal("teammate town return erased completed party facts")
	}
	if again, err := s.currentGuildRankingClear(peers[0], runtime); err != nil || again == nil || len(again.Members) != 2 {
		t.Fatal(again, err)
	}
	s.mu.Lock()
	s.gameSessions[1] = &gameSession{selectedCharacterID: 1, characterGeneration: 99}
	s.mu.Unlock()
	if clear.Revalidate() {
		t.Fatal("replacement actor still qualifies")
	}
	if _, err := s.currentGuildRankingClear(peers[0], runtime); err == nil {
		t.Fatal("stale actor accepted")
	}
}

func TestGuildRankingTerminalRequiresAuthoritativeParty(t *testing.T) {
	for _, mode := range []string{"left_before_clear", "wrong_party", "stale_generation", "missing_scene"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, runtime := rankingPartyFixture(t)
			switch mode {
			case "left_before_clear":
				s.runtimePartyManagerForService().Leave(2, peers[1].characterGeneration)
			case "wrong_party":
				runtime.partyInstance.key.PartyID++
			case "stale_generation":
				s.mu.Lock()
				s.gameSessions[2] = &gameSession{selectedCharacterID: 2, characterGeneration: 99}
				s.mu.Unlock()
			case "missing_scene":
				delete(runtime.partyInstance.scenes, 2)
			}
			if err := s.freezeCurrentGuildRankingRun(peers[0], runtime); err != nil {
				t.Fatal(err)
			}
			if clear, err := s.currentGuildRankingClear(peers[0], runtime); err != nil || clear != nil {
				t.Fatal("invalid party scored", clear, err)
			}
			// Adding a scene or returning to the party after terminal cannot earn points.
			runtime.partyInstance.scenes[2] = &worldmap.DungeonSession{}
			s.runtimePartyManagerForService().Join(runtime.partyInstance.key.PartyID, party.RuntimePartyMember{UserID: 2, SessionGeneration: peers[1].characterGeneration})
			if err := s.freezeCurrentGuildRankingRun(peers[0], runtime); err != nil {
				t.Fatal(err)
			}
			if clear, _ := s.currentGuildRankingClear(peers[0], runtime); clear != nil {
				t.Fatal("late join rewrote terminal facts")
			}
		})
	}
}

func TestGuildRankingSamplerStopsWithServiceContext(t *testing.T) {
	s, _, _, _ := rankingBridgeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.startCurrentGuildRankingSampler(ctx)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sampler survived shutdown")
	}
}
