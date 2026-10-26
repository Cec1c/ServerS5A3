package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/party"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type guildDungeonEntryFatigueFixture struct {
	service  *Service
	repos    dnfrepo.Group
	leaders  *gameSession
	runtimes []*runtimeDungeonState
	sessions []*gameSession
	follows  []*runtimePartyPreparedDungeonEntry
	entries  []dnfguild.DungeonEntry
	receipt  string
}

func newGuildDungeonEntryFatigueFixture(t *testing.T, count int) *guildDungeonEntryFatigueFixture {
	t.Helper()
	if count < 1 || count > 4 {
		t.Fatalf("invalid fixture member count %d", count)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	now := time.Now().UTC().Truncate(time.Second)
	var err error
	registry := dnfguild.NewRegistry()
	var guild *dnfguild.Guild
	fixture := &guildDungeonEntryFatigueFixture{repos: repos, receipt: ""}
	for index := 0; index < count; index++ {
		id := strconv.Itoa(index + 1)
		accountID := "guild-fatigue-account-" + id
		if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID}); err != nil {
			t.Fatal(err)
		}
		character := dnfrepo.CharacterRecord{
			CharacterID: id,
			AccountID:   accountID,
			Name:        "fatigue-member-" + id,
			Level:       90,
			Stats: map[string]int64{
				"server_group_id":        1,
				"fatigue":                80,
				"fatigue_limit":          156,
				dnffatigue.DayKey:        premium.ServiceDayID(now),
				"fatigue_used":           76,
				"fatigue_display_update": 76,
			},
		}
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			guild, err = registry.Create(character, "FatigueGuild", now)
		} else {
			_, err = registry.JoinDirect(guild.ID, character, now)
		}
		if err != nil {
			t.Fatal(err)
		}
		session := &gameSession{accountID: accountID, selectedCharacterID: uint16(index + 1), characterGeneration: uint64(index + 1)}
		runtime := &runtimeDungeonState{
			Dungeon: worldmap.Dungeon{
				ID: 8500,
				Metadata: worldmap.DungeonMetadata{
					IntegerValues: map[string]int64{worldmap.DungeonUseFatigueOnlyStartKey: 8},
				},
			},
			Character: character,
			AccountID: accountID,
		}
		fixture.sessions = append(fixture.sessions, session)
		fixture.runtimes = append(fixture.runtimes, runtime)
	}
	if err := dnfguild.Save(ctx, repos.Settings, registry, now); err != nil {
		t.Fatal(err)
	}
	if _, err := dnfguild.Load(ctx, repos.Settings); err != nil {
		t.Fatalf("load fixture registry: %v", err)
	}
	fixture.service = &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return fixture.repos, true }, gameSessions: make(map[uint16]*gameSession)}
	guildDungeonBridgeCatalog(t, fixture.service)
	catalog, err := fixture.service.currentGuildDungeonCatalog()
	if err != nil {
		t.Fatal(err)
	}
	owners := make([]dnfrepo.GuildOwner, len(fixture.sessions))
	for i, session := range fixture.sessions {
		owners[i] = dnfrepo.GuildOwner{AccountID: session.accountID, CharacterID: strconv.Itoa(int(session.selectedCharacterID))}
		fixture.service.gameSessions[session.selectedCharacterID] = session
	}
	fixture.entries, err = dnfguild.PrepareDungeonEntries(registry, catalog, 8500, owners, "atomic-guild-fatigue-run", now)
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range fixture.entries {
		fixture.runtimes[i].guildDungeonClear = &dnfdungeon.GuildDungeonClear{Entry: entry, Catalog: catalog}
	}
	fixture.receipt = dnfrepo.GuildDungeonEntryReceiptScope(guild.ID, 8500, fixture.entries[0].RunID)
	if count > 1 {
		manager := fixture.service.runtimePartyManagerForService()
		member := func(p *gameSession) party.RuntimePartyMember {
			return party.RuntimePartyMember{UserID: p.selectedCharacterID, SessionGeneration: p.characterGeneration, State: alignedcmd.PartyMemberState{UserID: p.selectedCharacterID}}
		}
		created := manager.Create(member(fixture.sessions[0]), alignedcmd.PartyState{MaxMembers: 4})
		if !created.OK {
			t.Fatal(created)
		}
		for _, session := range fixture.sessions[1:] {
			if got := manager.Join(created.Party.ID, member(session)); !got.OK {
				t.Fatal(got)
			}
		}
	}
	fixture.leaders = fixture.sessions[0]
	for index := 1; index < count; index++ {
		fixture.follows = append(fixture.follows, &runtimePartyPreparedDungeonEntry{
			session: fixture.sessions[index],
			runtime: fixture.runtimes[index],
			scene:   worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: int64(index), Y: 0}},
		})
	}
	return fixture
}

func (f *guildDungeonEntryFatigueFixture) commit(t *testing.T) error {
	t.Helper()
	return f.service.commitCurrentGuildDungeonEntryFatigue(
		context.Background(),
		f.leaders,
		f.runtimes[0],
		worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{}},
		f.follows,
	)
}

func (f *guildDungeonEntryFatigueFixture) storedFatigue(t *testing.T, characterID string) int64 {
	t.Helper()
	character, found, err := f.repos.Character.Load(context.Background(), characterID)
	if err != nil || !found {
		t.Fatalf("load character %s found=%t err=%v", characterID, found, err)
	}
	return character.Stats["fatigue"]
}

func (f *guildDungeonEntryFatigueFixture) receiptExists(t *testing.T) bool {
	t.Helper()
	_, found, err := f.repos.Settings.Load(context.Background(), f.receipt)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestGuildDungeonEntryFatigueChargesWholePartyAtomically(t *testing.T) {
	for count := 1; count <= 4; count++ {
		t.Run(fmt.Sprintf("%d_members", count), func(t *testing.T) {
			fixture := newGuildDungeonEntryFatigueFixture(t, count)
			if err := fixture.commit(t); err != nil {
				t.Fatal(err)
			}
			for index := 0; index < count; index++ {
				id := strconv.Itoa(index + 1)
				if got := fixture.storedFatigue(t, id); got != 72 {
					t.Fatalf("member %s fatigue=%d", id, got)
				}
				if !fixture.runtimes[index].fatigueStartDebited {
					t.Fatalf("member %s runtime was not marked debited", id)
				}
			}
			if !fixture.receiptExists(t) {
				t.Fatal("durable fatigue receipt missing")
			}
		})
	}
}

func TestGuildDungeonEntryFatigueRollsBackEveryMemberOnLateSaveFailure(t *testing.T) {
	for _, failID := range []string{"2", "4"} {
		t.Run("fail_"+failID, func(t *testing.T) {
			fixture := newGuildDungeonEntryFatigueFixture(t, 4)
			base := fixture.repos.Guild.(dnfrepo.GuildDungeonEntryUnitOfWork)
			fixture.repos.Guild = guildDungeonFailingEntryUOW{
				GuildUnitOfWork:             fixture.repos.Guild,
				GuildDungeonEntryUnitOfWork: base,
				characters: &guildDungeonFailingCharacters{
					CharacterRepository: fixture.repos.Character,
					failID:              failID,
					err:                 errors.New("injected character save failure"),
				},
			}
			if err := fixture.commit(t); err == nil {
				t.Fatal("late save failure was ignored")
			}
			for index := 0; index < 4; index++ {
				id := strconv.Itoa(index + 1)
				if got := fixture.storedFatigue(t, id); got != 80 {
					t.Fatalf("member %s retained partial debit: %d", id, got)
				}
				if fixture.runtimes[index].fatigueStartDebited {
					t.Fatalf("member %s was marked committed", id)
				}
			}
			if fixture.receiptExists(t) {
				t.Fatal("failed transaction retained receipt")
			}
		})
	}
}

func TestGuildDungeonEntryFatigueRejectsBadRosterAndStaleBalances(t *testing.T) {
	t.Run("stale_runtime_balance_uses_locked_record", func(t *testing.T) {
		fixture := newGuildDungeonEntryFatigueFixture(t, 4)
		fixture.runtimes[1].Character.Stats["fatigue"] = 0
		if err := fixture.commit(t); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 4; i++ {
			if got := fixture.storedFatigue(t, strconv.Itoa(i)); got != 72 {
				t.Fatalf("member %d fatigue=%d", i, got)
			}
		}
	})

	t.Run("locked_reread_insufficient", func(t *testing.T) {
		fixture := newGuildDungeonEntryFatigueFixture(t, 4)
		character, _, err := fixture.repos.Character.Load(context.Background(), "2")
		if err != nil {
			t.Fatal(err)
		}
		character.Stats["fatigue"] = 7
		if err := fixture.repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		if err := fixture.commit(t); !errors.Is(err, dnfdungeon.ErrEntryFatigueExhausted) {
			t.Fatalf("locked error=%v", err)
		}
		if got := fixture.storedFatigue(t, "1"); got != 80 {
			t.Fatalf("first member was written before locked recheck: %d", got)
		}
		if got := fixture.storedFatigue(t, "2"); got != 7 {
			t.Fatalf("stale member changed: %d", got)
		}
		if fixture.receiptExists(t) {
			t.Fatal("locked recheck failure wrote receipt")
		}
	})

	t.Run("stale_membership", func(t *testing.T) {
		fixture := newGuildDungeonEntryFatigueFixture(t, 4)
		registry, err := dnfguild.Load(context.Background(), fixture.repos.Settings)
		if err != nil {
			t.Fatal(err)
		}
		guild := registry.Guilds[fixture.entries[0].GuildID]
		delete(guild.Members, "3")
		if err := dnfguild.Save(context.Background(), fixture.repos.Settings, registry, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := fixture.commit(t); !errors.Is(err, dnfguild.ErrPermissionDenied) {
			t.Fatalf("stale membership error=%v", err)
		}
		for index := 0; index < 4; index++ {
			if got := fixture.storedFatigue(t, strconv.Itoa(index+1)); got != 80 {
				t.Fatalf("stale membership wrote member %d: %d", index+1, got)
			}
		}
	})

	t.Run("different_guild_or_run", func(t *testing.T) {
		for _, mutate := range []func(*runtimeDungeonState){
			func(runtime *runtimeDungeonState) { runtime.guildDungeonClear.Entry.GuildID++ },
			func(runtime *runtimeDungeonState) { runtime.guildDungeonClear.Entry.RunID += "-other" },
			func(runtime *runtimeDungeonState) {
				runtime.guildDungeonClear.Entry.JoinedAt = runtime.guildDungeonClear.Entry.JoinedAt.Add(time.Second)
			},
		} {
			fixture := newGuildDungeonEntryFatigueFixture(t, 4)
			mutate(fixture.runtimes[2])
			if err := fixture.commit(t); !errors.Is(err, dnfguild.ErrPermissionDenied) {
				t.Fatalf("forged entry error=%v", err)
			}
		}
	})
}

func TestGuildDungeonEntryFatigueSameRunReplayIsIdempotent(t *testing.T) {
	fixture := newGuildDungeonEntryFatigueFixture(t, 4)
	if err := fixture.commit(t); err != nil {
		t.Fatal(err)
	}
	for _, runtime := range fixture.runtimes {
		runtime.fatigueStartDebited = false
	}
	if err := fixture.commit(t); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		if got := fixture.storedFatigue(t, strconv.Itoa(index+1)); got != 72 {
			t.Fatalf("replay charged member %d twice: %d", index+1, got)
		}
	}
}

func TestGuildDungeonEntryFatigueRejectsChangedSessionOrParty(t *testing.T) {
	for _, change := range []string{"session", "generation", "party", "leader"} {
		t.Run(change, func(t *testing.T) {
			f := newGuildDungeonEntryFatigueFixture(t, 4)
			peer := f.sessions[2]
			switch change {
			case "session":
				f.service.gameSessions[peer.selectedCharacterID] = &gameSession{accountID: peer.accountID, selectedCharacterID: peer.selectedCharacterID, characterGeneration: peer.characterGeneration + 1}
			case "generation":
				peer.characterGeneration++
			case "party":
				f.service.runtimePartyManagerForService().Leave(peer.selectedCharacterID, peer.characterGeneration)
			case "leader":
				f.service.runtimePartyManagerForService().TransferLeader(f.leaders.selectedCharacterID, f.leaders.characterGeneration, peer.selectedCharacterID, peer.characterGeneration)
			}
			if err := f.commit(t); err == nil {
				t.Fatal("changed participant committed")
			}
			for i := 1; i <= 4; i++ {
				if got := f.storedFatigue(t, strconv.Itoa(i)); got != 80 {
					t.Fatal("changed participant charged", i, got)
				}
			}
			if f.receiptExists(t) {
				t.Fatal("changed participant wrote receipt")
			}
		})
	}
}

func TestGuildDungeonEntryFatigueRoomProjectionAfterZeroReplay(t *testing.T) {
	f := newGuildDungeonEntryFatigueFixture(t, 4)
	for i := 1; i <= 4; i++ {
		c, _, _ := f.repos.Character.Load(context.Background(), strconv.Itoa(i))
		c.Stats["fatigue"] = 8
		if err := f.repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.commit(t); err != nil {
		t.Fatal(err)
	}
	for _, runtime := range f.runtimes {
		runtime.fatigueStartDebited = false
	}
	if err := f.commit(t); err != nil {
		t.Fatal(err)
	}
	for i, runtime := range f.runtimes {
		for room := int64(0); room < 5; room++ {
			if err := f.service.debitDungeonRoomFatigue(f.sessions[i], runtime, worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: room}}); err != nil {
				t.Fatal(err)
			}
		}
		if runtime.Character.Stats["fatigue"] != 0 || f.storedFatigue(t, strconv.Itoa(i+1)) != 0 {
			t.Fatal("room projection debited replay")
		}
	}
}

func TestGuildDungeonEntryFatigueDoesNotChangeOrdinaryRoomDebit(t *testing.T) {
	fixture := newGuildDungeonEntryFatigueFixture(t, 1)
	fixture.runtimes[0].Dungeon = worldmap.Dungeon{ID: 1}
	fixture.runtimes[0].guildDungeonClear = nil
	if err := fixture.commit(t); err != nil {
		t.Fatal(err)
	}
	if fixture.receiptExists(t) {
		t.Fatal("ordinary dungeon entered guild fatigue transaction")
	}
	if got := fixture.storedFatigue(t, "1"); got != 80 {
		t.Fatalf("ordinary admission changed fatigue: %d", got)
	}
}

type guildDungeonFailingEntryUOW struct {
	dnfrepo.GuildUnitOfWork
	dnfrepo.GuildDungeonEntryUnitOfWork
	characters dnfrepo.CharacterRepository
}

func (u guildDungeonFailingEntryUOW) WithinGuildDungeonEntry(
	ctx context.Context,
	owners []dnfrepo.GuildOwner,
	receiptScope string,
	apply func(dnfrepo.Group) error,
) error {
	return u.GuildDungeonEntryUnitOfWork.WithinGuildDungeonEntry(ctx, owners, receiptScope, func(tx dnfrepo.Group) error {
		fail := *u.characters.(*guildDungeonFailingCharacters)
		fail.CharacterRepository = tx.Character
		tx.Character = &fail
		return apply(tx)
	})
}

type guildDungeonFailingCharacters struct {
	dnfrepo.CharacterRepository
	failID string
	err    error
}

func (r *guildDungeonFailingCharacters) Save(ctx context.Context, record dnfrepo.CharacterRecord) error {
	if record.CharacterID == r.failID {
		return r.err
	}
	return r.CharacterRepository.Save(ctx, record)
}
