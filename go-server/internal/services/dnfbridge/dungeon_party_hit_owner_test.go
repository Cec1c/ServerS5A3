package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住缺陷 B（操作者：「双方打同一个怪物有概率有一人的伤害为 1。」）。
//
// 取证与修法见 dungeon_party_hit_owner.go。服务端**不计算伤害**（没有 op180/734/735 处理器、
// op39 的 CombatEntries 是不透明字节、全仓没有伤害下限），所以这里钉的是**唯一能把
// "两个攻击者里恰好一人的命中"判无效的那条闸门**：两打一时 op39 的 related actor 是队友
// （现场 `related_actor_object_key=6 player_object_key=3`），默认档必须放行，且
// "退化成最小伤害 1"这个判定在两打一形状下不可达。

type hitOwnerFixtureState struct {
	service     *Service
	leader      *gameSession
	member      *gameSession
	leaderConn  *bufferConn
	memberConn  *bufferConn
	partyIDText string
	monsterKeys []uint32
	logger      *packetLogger
	logPath     string
}

// hitOwnerPVFSource 在既有合成副本上**只加一行怪**：两打一形状需要同一间房里至少两只
// 已宣告的敌对 actor（一只由队长侧上报、一只由队员侧上报，互不覆盖）。
func hitOwnerPVFSource() bridgePVFSource {
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(
		source["dungeon/test.dgn"],
		"[limit party count]\n1",
		"[limit party count]\n4",
		1,
	)
	source["map/dungeon/test/start.map"] += "3001 11 0 140 240 0 0 0 `[fixed]` `[normal]`\n"
	return source
}

func newHitOwnerFixture(t *testing.T) hitOwnerFixtureState {
	t.Helper()
	source := hitOwnerPVFSource()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	for _, characterID := range []string{"99", "100"} {
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: characterID,
			AccountID:   "account-1",
			Level:       20,
			Stats:       map[string]int64{"fatigue": 100},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: characterID,
			Slots:       map[string]dnfrepo.ItemStack{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		onlinePlayers:       newOnlinePlayerManager(),
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	channel := channelcatalog.Channel{ServerID: 1, ID: 121, Type: 11, Name: "ch.121", Port: 10121}
	leader := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, selectedCharacterID: 99}
	member := &gameSession{conn: &bufferConn{}, channel: channel, residentChannel: channel, selectedCharacterID: 100}
	service.bindGameSessionCharacter(leader, 99)
	service.bindGameSessionCharacter(member, 100)
	state := alignedcmd.PartyState{
		PartyID: 12,
		UserID:  99,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 99, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: 100, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(member, state)

	leaderRuntime := (*runtimeDungeonState)(nil)
	for _, session := range []*gameSession{leader, member} {
		runtime, _, err := service.prepareDungeonRuntime(
			context.Background(),
			session,
			dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1},
		)
		if err != nil {
			t.Fatalf("prepare party member %d dungeon runtime: %v", session.selectedCharacterID, err)
		}
		scene, ok := runtime.Session.Scene()
		if !ok {
			t.Fatalf("party member %d runtime has no scene", session.selectedCharacterID)
		}
		if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
			t.Fatalf("announce party member %d actors: %v", session.selectedCharacterID, err)
		}
		session.dungeon.mu.Lock()
		session.dungeon.runtime = runtime
		session.dungeon.mu.Unlock()
		if instance := service.adoptPartyDungeonInstance(session, runtime, scene); instance == nil {
			t.Fatalf("party member %d did not join the shared dungeon instance", session.selectedCharacterID)
		}
		if session == leader {
			leaderRuntime = runtime
		}
	}
	if got := service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("party dungeon instance count=%d want 1 (both members must share one run)", got)
	}
	snapshot := leaderRuntime.Room.Snapshot()
	keys := make([]uint32, 0, len(snapshot.Monsters))
	for _, monster := range snapshot.Monsters {
		if monster.State != runtimeDungeonMonsterAnnounced {
			t.Fatalf("fixture monster %d is not announced: %+v", monster.ObjectKey, monster)
		}
		keys = append(keys, monster.ObjectKey)
	}
	if len(keys) < 2 {
		t.Fatalf("fixture needs at least two announced monsters, got %d", len(keys))
	}
	logPath := filepath.Join(t.TempDir(), "hit-owner.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	service.packetLog = logger
	leaderConn := leader.conn.(*bufferConn)
	memberConn := member.conn.(*bufferConn)
	leaderConn.write.Reset()
	memberConn.write.Reset()
	return hitOwnerFixtureState{
		service:     service,
		leader:      leader,
		member:      member,
		leaderConn:  leaderConn,
		memberConn:  memberConn,
		partyIDText: strconv.Itoa(runtimePartyStateSnapshot(leader).PartyID),
		monsterKeys: keys,
		logger:      logger,
		logPath:     logPath,
	}
}

func (fixture hitOwnerFixtureState) readLog(t *testing.T) string {
	t.Helper()
	if err := fixture.logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged)
}

// twoAttackerDieMonsterBody 是当前 EXE 的 variable-combat op39 体：
// u32 runtimeObjectKey | u16 relatedActorObjectKey | … | body[22]=combatEntryCount | entries | 43B tail。
func twoAttackerDieMonsterBody(targetObjectKey uint32, relatedActorObjectKey uint16, combatEntries byte) []byte {
	body := make([]byte, dungeoncmd.DieMonsterVariableBaseSize+int(combatEntries)*dungeoncmd.DieMonsterVariableCombatEntrySize)
	binary.LittleEndian.PutUint32(body[0:4], targetObjectKey)
	binary.LittleEndian.PutUint16(body[4:6], relatedActorObjectKey)
	body[22] = combatEntries
	return body
}

func countNotifyDieMonsterFrames(t *testing.T, conn *bufferConn) int {
	t.Helper()
	count := 0
	for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
		if frame.msgID == uint16(dnfenum.CmdPacketNotifyDieMonster) {
			count++
		}
	}
	return count
}

// 默认档：两打一时**两个方向**的上报都必须被接受（谁先上报都不会有人的命中被判无效），
// 且退化成最小伤害 1 的判定不可达。
func TestPartyHitOwnerAcceptsBothAttackersOfTheSameMonsterWithoutClamping(t *testing.T) {
	fixture := newHitOwnerFixture(t)
	// 方向一：队长（99）上报"队员（100）打死了这只怪" —— 现场那一行的形状。
	leaderTarget := fixture.monsterKeys[0]
	if err := fixture.service.handleDungeonMonsterDeath(
		fixture.leader,
		twoAttackerDieMonsterBody(leaderTarget, 100, 2),
	); err != nil {
		t.Fatal(err)
	}
	// 方向二：队员（100）上报"队长（99）打死了另一只怪"。
	memberTarget := fixture.monsterKeys[1]
	if err := fixture.service.handleDungeonMonsterDeath(
		fixture.member,
		twoAttackerDieMonsterBody(memberTarget, 99, 2),
	); err != nil {
		t.Fatal(err)
	}

	// 两侧都要拿到 op38：自己的上报被接受（至少 1 条），再加上共享实例的权威死亡广播。
	if got := countNotifyDieMonsterFrames(t, fixture.leaderConn); got < 1 {
		t.Fatalf("leader received %d op38 death notifications, want at least its own accepted report", got)
	}
	if got := countNotifyDieMonsterFrames(t, fixture.memberConn); got < 2 {
		t.Fatalf("member received %d op38 death notifications, want its own report plus the peer broadcast", got)
	}

	text := fixture.readLog(t)
	if strings.Contains(text, "reason=room_related_actor_object_key_mismatch") {
		t.Fatalf("two attackers on one monster still hit the owner gate\n%s", text)
	}
	if strings.Contains(text, "kind=game-dungeon-party-hit-clamped-to-one") {
		t.Fatalf("a two-attacker hit was still clamped to the minimum damage\n%s", text)
	}
	accepted := 0
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "kind=game-dungeon-party-hit-accepted") {
			continue
		}
		accepted++
		required := []string{
			"party_id=" + fixture.partyIDText,
			"instance_id=1",
			"same_party_dungeon_instance=true",
			"damage_floor=real",
			"clamped_to_one=false",
			"gate=party_peer_attacker_same_instance",
			"damage_evidence_combat_entries=2",
			"switch=" + dungeonPartyHitOwnerEnv,
			"mode=party",
		}
		for _, field := range required {
			if !strings.Contains(line, field) {
				t.Fatalf("hit-accepted event is missing %q\nline=%s", field, line)
			}
		}
	}
	if accepted != 2 {
		t.Fatalf("hit-accepted events=%d want 2 (one per attacker direction)\n%s", accepted, text)
	}
	if !strings.Contains(text, "attacker_char_id=100") || !strings.Contains(text, "attacker_char_id=99") {
		t.Fatalf("hit-accepted events must name both attackers\n%s", text)
	}
	if !strings.Contains(text, "target_object_key="+strconv.FormatUint(uint64(leaderTarget), 10)) ||
		!strings.Contains(text, "target_object_key="+strconv.FormatUint(uint64(memberTarget), 10)) {
		t.Fatalf("hit-accepted events must name both targets\n%s", text)
	}

	// 「退化成最小伤害 1」这条判定在两打一形状下不可达：解析结果必须是 accepted + floor != 1。
	for _, related := range []uint16{99, 100} {
		session := fixture.member
		if related == 100 {
			session = fixture.leader
		}
		runtime := session.dungeon.runtime
		resolution := fixture.service.resolveCurrentDungeonPartyHitOwner(
			session,
			runtime,
			related,
			dungeonPartyHitOwnerParty,
		)
		if !resolution.accepted {
			t.Fatalf("related actor %d of the same party/instance was not accepted: %+v", related, resolution)
		}
		verdict := dungeonPartyHitVerdictForAccepted(resolution.accepted)
		if floor := currentDungeonPartyHitDamageFloor(verdict); floor == currentDungeonPartyHitMinimumDamage {
			t.Fatalf("related actor %d can still be clamped to %d", related, floor)
		}
	}
}

// 回退档 `strict` 逐字节回到今天：同一条两打一上报被判成
// `room_related_actor_object_key_mismatch`，一个字节都不回，且不记本文件的新事件。
func TestPartyHitOwnerStrictLiteralReproducesTodaysGate(t *testing.T) {
	t.Setenv(dungeonPartyHitOwnerEnv, dungeonPartyHitOwnerValueStrict)
	fixture := newHitOwnerFixture(t)
	target := fixture.monsterKeys[0]
	if err := fixture.service.handleDungeonMonsterDeath(
		fixture.leader,
		twoAttackerDieMonsterBody(target, 100, 2),
	); err != nil {
		t.Fatal(err)
	}
	if got := countNotifyDieMonsterFrames(t, fixture.leaderConn); got != 0 {
		t.Fatalf("strict mode sent %d op38 frames, want 0 (today's byte-for-byte behaviour)", got)
	}
	text := fixture.readLog(t)
	if !strings.Contains(text, "kind=game-dungeon-monster-death-blocked") ||
		!strings.Contains(text, "reason=room_related_actor_object_key_mismatch") {
		t.Fatalf("strict mode did not reproduce today's owner gate\n%s", text)
	}
	for _, forbidden := range []string{
		"kind=game-dungeon-party-hit-accepted",
		"kind=game-dungeon-party-hit-clamped-to-one",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("strict mode logged a new event %q\n%s", forbidden, text)
		}
	}
}

// 不是队友的攻击者仍然拒收（不改动原有边界），但默认档要把"这次命中会退化成最小伤害 1"
// 的原因写成一条可判读事件。
func TestPartyHitOwnerStillBlocksAStrangerAndExplainsTheClamp(t *testing.T) {
	fixture := newHitOwnerFixture(t)
	const strangerObjectKey = uint16(4242)
	target := fixture.monsterKeys[0]
	if err := fixture.service.handleDungeonMonsterDeath(
		fixture.leader,
		twoAttackerDieMonsterBody(target, strangerObjectKey, 1),
	); err != nil {
		t.Fatal(err)
	}
	if got := countNotifyDieMonsterFrames(t, fixture.leaderConn); got != 0 {
		t.Fatalf("a stranger's hit produced %d op38 frames, want 0", got)
	}
	text := fixture.readLog(t)
	if !strings.Contains(text, "kind=game-dungeon-monster-death-blocked") ||
		!strings.Contains(text, "reason=room_related_actor_object_key_mismatch") {
		t.Fatalf("stranger hit did not reproduce the owner gate\n%s", text)
	}
	line := ""
	for _, candidate := range strings.Split(text, "\n") {
		if strings.Contains(candidate, "kind=game-dungeon-party-hit-clamped-to-one") {
			line = candidate
			break
		}
	}
	if line == "" {
		t.Fatalf("clamp event missing for the stranger hit\n%s", text)
	}
	for _, field := range []string{
		"attacker_is_party_member=false",
		"attacker_in_same_dungeon_instance=false",
		"reporter_char_id=99",
		"related_actor_object_key=" + strconv.Itoa(int(strangerObjectKey)),
		"damage_floor=1",
		"clamped_to_one=true",
		"reason=related_actor_is_not_a_member_of_the_reporting_session_party",
	} {
		if !strings.Contains(line, field) {
			t.Fatalf("clamp event is missing %q\nline=%s", field, line)
		}
	}
}

// 同队但**已经不在同一场副本实例里**的队友不算两打一：仍然拒收（fail-closed）。
func TestPartyHitOwnerRejectsAPartyMemberFromAnotherInstance(t *testing.T) {
	fixture := newHitOwnerFixture(t)
	// 把队员从实例里摘掉（模拟它已经出图/在别的场），队伍关系仍在。
	fixture.service.releasePartyDungeonInstance(
		fixture.member.dungeon.runtime,
		fixture.member.selectedCharacterID,
		"test_member_left_the_instance",
	)
	target := fixture.monsterKeys[0]
	if err := fixture.service.handleDungeonMonsterDeath(
		fixture.leader,
		twoAttackerDieMonsterBody(target, 100, 2),
	); err != nil {
		t.Fatal(err)
	}
	if got := countNotifyDieMonsterFrames(t, fixture.leaderConn); got != 0 {
		t.Fatalf("a peer outside the instance produced %d op38 frames, want 0", got)
	}
	text := fixture.readLog(t)
	if !strings.Contains(text, "kind=game-dungeon-party-hit-clamped-to-one") ||
		!strings.Contains(text, "reason=party_peer_is_not_in_this_dungeon_instance") {
		t.Fatalf("out-of-instance peer was not rejected with a named reason\n%s", text)
	}
}
