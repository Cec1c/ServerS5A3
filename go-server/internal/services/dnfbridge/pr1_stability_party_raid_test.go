package dnfbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type pr1FailingWriteConn struct {
	bufferConn
	err error
}

func (c *pr1FailingWriteConn) Write([]byte) (int, error) {
	return 0, c.err
}

func (c *pr1FailingWriteConn) SetWriteDeadline(time.Time) error { return nil }

type pr1MemorySource map[string]string

func (m pr1MemorySource) ReadText(path string) (string, error) {
	v, ok := m[path]
	if !ok {
		return "", errors.New("not found: " + path)
	}
	return v, nil
}

func TestPR1TownPasserbyNotUpgradedAndPeerWriteErrorIsolated(t *testing.T) {
	t.Setenv(townDisplayOnlyEnv, "on")
	_, s, observer, newcomer, observerConn, _ := newTownCoPresenceTestService(t)
	s.options.currentClientVersion = "official-24.9"

	// observer is an unrelated town passerby; newcomer + peer3 form party 71.
	peer3Conn := &bufferConn{}
	peer3 := &gameSession{
		conn: peer3Conn, channel: newcomer.channel, accountID: "account-pr1-peer3",
		selectedCharacterID: 3003, loginPacketSeen: true, accountFromToken: true,
		townActorOwnerChannel: newcomer.townActorOwnerChannel, connectionTownActorOwnerChannel: newcomer.connectionTownActorOwnerChannel,
	}
	s.bindGameSessionCharacter(peer3, 3003)

	newcomerIdentity, _ := s.boundGameSessionCharacterSnapshot(newcomer)
	observer.rememberTownDisplayPeer(newcomerIdentity)
	observerConn.write.Reset()

	partyState := alignedcmd.PartyState{
		PartyID: 71,
		UserID:  newcomer.selectedCharacterID,
		Members: []alignedcmd.PartyMemberState{
			{UserID: newcomer.selectedCharacterID, Slot: 0, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: peer3.selectedCharacterID, Slot: 1, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}

	// 1. Passerby must NOT trigger town_display_only_party_upgrade.
	if err := s.upgradeTownDisplayPeersForParty(observer, partyState); err != nil {
		t.Fatalf("upgradeTownDisplayPeersForParty(passerby) err=%v", err)
	}
	if observerConn.write.Len() != 0 {
		t.Fatalf("passerby unexpectedly received %d bytes from town_display_only_party_upgrade", observerConn.write.Len())
	}

	// 2. Dead passerby socket in same town area must NOT return error to initiator.
	deadConn := &pr1FailingWriteConn{err: context.Canceled}
	deadPasserby := &gameSession{
		conn: deadConn, channel: newcomer.channel, accountID: "account-pr1-dead",
		selectedCharacterID: 3379, loginPacketSeen: true, accountFromToken: true,
		townActorOwnerChannel: newcomer.townActorOwnerChannel, connectionTownActorOwnerChannel: newcomer.connectionTownActorOwnerChannel,
	}
	s.bindGameSessionCharacter(deadPasserby, 3379)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 3379,
		Session:     deadPasserby,
		ChannelID:   newcomer.channel.ID,
		TownID:      31,
		AreaID:      2,
	})
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: newcomer.selectedCharacterID,
		Session:     newcomer,
		ChannelID:   newcomer.channel.ID,
		TownID:      31,
		AreaID:      2,
	})

	if err := s.broadcastCurrentPartyActorProjection(newcomer, partyState, "pr1_test"); err != nil {
		t.Fatalf("dead town passerby leaked write error to initiator: %v", err)
	}

	// 3. Managed party snapshots isolate failing non-initiator peer while keeping healthy initiator clean.
	failingPeerConn := &pr1FailingWriteConn{err: io.ErrClosedPipe}
	failingPeer := &gameSession{
		conn: failingPeerConn, channel: newcomer.channel, accountID: "account-pr1-failing",
		selectedCharacterID: 4401, loginPacketSeen: true, accountFromToken: true,
		townActorOwnerChannel: newcomer.townActorOwnerChannel, connectionTownActorOwnerChannel: newcomer.connectionTownActorOwnerChannel,
	}
	s.bindGameSessionCharacter(failingPeer, 4401)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 4401,
		Session:     failingPeer,
		ChannelID:   newcomer.channel.ID,
		TownID:      31,
		AreaID:      2,
	})

	state, _, joined := s.createOrJoinManagedRuntimeParty(failingPeer, newcomer)
	if !joined {
		t.Fatal("failed to create managed party for snapshot isolation test")
	}
	if err := s.sendManagedRuntimePartySnapshotsForInitiator(newcomer, state); err != nil {
		t.Fatalf("non-initiator peer snapshot write failure leaked to healthy initiator: %v", err)
	}

	// 4. Follower enter-select sync failure must NOT disconnect leader.
	canceledCtx, cancelFollower := context.WithCancel(context.Background())
	cancelFollower()
	failingPeer.ctx = canceledCtx
	if err := s.synchronizeRuntimePartyMembersEnterSelect(newcomer, "pr1_test"); err != nil {
		t.Fatalf("follower enter-select failure leaked to leader: %v", err)
	}
}

func newPR1DungeonSession(t *testing.T, topology *worldmap.DungeonTopology) (*worldmap.DungeonSession, worldmap.DungeonRoomScene) {
	t.Helper()
	run, err := worldmap.NewDungeonRun(topology)
	if err != nil {
		t.Fatalf("NewDungeonRun: %v", err)
	}
	ds, err := worldmap.NewDungeonSession(run)
	if err != nil {
		t.Fatalf("NewDungeonSession: %v", err)
	}
	scene, ok := ds.Scene()
	if !ok {
		t.Fatal("dungeon session missing initial scene")
	}
	return ds, scene
}

func TestPR1PartyDungeonPreDefeatedBindAndMoveReconcile(t *testing.T) {
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[size]\n1 1", "[size]\n2 1", 1)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[greed]\n`A`", "[greed]\n`AA`", 1)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "`map` 0 0 100", "`map` 0 0 100 `boss` 1 0 101", 1)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[boss map]\n0 0", "[boss map]\n1 0", 1)
	source[worldmap.DefaultMapList] += "101 `dungeon/test/next.map`\n"
	source["map/dungeon/test/next.map"] = "[map name]\n`next`\n[dungeon]\n700\n[type]\n`[boss]`\n[monster]\n"
	_, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	topology, err := worldmap.BuildDungeonLayout(resolver, 700, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
		return choice.Candidates[0].ID, nil
	})
	if err != nil {
		t.Fatalf("build synthetic layout: %v", err)
	}
	followerDS, scene := newPR1DungeonSession(t, topology)
	leaderDS, _ := newPR1DungeonSession(t, topology)

	service, leader, follower, leaderRuntime, followerRuntime, _ := partyDungeonInstanceFixture(t, 7, 4242)
	service.dungeonMonsterTable = monsters
	sharedRoom, nextKey, err := newRuntimeDungeonRoom(scene, monsters, firstDungeonMonsterObjectKey)
	if err != nil {
		t.Fatalf("newRuntimeDungeonRoom: %v", err)
	}
	leaderRuntime.Session = leaderDS
	leaderRuntime.Room = sharedRoom
	leaderRuntime.NextObjectKey = nextKey
	leaderRuntime.Rooms = map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit{
		runtimeDungeonRoomKeyFromScene(scene): {Scene: scene, Room: sharedRoom, Seed: 4242, DropRNG: 4242},
	}
	followerRuntime.Session = followerDS
	followerRuntime.Room = sharedRoom
	followerRuntime.NextObjectKey = nextKey
	followerRuntime.Rooms = map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit{
		runtimeDungeonRoomKeyFromScene(scene): {Scene: scene, Room: sharedRoom, Seed: 4242, DropRNG: 4242},
	}
	leader.dungeon.mu.Lock()
	leader.dungeon.runtime = leaderRuntime
	leader.dungeon.mu.Unlock()
	follower.dungeon.mu.Lock()
	follower.dungeon.runtime = followerRuntime
	follower.dungeon.mu.Unlock()

	if instance := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); instance == nil {
		t.Fatal("follower did not create shared party dungeon instance")
	}
	if instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene); instance == nil {
		t.Fatal("leader did not adopt shared party dungeon instance")
	}

	room := followerRuntime.Room
	if room == nil || room != leaderRuntime.Room {
		t.Fatalf("expected shared room, follower=%p leader=%p", followerRuntime.Room, leaderRuntime.Room)
	}
	snapshot := room.Snapshot()
	if len(snapshot.Monsters) == 0 {
		t.Fatal("fixture room has zero monsters")
	}

	// Follower announces first and kills all monsters in the room BEFORE leader announces.
	if _, err := room.AnnounceAllActors(followerRuntime.Session); err != nil {
		t.Fatalf("follower AnnounceAllActors: %v", err)
	}
	for _, m := range snapshot.Monsters {
		if _, _, err := room.CommitActorDeathReport(m.ObjectKey, followerRuntime.Session); err != nil {
			t.Fatalf("follower CommitActorDeathReport(%d): %v", m.ObjectKey, err)
		}
	}

	// Leader now arrives late and announces the already-cleared shared room.
	if _, err := room.AnnounceAllActors(leaderRuntime.Session); err != nil {
		t.Fatalf("late leader AnnounceAllActors: %v", err)
	}
	leaderScene, ok := leaderRuntime.Session.RuntimeScene()
	if !ok || !leaderScene.Cleared {
		t.Fatalf("late leader scene not marked cleared after AnnounceAllActors on pre-defeated room: ok=%t cleared=%t", ok, leaderScene.Cleared)
	}

	// Pre-defeated drop-free op38 replay emits one op38 per pre-defeated monster.
	leaderConn, ok := leader.conn.(*bufferConn)
	if !ok {
		t.Fatalf("unexpected leader conn type %T", leader.conn)
	}
	leaderConn.write.Reset()
	if err := service.sendPartyDungeonPreDefeatedActorsLocked(leader, leaderRuntime); err != nil {
		t.Fatalf("sendPartyDungeonPreDefeatedActorsLocked: %v", err)
	}
	var op38Count int
	raw := leaderConn.write.Bytes()
	for len(raw) > 0 {
		pkt, rest := splitGameServerUpperPacketWithHeader(t, raw, service.gameUpperHeaderSize())
		if pkt.Header.MsgID == uint16(dnfenum.CmdPacketNotifyDieMonster) {
			op38Count++
		}
		raw = rest
	}
	if op38Count != len(snapshot.Monsters) {
		t.Fatalf("pre-defeated op38 count=%d, want %d", op38Count, len(snapshot.Monsters))
	}

	// Test ReconcileDefeatedHostiles on a fresh session that skipped binding a defeated hostile.
	freshDS, _ := newPR1DungeonSession(t, topology)
	repaired, cleared := room.ReconcileDefeatedHostiles(freshDS)
	if repaired == 0 || !cleared {
		t.Fatalf("ReconcileDefeatedHostiles repaired=%d cleared=%t, want >0 and true", repaired, cleared)
	}

	// Test move preflight reconcile path on ErrRunCurrentRoomNotCleared.
	moveDS, moveScene := newPR1DungeonSession(t, topology)
	_, moveLeader, _, moveRuntime, _, _ := partyDungeonInstanceFixture(t, 9, 4244)
	moveRoom, moveNextKey, err := newRuntimeDungeonRoom(moveScene, monsters, firstDungeonMonsterObjectKey)
	if err != nil {
		t.Fatalf("moveRoom newRuntimeDungeonRoom: %v", err)
	}
	moveRuntime.Session = moveDS
	moveRuntime.Room = moveRoom
	moveRuntime.NextObjectKey = moveNextKey
	moveRuntime.Rooms = map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit{
		runtimeDungeonRoomKeyFromScene(moveScene): {Scene: moveScene, Room: moveRoom, Seed: 4244, DropRNG: 4244},
	}
	moveLeader.dungeon.mu.Lock()
	moveLeader.dungeon.runtime = moveRuntime
	moveLeader.dungeon.mu.Unlock()
	if _, err := moveRoom.AnnounceAllActors(moveDS); err != nil {
		t.Fatalf("moveRoom AnnounceAllActors: %v", err)
	}
	// Simulate shared room monsters becoming defeated without marking moveDS.
	moveRuntime.Room.mu.Lock()
	for i := range moveRuntime.Room.monsters {
		moveRuntime.Room.monsters[i].State = runtimeDungeonMonsterDefeated
	}
	moveRuntime.Room.mu.Unlock()
	curScene, _ := moveRuntime.Session.RuntimeScene()
	if curScene.Cleared {
		t.Fatal("expected test scene uncleared before reconcile")
	}
	var blockedReq dungeoncmd.MoveMapRequest
	foundDoor := false
	for _, delta := range [][2]int64{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		nx, ny := curScene.Coordinate.X+delta[0], curScene.Coordinate.Y+delta[1]
		if nx < 0 || ny < 0 {
			continue
		}
		candidate := dungeoncmd.MoveMapRequest{NextX: byte(nx), NextY: byte(ny)}
		if _, err := service.planCurrentDungeonMove(moveRuntime, candidate); errors.Is(err, worldmap.ErrRunCurrentRoomNotCleared) {
			blockedReq = candidate
			foundDoor = true
			break
		}
	}
	if !foundDoor {
		t.Fatal("fixture room has no adjacent exit that triggers ErrRunCurrentRoomNotCleared")
	}
	repairedCount, nowCleared := moveRuntime.Room.ReconcileDefeatedHostiles(moveRuntime.Session)
	if repairedCount == 0 || !nowCleared {
		t.Fatalf("expected reconcile to clear room, repaired=%d cleared=%t", repairedCount, nowCleared)
	}
	if _, err := service.planCurrentDungeonMove(moveRuntime, blockedReq); err != nil {
		t.Fatalf("planCurrentDungeonMove failed after reconcile: %v", err)
	}
}

type raidMuProbeCharRepo struct {
	dnfrepo.CharacterRepository
	onLoad func()
}

func (r *raidMuProbeCharRepo) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if r.onLoad != nil {
		r.onLoad()
	}
	return r.CharacterRepository.Load(ctx, id)
}

type raidInventoryLoadProbeRepo struct {
	dnfrepo.InventoryRepository
	loads int
}

func (r *raidInventoryLoadProbeRepo) Load(ctx context.Context, characterID string) (dnfrepo.InventoryRecord, bool, error) {
	r.loads++
	return r.InventoryRepository.Load(ctx, characterID)
}

func TestPR1RaidCreateDoesNotHoldRaidMuDuringCharacterLoad(t *testing.T) {
	f := raidSafetyFixture(t, nil, 1)
	loadCalled := false
	raidMuFreeDuringLoad := false
	orig := f.repos.Character
	f.repos.Character = &raidMuProbeCharRepo{
		CharacterRepository: orig,
		onLoad: func() {
			loadCalled = true
			if f.service.raidMu.TryLock() {
				raidMuFreeDuringLoad = true
				f.service.raidMu.Unlock()
			}
		},
	}
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return f.repos, true }

	session := f.session()
	session.selectedCharacterID = 19
	f.service.createRuntimeRaid(session, 19, raid.InfoRequest{RouteOrRaidType: 0, NameBytes: []byte("probe")})
	if !loadCalled {
		t.Fatal("expected Character.Load to be invoked during createRuntimeRaid")
	}
	if !raidMuFreeDuringLoad {
		t.Fatal("raidMu was held during Character.Load inside createRuntimeRaid")
	}
}

func TestPR1RaidStartReleasesRaidMuDuringDBAndRejectsConcurrentStart(t *testing.T) {
	f := raidSafetyFixture(t, nil, 1)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)

	// Hold Starting=true manually to verify concurrent start & membership ops reject cleanly
	// without deadlocking raidMu.
	f.service.raidMu.Lock()
	f.state.Starting = true
	f.service.raidMu.Unlock()

	if _, ok, _, err := f.service.startRuntimeRaidWithEntryCostPhase(session); ok || err == nil {
		t.Fatalf("concurrent start during Starting=true should be rejected, got ok=%t err=%v", ok, err)
	}
	if _, ok := f.service.modifyRuntimeRaidInfo(19, raid.InfoRequest{NameBytes: []byte("blocked")}); ok {
		t.Fatal("modifyRuntimeRaidInfo should be rejected while Starting=true")
	}
	if _, removed, _ := f.service.removeRuntimeRaidMember(19); removed {
		t.Fatal("removeRuntimeRaidMember should be rejected while Starting=true")
	}

	f.service.raidMu.Lock()
	f.state.Starting = false
	f.service.raidMu.Unlock()

	// Verify failed Phase-1 start (missing PVF dependencies) always resets Starting=false
	// and never leaks raidMu lock.
	_, ok, consumed, err := f.service.startRuntimeRaidWithEntryCostPhase(session)
	if ok || consumed || err == nil {
		t.Fatalf("expected missing battle dependency rejection, got ok=%t consumed=%t err=%v", ok, consumed, err)
	}
	f.service.raidMu.Lock()
	stillStarting := f.state.Starting
	f.service.raidMu.Unlock()
	if stillStarting {
		t.Fatal("Starting flag leaked true after rejected start")
	}
}

func TestPR1AntonPhase2StartSkipsEntryCostContainerRefresh(t *testing.T) {
	t.Setenv(raidStartEntryCostGuardEnv, "enforce")
	antonScript := `
[ENTRY COST]
10096296 1
[WAITING ROOM]
16 1
[RAID START MINIMUM MEMBER]
1
[SINGLE RAID START MINIUM MEMBER]
1
[RAID MEMBER MAX]
20
[PHASE MAX]
2
[START DELAY TIME]
5
[WEEKLY CLEAR COUNT]
3
[PHASE TIME OVER]
2400 1200
[PHASE BREAK TIME]
60
[MOVIE END TIME]
5 5 5 5
[SHIELD CHARGE RATE]
10 20
[HATCHERY INFO]
4 1 221 222 223 224
[PHASE]
[DUNGEON INFO]
[INDEX]
210
[INIT STATE]
` + "`open`" + `
[210 DGN HP]
1
[/DUNGEON INFO]
[TRIGGER]
[ON CHANGE DUNGEON STATE]
210
[CHECK DUNGEON STATE]
210 ` + "`clear`" + `
[/TRIGGER]
[BEHAVIOR]
[CLEAR PHASE]
[/BEHAVIOR]
[/PHASE]
[PHASE]
[DUNGEON INFO]
[INDEX]
221
[INIT STATE]
` + "`open`" + `
[221 DGN HP]
1
[/DUNGEON INFO]
[DUNGEON INFO]
[INDEX]
222
[INIT STATE]
` + "`open`" + `
[222 DGN HP]
1
[/DUNGEON INFO]
[DUNGEON INFO]
[INDEX]
223
[INIT STATE]
` + "`open`" + `
[223 DGN HP]
1
[/DUNGEON INFO]
[DUNGEON INFO]
[INDEX]
224
[INIT STATE]
` + "`open`" + `
[224 DGN HP]
1
[/DUNGEON INFO]
[TRIGGER]
[IF]
[BLACK VOLCANO BARRIER]
0
[/TRIGGER]
[BEHAVIOR]
[SET]
[BLACK VOLCANO BARRIER]
10000
[/BEHAVIOR]
[/PHASE]
`
	antonSymbols := `
7
[IS EXIST INFECTION DUNGEON]
8
[INFECTION DUNGEON INDEX]
110
[BLACK VOLCANO BARRIER]
126
[MOVIE BARRIER BREAK]
127
[MOVIE BARRIER RECOVER]
20
[210 DGN HP]
21
[221 DGN HP]
22
[222 DGN HP]
23
[223 DGN HP]
24
[224 DGN HP]
31
[221 HATCHERY FAIL]
32
[221 HATCHERY NAMED CREATE]
33
[222 HATCHERY FAIL]
34
[222 HATCHERY NAMED CREATE]
35
[223 HATCHERY FAIL]
36
[223 HATCHERY NAMED CREATE]
37
[224 HATCHERY FAIL]
38
[224 HATCHERY NAMED CREATE]
`
	antonSource := pr1MemorySource{
		"etc/raid/anton.etc":        antonScript,
		"etc/raid/anton.etc.symbol": antonSymbols,
		"passiveobject/actionobject/map/anton/phase2/transmission/action/transmission_s_last.act": "[RAID SET SYMBOL]\n110\n[-]\n100\n",
		"passiveobject/actionobject/map/anton/phase2/transmission/action/transmission_l_last.act": "[RAID SET SYMBOL]\n110\n[-]\n200\n",
		"monster/newmonsters/anton/phase3/constructor/action/last.act":                            "[CHECK RAID SYMBOL]\n8\n[=]\n221\n[RAID SET SYMBOL]\n110\n[-]\n300\n[RAID SET SYMBOL]\n110\n[-]\n500\n",
		"monster/newmonsters/anton/phase3/constructor/action/last_1.act":                          "[CHECK RAID SYMBOL]\n8\n[=]\n222\n[RAID SET SYMBOL]\n110\n[-]\n300\n[RAID SET SYMBOL]\n110\n[-]\n500\n",
		"monster/newmonsters/anton/phase3/constructor/action/last_2.act":                          "[CHECK RAID SYMBOL]\n8\n[=]\n223\n[RAID SET SYMBOL]\n110\n[-]\n300\n[RAID SET SYMBOL]\n110\n[-]\n500\n",
		"monster/newmonsters/anton/phase3/constructor/action/last_3.act":                          "[CHECK RAID SYMBOL]\n8\n[=]\n224\n[RAID SET SYMBOL]\n110\n[-]\n300\n[RAID SET SYMBOL]\n110\n[-]\n500\n",
	}
	program, err := raid.LoadAntonProgram(antonSource)
	if err != nil {
		t.Fatalf("LoadAntonProgram: %v", err)
	}
	now := time.Now().Add(-2 * time.Minute)
	prog, err := program.BeginPhase(0, now, 12345)
	if err != nil {
		t.Fatalf("BeginPhase(0): %v", err)
	}
	prog, _, err = prog.ClearDungeon("run-1", 210, prog.Epoch(), now.Add(time.Second))
	if err != nil || !prog.PhaseCleared() {
		t.Fatalf("ClearDungeon phase 0: err=%v cleared=%t", err, prog.PhaseCleared())
	}

	f := raidSafetyFixture(t, nil, 1)
	f.service.options.currentClientVersion = "official-24.9"
	f.service.dungeonRuleSource = antonSource
	invProbe := &raidInventoryLoadProbeRepo{InventoryRepository: f.repos.Inventory}
	f.repos.Inventory = invProbe
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return f.repos, true }

	leader := f.session()
	leader.loginPacketSeen, leader.accountFromToken = true, true
	leader.channel.Type = byte(dnfenum.RaidChannelType)
	f.service.registerLiveGameSession(leader)
	t.Cleanup(func() { f.service.unregisterLiveGameSession(leader) })
	f.service.bindGameSessionCharacter(leader, 19)
	f.service.onlinePlayers = newOnlinePlayerManager()
	f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID:        19,
		AccountID:          "account-1",
		Session:            leader,
		ChannelID:          leader.channel.ID,
		TownID:             16,
		AreaID:             1,
		Instance:           f.state.RaidKey,
		RaidBattleInstance: "inst-phase2-skip",
	})

	completedAt := now.Add(time.Second)
	flow := &currentAntonRewardFlow{
		InstanceID:  "inst-phase2-skip",
		Phase:       0,
		CompletedAt: completedAt,
		FinishedAt:  completedAt.Add(time.Second),
		BreakUntil:  completedAt.Add(2 * time.Second),
		Recipients:  []currentAntonRewardRecipient{{CharacterID: 19, AccountID: "account-1", Group: 1}},
		Party:       map[uint16]*currentAntonRewardCard{19: {Committed: true}},
		Squad:       map[uint16]*currentAntonRewardCard{19: {Committed: true}},
	}
	f.state.CurrentLayout = true
	f.state.Started = true
	f.state.Members[0].GroupIndex = 1
	f.state.Battle = &runtimeRaidBattle{
		InstanceID:       "inst-phase2-skip",
		Mode:             1,
		ChannelID:        leader.channel.ID,
		ChannelType:      leader.channel.Type,
		Phase:            0,
		PhaseCompleted:   true,
		PhaseCompletedAt: completedAt,
		Anton:            prog,
		Dungeons:         prog.Dungeons(),
		MemberAccounts:   map[uint16]string{19: "account-1"},
		Configuration:    raid.BattleConfiguration{StartDelaySeconds: 5, PhaseLimitSeconds: []uint32{1200, 1200}},
		RewardFlow:       [2]*currentAntonRewardFlow{flow, nil},
	}

	invProbe.loads = 0
	f.conn.write.Reset()
	if err := f.service.handleOnlineStartRaid(leader, uint16(dnfenum.CmdPacketStartRaid), nil); err != nil {
		t.Fatalf("handleOnlineStartRaid phase 2: %v", err)
	}
	if f.state.Battle.Phase != 1 {
		t.Fatalf("expected Battle.Phase=1 after phase-2 start, got %d", f.state.Battle.Phase)
	}
	if strings.Contains(f.logText(t), "raid_entry_cost_after_commit") {
		t.Fatalf("phase-2 start unexpectedly logged raid_entry_cost_after_commit container refresh:\n%s", f.logText(t))
	}
	raw := f.conn.write.Bytes()
	for len(raw) > 0 {
		pkt, rest := splitGameServerUpperPacketWithHeader(t, raw, f.service.gameUpperHeaderSize())
		if pkt.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) {
			t.Fatalf("phase-2 start unexpectedly emitted op13 container-list refresh packet")
		}
		raw = rest
	}
	if invProbe.loads >= len(currentSelectedItemListTypes) {
		t.Fatalf("phase-2 start triggered %d inventory loads (expected no 5-container entry_cost_containers refresh)", invProbe.loads)
	}
}

func TestPR1FiendWarClockSameProjectionSuppressesIdleTicks(t *testing.T) {
	cfg := &fiendwar.FiendWarBattleConfig{
		Raid: &fiendwar.AntonConfig{},
		Areas: map[int]fiendwar.FiendWarArea{
			1: {Index: 1, Kind: "radar", Dungeon: 5101},
		},
		Rules:   []fiendwar.FiendWarRule{{}},
		Symbols: map[string]uint32{"dummy": 1},
	}
	battle, err := fiendwar.NewFiendWarBattle(cfg)
	if err != nil {
		t.Fatalf("NewFiendWarBattle: %v", err)
	}
	baseNow := time.Date(2026, 11, 24, 21, 0, 0, 0, time.UTC)
	battle.MerchantArea = 1
	battle.MerchantExpiresAt = baseNow.Add(10 * time.Second)

	f := raidSafetyFixture(t, nil, 1)
	f.service.options.currentClientVersion = "official-24.9"
	rb := &runtimeRaidBattle{
		InstanceID:     "fiendwar-pr1-clock",
		Mode:           3,
		EnterAfter:     baseNow.Add(-time.Minute),
		Deadline:       baseNow.Add(30 * time.Minute),
		FiendWar:       battle,
		MemberAccounts: map[uint16]string{19: "account-19"},
	}
	f.state.Started = true
	f.state.Battle = rb

	// Idle tick before merchant expiry: projection unchanged, ProgressVersion must not bump.
	states, errs := f.service.advanceCurrentRaidClocks(baseNow.Add(time.Second))
	if len(errs) != 0 || len(states) != 0 || rb.ProgressVersion != 0 {
		t.Fatalf("idle FiendWar tick mutated projection: states=%d errs=%v version=%d", len(states), errs, rb.ProgressVersion)
	}
	states, errs = f.service.advanceCurrentRaidClocks(baseNow.Add(2 * time.Second))
	if len(errs) != 0 || len(states) != 0 || rb.ProgressVersion != 0 {
		t.Fatalf("second idle FiendWar tick mutated projection: states=%d errs=%v version=%d", len(states), errs, rb.ProgressVersion)
	}

	// Tick at merchant expiry: MerchantArea clears -> SameProjection false -> broadcast once!
	states, errs = f.service.advanceCurrentRaidClocks(baseNow.Add(10 * time.Second))
	if len(errs) != 0 || len(states) != 1 || rb.ProgressVersion != 1 || rb.FiendWar.MerchantArea != 0 {
		t.Fatalf("expected FiendWar projection update on merchant expiry: states=%d errs=%v version=%d merchant=%d",
			len(states), errs, rb.ProgressVersion, rb.FiendWar.MerchantArea)
	}

	// Next idle tick after expiry must again produce 0 updates.
	states, errs = f.service.advanceCurrentRaidClocks(baseNow.Add(11 * time.Second))
	if len(errs) != 0 || len(states) != 0 || rb.ProgressVersion != 1 {
		t.Fatalf("post-expiry idle tick emitted duplicate update: states=%d errs=%v version=%d", len(states), errs, rb.ProgressVersion)
	}
}

var _ net.Conn = (*pr1FailingWriteConn)(nil)
