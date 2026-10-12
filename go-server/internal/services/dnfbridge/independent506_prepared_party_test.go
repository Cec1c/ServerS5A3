package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Independent 474 controls (portable: compiles on both the old production
// source and the candidate).
//
// The shipped review452 follower fixture (sha 4a05e56c) persists only the
// leader. review452GroundRun builds the follower runtime from a *second*
// prepareCurrentDungeonDropTest (fresh memory repositories) and never saves
// that character/account into the repository group the freeze runs against,
// so currentRaidAuthenticated(peer) is false inside
// freezeCurrentRaidDungeonGroupEntry and the test stops at
// "raid entry prepared owner unavailable" (review452_team_ground_drops_test.go:310)
// before it can assert any product behaviour. That precondition is identical on
// the old production source and on the candidate, so it is a fixture defect and
// not a ground-drop regression. These controls persist every prepared owner into
// the same repository group and then measure the real producers.
type independent506Party struct {
	service       *Service
	leader        *gameSession
	leaderRuntime *runtimeDungeonState
	peer          *gameSession
	peerRuntime   *runtimeDungeonState
}

func independent506PrepareParty(t *testing.T, kind string, single bool, follower bool) *independent506Party {
	t.Helper()
	s, r, repositories := prepareCurrentDungeonDropTest(t, 0, 3227)
	s.options.currentClientVersion = "official-24.9"
	leader := &gameSession{conn: &bufferConn{}, accountID: "account-1", loginPacketSeen: true, accountFromToken: true}
	var state *runtimeRaidState
	var battle *runtimeRaidBattle
	switch kind {
	case "anton":
		catalog, _ := actualAntonRewardFixture(t)
		program, err := raid.LoadAntonProgram(catalog.source.(*platformpvf.Archive))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Add(-time.Minute)
		progress, err := program.BeginPhase(0, now, 506)
		if err != nil {
			t.Fatal(err)
		}
		leader.channel = channelcatalog.Channel{ID: 23, Type: 23}
		battle = &runtimeRaidBattle{
			InstanceID: "506000112233445566778899aabbccdd", Mode: 1,
			ChannelID: 23, ChannelType: 23, EnterAfter: now, Deadline: now.Add(time.Hour),
			Anton: progress, Dungeons: progress.Dungeons(),
			MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20"},
		}
		state = &runtimeRaidState{
			RaidKey: 23<<16 | 19, LeaderID: 19, Started: true, Battle: battle,
			Members: []runtimeRaidMemberState{{CharID: 19, GroupIndex: 1}, {CharID: 20, GroupIndex: 1}},
		}
		s.raids = map[uint32]*runtimeRaidState{state.RaidKey: state}
		r.Dungeon.ID = 210
	case "luke":
		f, leaderSession, _ := normalLukeFixture(t)
		leader = leaderSession
		leader.conn = &bufferConn{}
		state, battle = f.state, f.state.Battle
		s.raids = f.service.raids
		r.Dungeon.ID = 5054
	default:
		t.Fatal("unknown fixture kind")
	}
	r.Character.CharacterID, r.Character.AccountID, r.AccountID = "19", "account-1", "account-1"
	leader.connID = "ind506-" + kind + "-leader"
	if err := repositories.Character.Save(context.Background(), r.Character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	s.bindGameSessionCharacter(leader, 19)
	leader.dungeon.runtime = r
	battle.Single = single
	party := &independent506Party{service: s, leader: leader, leaderRuntime: r}
	var followers []*runtimePartyPreparedDungeonEntry
	if follower {
		_, peerRuntime, _ := prepareCurrentDungeonDropTest(t, 0, 3227)
		peerRuntime.Character.CharacterID, peerRuntime.Character.AccountID, peerRuntime.AccountID = "20", "account-20", "account-20"
		peerRuntime.Dungeon.ID = r.Dungeon.ID
		// The shipped fixture omits these two writes; without them the follower
		// cannot pass currentRaidAuthenticated and admission fails before any
		// product assertion.
		if err := repositories.Character.Save(context.Background(), peerRuntime.Character); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "20", Slots: map[string]repo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
		peer := &gameSession{conn: &bufferConn{}, connID: "ind506-" + kind + "-peer", accountID: "account-20", loginPacketSeen: true, accountFromToken: true, channel: leader.channel, dungeon: dungeonSessionState{runtime: peerRuntime}}
		s.bindGameSessionCharacter(peer, 20)
		state.Members[1].GroupIndex = state.Members[0].GroupIndex
		followers = []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: peerRuntime}}
		party.peer, party.peerRuntime = peer, peerRuntime
	}
	if err := s.freezeCurrentRaidDungeonGroupEntry(leader, r, followers); err != nil {
		t.Fatal("independent506 corrected admission fixture", err)
	}
	return party
}

// Team raid: every legally prepared participant of the frozen party must stop
// producing ground drops. On the candidate this passes; on the old production
// source the same legally prepared participants still emit drops (RED).
func TestIndependent506PreparedPartyParticipantsEmitNoGroundDrops(t *testing.T) {
	for _, kind := range []string{"anton", "luke"} {
		t.Run(kind, func(t *testing.T) {
			party := independent506PrepareParty(t, kind, false, true)
			if party.leaderRuntime.raidEntry == nil || party.peerRuntime.raidEntry == nil {
				t.Fatal("prepared runtimes lost the frozen entry")
			}
			if party.leaderRuntime.raidEntry.RunID != party.peerRuntime.raidEntry.RunID || len(party.leaderRuntime.raidEntry.Participants) != 2 {
				t.Fatal("party did not freeze one common admission")
			}
			for index, runtime := range []*runtimeDungeonState{party.leaderRuntime, party.peerRuntime} {
				monster := review452PositiveMonsterRules(t, party.service, runtime)
				beforeKey := runtime.NextObjectKey
				wires, err := party.service.planCurrentDungeonMonsterDrops(runtime, monster, currentSceneActorObjectKey(uint16(19+index)))
				if err != nil {
					t.Fatal(err)
				}
				if len(wires) != 0 || review452GroundCount(runtime) != 0 || runtime.NextObjectKey != beforeKey {
					t.Fatalf("participant %d generated ground state: wires=%d ledger=%d", index, len(wires), review452GroundCount(runtime))
				}
			}
		})
	}
}

// Single raid: the ban must not reach a one-member raid. Positive control that
// keeps its guaranteed gold/equipment ground drops on both trees.
func TestIndependent506SingleRaidKeepsGroundDrops(t *testing.T) {
	party := independent506PrepareParty(t, "anton", true, false)
	monster := review452PositiveMonsterRules(t, party.service, party.leaderRuntime)
	wires, err := party.service.planCurrentDungeonMonsterDrops(party.leaderRuntime, monster, currentSceneActorObjectKey(19))
	if err != nil {
		t.Fatal(err)
	}
	if len(wires) == 0 || review452GroundCount(party.leaderRuntime) == 0 {
		t.Fatal("single raid lost its ground drops")
	}
}
