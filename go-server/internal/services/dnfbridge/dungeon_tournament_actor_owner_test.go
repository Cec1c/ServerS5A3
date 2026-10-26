package dnfbridge

// Synthetic actor-owner fixtures: binding order, wire identity and refusal
// before mutation. Resource identity is covered by the real-PVF regression.

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

const tournamentOwnerTestMapID = int64(102)

// tournamentOwnerTestMonitorRef / tournamentOwnerTestAPCRef 只是语法糖，避免每处手写 Kind。
func tournamentOwnerTestMonitorRef(index int) worldmap.HostileReference {
	return worldmap.HostileReference{Kind: worldmap.HostileMonster, Index: index}
}

func tournamentOwnerTestAPCRef(index int) worldmap.HostileReference {
	return worldmap.HostileReference{Kind: worldmap.HostileAICharacter, Index: index}
}

func ptrTournamentOwnerRef(reference worldmap.HostileReference) *worldmap.HostileReference {
	return &reference
}

// tournamentOwnerTestScene 造一个只承载 TournamentActorOrder 的最小场景。
// bindTournamentRoomPath 只读 scene.Map.Map.TournamentActorOrder；
// Monsters/AICharacters 一并给出只是为了让夹具形状与生产投影后一致。
func tournamentOwnerTestScene(
	monsterCount int,
	apcCount int,
	order []worldmap.HostileReference,
) worldmap.DungeonRoomScene {
	scene := worldmap.DungeonRoomScene{
		Coordinate:     worldmap.RoomCoordinate{X: 1, Y: 1},
		Map:            worldmap.ResolvedMap{Map: worldmap.Map{ID: tournamentOwnerTestMapID}},
		RuntimeObjects: make(map[uint32]worldmap.HostileReference),
	}
	for index := 0; index < monsterCount; index++ {
		scene.Monsters = append(scene.Monsters, worldmap.MonsterSpawn{
			MonsterID: int64(3001 + index),
			Level:     70,
			Position:  worldmap.Point3{X: 10, Y: 20},
		})
		scene.ExpectedHostiles = append(scene.ExpectedHostiles, tournamentOwnerTestMonitorRef(index))
	}
	for index := 0; index < apcCount; index++ {
		scene.AICharacters = append(scene.AICharacters, worldmap.AICharacter{
			Code:     int64(4001 + index),
			Position: worldmap.Point{X: 10, Y: 20},
			Faction:  "[monster]",
			AIType:   "[boss]",
		})
		scene.ExpectedHostiles = append(scene.ExpectedHostiles, tournamentOwnerTestAPCRef(index))
	}
	scene.Map.Map.TournamentActorOrder = append([]worldmap.HostileReference(nil), order...)
	return scene
}

// tournamentOwnerTestBuildRoom 按 newRuntimeDungeonRoom + AttachExtendedActors 的既有口径铺房间：
// 普通 [monster] 先连续发号，扩展 actor 紧随其后。扩展 actor 的 HostileReference 为 nil
// 表示"非敌对/非路径"行（生产上就是 [character] 阵营 APC）。
func tournamentOwnerTestBuildRoom(
	monsterRefs []worldmap.HostileReference,
	extendedRefs []*worldmap.HostileReference,
	first uint32,
) *runtimeDungeonRoom {
	room := &runtimeDungeonRoom{
		coordinate:          worldmap.RoomCoordinate{X: 1, Y: 1},
		mapID:               tournamentOwnerTestMapID,
		byObjectKey:         make(map[uint32]int, len(monsterRefs)),
		extendedByObjectKey: make(map[uint32]int, len(extendedRefs)),
	}
	next := first
	for index, reference := range monsterRefs {
		room.byObjectKey[next] = index
		room.monsters = append(room.monsters, runtimeDungeonMonster{
			ObjectKey: next,
			Reference: reference,
			Spawn:     worldmap.MonsterSpawn{MonsterID: int64(3001 + index), Level: 70},
			State:     runtimeDungeonMonsterPlanned,
		})
		next++
	}
	for index, reference := range extendedRefs {
		actor := runtimeDungeonExtendedActor{
			Kind:      runtimeDungeonActorAICharacter,
			ObjectKey: next,
			Packet: currentDungeonStartMapActor{
				ObjectKey:   uint16(next),
				Code:        uint32(5000 + index),
				PacketIndex: uint32(index),
				Level:       70,
				Type:        5,
			},
			State: runtimeDungeonMonsterPlanned,
		}
		if reference != nil {
			copied := *reference
			actor.HostileReference = &copied
		}
		room.extendedByObjectKey[next] = index
		room.extendedActors = append(room.extendedActors, actor)
		next++
	}
	return room
}

// tournamentOwnerTestTournament 造一份 PathActors 长度/类型可控、Rounds 与生产同形状
// （16/8/4/2 队、每队 PartyLimit=1 人）的赛制句柄。PartyLimit 固定 1：一轮一个路径 actor，
// 便于把 order 下标直接当轮次，也便于用生产 386 构造器核对公告序号。
func tournamentOwnerTestTournament(types []byte) *tournamentDungeonRuntime {
	pathActors := make([]tournamentPathActor, len(types))
	for index, actorType := range types {
		pathActors[index] = tournamentPathActor{
			Code:       int64(6001 + index),
			Level:      70,
			Type:       actorType,
			IsBlocking: true,
		}
	}
	rounds := make([]tournamentRoundSnapshot, 0, tournamentRoundCount)
	teamCount := tournamentTeamCount
	for round := byte(1); round <= tournamentRoundCount; round++ {
		teams := make([]tournamentTeamSnapshot, 0, teamCount)
		for position := 0; position < teamCount; position++ {
			teams = append(teams, newTournamentTeamSnapshot(byte(position), []tournamentActorSnapshot{{
				Code:     int64(3001 + position),
				Strength: int64(100 + position),
				Level:    70,
			}}, false))
		}
		rounds = append(rounds, tournamentRoundSnapshot{Number: round, Teams: teams})
		teamCount /= 2
	}
	return &tournamentDungeonRuntime{
		Definition: &tournamentDefinition{
			DungeonID:  120,
			MapID:      tournamentOwnerTestMapID,
			BasisLevel: 70,
			PartyLimit: 1,
		},
		Rounds:            rounds,
		PathActors:        pathActors,
		acceptedSequences: make(map[int]struct{}, len(pathActors)),
		currentRound:      1,
	}
}

// tournamentOwnerTestRoomState 是"拒绝时必须逐字段不变"的房间侧绑定态快照。
// 赛制句柄自己的 firstSequence 不属于房间，单独断言。
type tournamentOwnerTestRoomState struct {
	MonsterKeys         []uint32
	ExtendedKeys        []uint32
	ExtendedPacketKeys  []uint16
	ExtendedPacketCodes []uint32
	ByObjectKey         map[uint32]int
	ExtendedByObjectKey map[uint32]int
	AnnouncedSessions   int
}

func tournamentOwnerTestState(t *testing.T, room *runtimeDungeonRoom) tournamentOwnerTestRoomState {
	t.Helper()
	state := tournamentOwnerTestRoomState{}
	if room == nil {
		return state
	}
	room.mu.RLock()
	defer room.mu.RUnlock()
	for _, monster := range room.monsters {
		state.MonsterKeys = append(state.MonsterKeys, monster.ObjectKey)
	}
	for _, actor := range room.extendedActors {
		state.ExtendedKeys = append(state.ExtendedKeys, actor.ObjectKey)
		state.ExtendedPacketKeys = append(state.ExtendedPacketKeys, actor.Packet.ObjectKey)
		state.ExtendedPacketCodes = append(state.ExtendedPacketCodes, actor.Packet.Code)
	}
	state.ByObjectKey = make(map[uint32]int, len(room.byObjectKey))
	for key, index := range room.byObjectKey {
		state.ByObjectKey[key] = index
	}
	state.ExtendedByObjectKey = make(map[uint32]int, len(room.extendedByObjectKey))
	for key, index := range room.extendedByObjectKey {
		state.ExtendedByObjectKey[key] = index
	}
	state.AnnouncedSessions = len(room.announcedSessions)
	return state
}

// tournamentOwnerTestKeyForReference 返回唯一拥有该 ref 的 room actor 的 ObjectKey；
// 拥有者数量 != 1 时直接 Fatal —— "每 ref 恰一 owner" 是绑定后的不变式。
func tournamentOwnerTestKeyForReference(t *testing.T, room *runtimeDungeonRoom, reference worldmap.HostileReference) uint32 {
	t.Helper()
	snapshot := room.Snapshot()
	owners := 0
	var key uint32
	for _, monster := range snapshot.Monsters {
		if monster.Reference == reference {
			owners++
			key = monster.ObjectKey
		}
	}
	for _, actor := range snapshot.ExtendedActors {
		if actor.HostileReference != nil && *actor.HostileReference == reference {
			owners++
			key = actor.ObjectKey
		}
	}
	if owners != 1 {
		t.Fatalf("reference %s/%d has %d owners in the room; want exactly 1",
			reference.Kind, reference.Index, owners)
	}
	return key
}

// tournamentOwnerTestAssertOwnerInvariants 校验"路径 actor 占 first..first+len(order)-1、
// 每个 key 唯一、两张索引表与 actor 一一对应、扩展 actor 的 Packet.ObjectKey 同步"。
func tournamentOwnerTestAssertOwnerInvariants(
	t *testing.T,
	room *runtimeDungeonRoom,
	tournament *tournamentDungeonRuntime,
	first uint32,
	order []worldmap.HostileReference,
) {
	t.Helper()
	snapshot := room.Snapshot()
	seen := make(map[uint32]string, len(snapshot.Monsters)+len(snapshot.ExtendedActors))
	for index, monster := range snapshot.Monsters {
		if previous, exists := seen[monster.ObjectKey]; exists {
			t.Fatalf("monster %d key %d collides with %s", index, monster.ObjectKey, previous)
		}
		seen[monster.ObjectKey] = "monster"
		if got := room.byObjectKey[monster.ObjectKey]; got != index {
			t.Fatalf("byObjectKey[%d]=%d want monster index %d", monster.ObjectKey, got, index)
		}
	}
	for index, actor := range snapshot.ExtendedActors {
		if previous, exists := seen[actor.ObjectKey]; exists {
			t.Fatalf("extended actor %d key %d collides with %s", index, actor.ObjectKey, previous)
		}
		seen[actor.ObjectKey] = "extended"
		if got := room.extendedByObjectKey[actor.ObjectKey]; got != index {
			t.Fatalf("extendedByObjectKey[%d]=%d want extended index %d", actor.ObjectKey, got, index)
		}
		if uint32(actor.Packet.ObjectKey) != actor.ObjectKey {
			t.Fatalf("extended actor %d owner key=%d packet key=%d are out of sync",
				index, actor.ObjectKey, actor.Packet.ObjectKey)
		}
		if actor.ObjectKey > 0xFFFF {
			t.Fatalf("extended actor %d key=%d exceeds the u16 wire range", index, actor.ObjectKey)
		}
	}
	if len(seen) != len(snapshot.Monsters)+len(snapshot.ExtendedActors) {
		t.Fatalf("room keys are not unique: %d keys for %d actors",
			len(seen), len(snapshot.Monsters)+len(snapshot.ExtendedActors))
	}
	for index, reference := range order {
		want := first + uint32(index)
		if got := tournamentOwnerTestKeyForReference(t, room, reference); got != want {
			t.Fatalf("path actor %d (ref %s/%d) key=%d want %d (first+index)",
				index, reference.Kind, reference.Index, got, want)
		}
	}
	if got := tournament.boundFirstSequence(); got != uint16(first) {
		t.Fatalf("bound first sequence=%d want %d", got, first)
	}
	total := uint32(len(snapshot.Monsters) + len(snapshot.ExtendedActors))
	if got := room.highObjectKey(); got != first+total {
		t.Fatalf("high object key=%d want %d", got, first+total)
	}
}

// tournamentOwnerTestFindPathSection 在 386 公告体里定位路径 actor 段。第二段的逐轮记录是
// 【round.Number(1) + PartyLimit×(u16 sequence + i32 Code + u8 Level + u8 Type)】，
// 所以 PartyLimit=1 时每条记录 9 字节、u16 序号在记录内偏移 1（既有 in-tree 用例读的
// 225+9i 正是"首条记录 224 起 + 1"）。本函数对每个候选起点逐槽要求
// "轮号 == i+1、u16 序号 == first+i、i32 Code / u8 Level / u8 Type 与路径定义逐值相等"，
// 全部命中才返回该起点；-1 表示不存在这样的连续块。以末尾边界和逐字段匹配共同核对。
func tournamentOwnerTestFindPathSection(body []byte, pathActors []tournamentPathActor, first uint16, partyLimit int) int {
	if partyLimit != 1 {
		return -1
	}
	const recordSize = 1 + 8
	size := len(pathActors) * recordSize
	if size == 0 || size > len(body) {
		return -1
	}
	for offset := 6; offset+size <= len(body); offset++ {
		matched := true
		for index, actor := range pathActors {
			at := offset + index*recordSize
			if body[at] != byte(index+1) ||
				binary.LittleEndian.Uint16(body[at+1:at+3]) != first+uint16(index) ||
				int64(int32(binary.LittleEndian.Uint32(body[at+3:at+7]))) != actor.Code ||
				body[at+7] != actor.Level ||
				body[at+8] != actor.Type {
				matched = false
				break
			}
		}
		if matched {
			return offset
		}
	}
	return -1
}

// ── 拒绝边界（父批"先验证后改 key"契约）──────────────────────────────────

func TestTournamentOwnerBindTournamentRoomPathRejectsBeforeMutation(t *testing.T) {
	cases := []struct {
		name       string
		monsters   []worldmap.HostileReference
		extended   []*worldmap.HostileReference
		order      []worldmap.HostileReference
		pathTypes  []byte
		first      uint32
		announced  bool
		wantErr    error
		wantReason string
	}{
		{
			name:       "duplicate_reference_inside_order",
			monsters:   []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			order:      []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestMonitorRef(0)},
			pathTypes:  []byte{0, 0},
			first:      firstDungeonMonsterObjectKey,
			wantReason: "order 里同一个 ref 出现两次：第二个下标会抢走第一个的 key",
		},
		{
			name:       "order_reference_without_owner",
			monsters:   []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			extended:   []*worldmap.HostileReference{nil},
			order:      []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestAPCRef(0)},
			pathTypes:  []byte{0, 5},
			first:      firstDungeonMonsterObjectKey,
			wantReason: "order 指向的 ref 在房间里没有任何 owner（非敌对 APC）",
		},
		{
			name:       "ambiguous_owner_for_one_reference",
			monsters:   []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestMonitorRef(0)},
			order:      []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			pathTypes:  []byte{0},
			first:      firstDungeonMonsterObjectKey,
			wantReason: "同一个 ref 两个 owner：两个 owner 会拿到同一个 key 并覆盖索引表",
		},
		{
			name:       "double_owner_masks_unowned_reference",
			monsters:   []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestMonitorRef(0)},
			extended:   []*worldmap.HostileReference{ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0))},
			order:      []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestAPCRef(1)},
			pathTypes:  []byte{0, 5},
			first:      firstDungeonMonsterObjectKey,
			wantReason: "重复 owner(+1) 与无主 ref 相抵：只数 matched 的旧实现会静默成功",
		},
		{
			name:       "order_length_mismatch",
			monsters:   []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			order:      []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			pathTypes:  []byte{0, 0},
			first:      firstDungeonMonsterObjectKey,
			wantReason: "order 长度与 tournament.PathActors 不等",
		},
		{
			name:       "empty_order",
			pathTypes:  nil,
			first:      firstDungeonMonsterObjectKey,
			wantReason: "没有路径 actor 时不得把房间绑定到空序列",
		},
		{
			name:       "zero_first_sequence",
			monsters:   []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			order:      []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			pathTypes:  []byte{0},
			first:      0,
			wantErr:    errDungeonExtendedActorObjectKeyRange,
			wantReason: "序号起点为 0（客户端把 0 当作未绑定）",
		},
		{
			name:       "sequence_overflow",
			monsters:   []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			extended:   []*worldmap.HostileReference{ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0))},
			order:      []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestAPCRef(0)},
			pathTypes:  []byte{0, 5},
			first:      65535,
			wantErr:    errDungeonExtendedActorObjectKeyRange,
			wantReason: "first+total 越过 65536（u16 公告空间上界）",
		},
		{
			name:       "already_announced_room",
			monsters:   []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
			extended:   []*worldmap.HostileReference{ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0))},
			order:      []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestAPCRef(0)},
			pathTypes:  []byte{0, 5},
			first:      firstDungeonMonsterObjectKey,
			announced:  true,
			wantErr:    errDungeonActorAlreadyAnnounced,
			wantReason: "房间已经向会话宣告过 actor，再改 key 会让客户端手上的 key 失效",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			room := tournamentOwnerTestBuildRoom(testCase.monsters, testCase.extended, firstDungeonMonsterObjectKey)
			if testCase.announced {
				room.announcedSessions = map[*worldmap.DungeonSession]struct{}{{}: {}}
			}
			tournament := tournamentOwnerTestTournament(testCase.pathTypes)
			scene := tournamentOwnerTestScene(len(testCase.monsters), len(testCase.extended), testCase.order)
			before := tournamentOwnerTestState(t, room)

			err := bindTournamentRoomPath(room, tournament, scene, testCase.first)
			if err == nil {
				t.Fatalf("bindTournamentRoomPath accepted an invalid binding; must be rejected because: %s",
					testCase.wantReason)
			}
			if testCase.wantErr != nil && !errors.Is(err, testCase.wantErr) {
				t.Fatalf("error=%v want %v", err, testCase.wantErr)
			}
			after := tournamentOwnerTestState(t, room)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected binding mutated the room;\nbefore=%+v\nafter =%+v", before, after)
			}
			if got := tournament.boundFirstSequence(); got != 0 {
				t.Fatalf("rejected binding bound the tournament first sequence=%d want 0", got)
			}
		})
	}
}

func TestTournamentOwnerBindTournamentRoomPathRejectsMissingRoom(t *testing.T) {
	tournament := tournamentOwnerTestTournament([]byte{0})
	scene := tournamentOwnerTestScene(1, 0, []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)})
	err := bindTournamentRoomPath(nil, tournament, scene, firstDungeonMonsterObjectKey)
	if err == nil {
		t.Fatal("bindTournamentRoomPath accepted a missing room")
	}
	if got := tournament.boundFirstSequence(); got != 0 {
		t.Fatalf("missing-room rejection bound the first sequence=%d want 0", got)
	}
}

func TestTournamentOwnerBindTournamentRoomPathNilTournamentIsNoOp(t *testing.T) {
	room := tournamentOwnerTestBuildRoom(
		[]worldmap.HostileReference{tournamentOwnerTestMonitorRef(0)},
		[]*worldmap.HostileReference{ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0))},
		firstDungeonMonsterObjectKey,
	)
	scene := tournamentOwnerTestScene(1, 1, []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestAPCRef(0)})
	before := tournamentOwnerTestState(t, room)
	if err := bindTournamentRoomPath(room, nil, scene, firstDungeonMonsterObjectKey); err != nil {
		t.Fatalf("nil tournament must be a no-op, got %v", err)
	}
	if after := tournamentOwnerTestState(t, room); !reflect.DeepEqual(before, after) {
		t.Fatalf("nil tournament mutated the room;\nbefore=%+v\nafter =%+v", before, after)
	}
}

// ── 接受侧：混合 monster/APC 轮次顺序 ─────────────────────────────────────

func TestTournamentOwnerBindTournamentRoomPathMixedMonsterAndAPCOrder(t *testing.T) {
	monsters := []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestMonitorRef(1)}
	extended := []*worldmap.HostileReference{
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0)),
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(1)),
	}
	order := []worldmap.HostileReference{
		tournamentOwnerTestMonitorRef(0),
		tournamentOwnerTestAPCRef(0),
		tournamentOwnerTestMonitorRef(1),
		tournamentOwnerTestAPCRef(1),
	}
	room := tournamentOwnerTestBuildRoom(monsters, extended, firstDungeonMonsterObjectKey)
	tournament := tournamentOwnerTestTournament([]byte{0, 5, 3, 8})
	scene := tournamentOwnerTestScene(len(monsters), len(extended), order)

	if err := bindTournamentRoomPath(room, tournament, scene, firstDungeonMonsterObjectKey); err != nil {
		t.Fatalf("mixed monster/APC path binding: %v", err)
	}
	tournamentOwnerTestAssertOwnerInvariants(t, room, tournament, firstDungeonMonsterObjectKey, order)

	// 顺序不被"按 kind 分组"：路径下标 0/2 是 monster，1/3 是 APC。
	for index, reference := range order {
		wantKind := worldmap.HostileMonster
		if index%2 == 1 {
			wantKind = worldmap.HostileAICharacter
		}
		if reference.Kind != wantKind {
			t.Fatalf("fixture drift: order[%d] kind=%s want=%s", index, reference.Kind, wantKind)
		}
		key := tournamentOwnerTestKeyForReference(t, room, reference)
		if got, wantRound := tournament.tournamentRoundForSequence(int(key)), index+1; got != wantRound {
			t.Fatalf("path actor %d key=%d maps to round %d want %d", index, key, got, wantRound)
		}
	}

	// 386 公告体（第二段）逐槽写 first+i：路径顺序改了 owner key，
	// 公告序号必须落在同一批 key 上，否则客户端上报的 actor id 对不上房间 owner。
	infoBody, err := buildTournamentInfoBody(tournament, 0, tournament.boundFirstSequence())
	if err != nil {
		t.Fatalf("build tournament info body: %v", err)
	}
	pathActors := tournament.snapshot().PathActors
	section := tournamentOwnerTestFindPathSection(infoBody, pathActors, tournament.boundFirstSequence(), tournament.Definition.PartyLimit)
	t.Logf("386 path actor section offset=%d body_len=%d path_actors=%d",
		section, len(infoBody), len(pathActors))
	if section < 0 {
		t.Fatalf("info body has no path actor section matching first+i and the path definitions (body=%x)", infoBody)
	}
	if section+9*len(order) != len(infoBody) {
		t.Fatalf("path actor section at %d with %d actors does not end at the body end %d",
			section, len(order), len(infoBody))
	}
	for index, reference := range order {
		// 每条记录 = 轮号(1) + 序号(2) + Code(4) + Level(1) + Type(1)。
		at := section + index*9 + 1
		want := uint16(tournamentOwnerTestKeyForReference(t, room, reference))
		if got := binary.LittleEndian.Uint16(infoBody[at : at+2]); got != want {
			t.Fatalf("info body path actor %d sequence=%d want room owner key=%d", index, got, want)
		}
	}

	// 死亡上报按路径 key 推进：全灭 4 轮 ⇒ Champion，且每轮各推进一次。
	for index, reference := range order {
		transition := tournament.tryApplyActorDeath(int(tournamentOwnerTestKeyForReference(t, room, reference)))
		if !transition.accepted() {
			t.Fatalf("death for path actor %d rejected: %+v", index, transition)
		}
		if index < len(order)-1 {
			if !transition.roundCompleted() || transition.tournamentCompleted() {
				t.Fatalf("path actor %d transition=%+v want round completed", index, transition)
			}
			continue
		}
		if !transition.tournamentCompleted() {
			t.Fatalf("last path actor transition=%+v want tournament completed", transition)
		}
	}
	if !tournament.isChampion() {
		t.Fatal("tournament did not reach champion after all path actors died")
	}
}

// ── 接受侧：全 APC 路径 type 5/8 ──────────────────────────────────────────

func TestTournamentOwnerBindTournamentRoomPathAllAPCTypes5And8(t *testing.T) {
	extended := []*worldmap.HostileReference{
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0)),
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(1)),
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(2)),
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(3)),
	}
	order := []worldmap.HostileReference{
		tournamentOwnerTestAPCRef(0),
		tournamentOwnerTestAPCRef(1),
		tournamentOwnerTestAPCRef(2),
		tournamentOwnerTestAPCRef(3),
	}
	room := tournamentOwnerTestBuildRoom(nil, extended, firstDungeonMonsterObjectKey)
	tournament := tournamentOwnerTestTournament([]byte{5, 5, 5, 8})
	scene := tournamentOwnerTestScene(0, len(extended), order)

	if err := bindTournamentRoomPath(room, tournament, scene, firstDungeonMonsterObjectKey); err != nil {
		t.Fatalf("all-APC path binding: %v", err)
	}
	tournamentOwnerTestAssertOwnerInvariants(t, room, tournament, firstDungeonMonsterObjectKey, order)
	snapshot := room.Snapshot()
	if len(snapshot.Monsters) != 0 {
		t.Fatalf("all-APC path produced %d ordinary monsters; want 0", len(snapshot.Monsters))
	}
	if len(snapshot.ExtendedActors) != len(order) {
		t.Fatalf("all-APC path extended actors=%d want %d", len(snapshot.ExtendedActors), len(order))
	}
	for index, actor := range snapshot.ExtendedActors {
		if actor.Kind != runtimeDungeonActorAICharacter {
			t.Fatalf("path actor %d kind=%s want %s", index, actor.Kind, runtimeDungeonActorAICharacter)
		}
		if actor.HostileReference == nil {
			t.Fatalf("path actor %d has no hostile reference", index)
		}
		if want := firstDungeonMonsterObjectKey + uint32(index); actor.ObjectKey != want {
			t.Fatalf("APC %d owner key=%d want %d", index, actor.ObjectKey, want)
		}
		if actor.Packet.ObjectKey != uint16(actor.ObjectKey) {
			t.Fatalf("APC %d packet object key=%d want owner key=%d",
				index, actor.Packet.ObjectKey, actor.ObjectKey)
		}
		if got, wantRound := tournament.tournamentRoundForSequence(int(actor.ObjectKey)), index+1; got != wantRound {
			t.Fatalf("APC %d key=%d maps to round %d want %d", index, actor.ObjectKey, got, wantRound)
		}
	}

	// defeated 快照 ⇒ 按 key 升序的路径键序（状态机按轮消费）。
	tournament2 := tournamentOwnerTestTournament([]byte{5, 5, 5, 8})
	room2 := tournamentOwnerTestBuildRoom(nil, extended, firstDungeonMonsterObjectKey)
	if err := bindTournamentRoomPath(room2, tournament2, scene, firstDungeonMonsterObjectKey); err != nil {
		t.Fatal(err)
	}
	room2.mu.Lock()
	for index := range room2.extendedActors {
		room2.extendedActors[index].State = runtimeDungeonMonsterDefeated
	}
	room2.mu.Unlock()
	keys := tournamentDefeatedObjectKeys(room2.Snapshot())
	if len(keys) != len(order) {
		t.Fatalf("defeated tournament object keys=%v want %d entries", keys, len(order))
	}
	for index, key := range keys {
		if want := firstDungeonMonsterObjectKey + uint32(index); key != want {
			t.Fatalf("defeated key[%d]=%d want %d (ascending path order)", index, key, want)
		}
	}
}

// ── 接受侧：扩展 actor 的 Packet.ObjectKey 与 owner 同步 ──────────────────

func TestTournamentOwnerBindTournamentExtendedActorPacketObjectKeyFollowsPathOrder(t *testing.T) {
	monsters := []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestMonitorRef(1)}
	extended := []*worldmap.HostileReference{
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0)),
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(1)),
	}
	// 交错顺序：APC 抢到路径第 1/3 位，普通怪退到第 2/4 位。
	order := []worldmap.HostileReference{
		tournamentOwnerTestAPCRef(0),
		tournamentOwnerTestMonitorRef(0),
		tournamentOwnerTestAPCRef(1),
		tournamentOwnerTestMonitorRef(1),
	}
	room := tournamentOwnerTestBuildRoom(monsters, extended, firstDungeonMonsterObjectKey)
	before := room.Snapshot()
	tournament := tournamentOwnerTestTournament([]byte{5, 0, 5, 0})
	scene := tournamentOwnerTestScene(len(monsters), len(extended), order)

	if err := bindTournamentRoomPath(room, tournament, scene, firstDungeonMonsterObjectKey); err != nil {
		t.Fatalf("interleaved APC/monster binding: %v", err)
	}
	tournamentOwnerTestAssertOwnerInvariants(t, room, tournament, firstDungeonMonsterObjectKey, order)
	after := room.Snapshot()

	// 行身份（PacketIndex/Code/Type/Level）不得因为改 key 而变；只有 ObjectKey 同步改。
	for index := range after.ExtendedActors {
		if after.ExtendedActors[index].Packet.ObjectKey != uint16(after.ExtendedActors[index].ObjectKey) {
			t.Fatalf("extended actor %d packet key=%d owner key=%d out of sync",
				index, after.ExtendedActors[index].Packet.ObjectKey, after.ExtendedActors[index].ObjectKey)
		}
		if after.ExtendedActors[index].Packet.Code != before.ExtendedActors[index].Packet.Code ||
			after.ExtendedActors[index].Packet.PacketIndex != before.ExtendedActors[index].Packet.PacketIndex ||
			after.ExtendedActors[index].Packet.Type != before.ExtendedActors[index].Packet.Type ||
			after.ExtendedActors[index].Packet.Level != before.ExtendedActors[index].Packet.Level {
			t.Fatalf("extended actor %d row identity changed: before=%+v after=%+v",
				index, before.ExtendedActors[index].Packet, after.ExtendedActors[index].Packet)
		}
	}
	// 顺序确实被打乱（否则这条用例证明不了"同步"）。
	if after.ExtendedActors[0].ObjectKey == before.ExtendedActors[0].ObjectKey {
		t.Fatalf("fixture did not permute APC 0: before=%d after=%d",
			before.ExtendedActors[0].ObjectKey, after.ExtendedActors[0].ObjectKey)
	}
	if got, want := after.ExtendedActors[0].ObjectKey, firstDungeonMonsterObjectKey; got != want {
		t.Fatalf("APC 0 key=%d want %d", got, want)
	}
	if got, want := after.ExtendedActors[1].ObjectKey, firstDungeonMonsterObjectKey+2; got != want {
		t.Fatalf("APC 1 key=%d want %d", got, want)
	}
	// 普通怪的 Reference（owner 身份）不得被跨 kind 覆盖。
	for index, monster := range after.Monsters {
		if monster.Reference != before.Monsters[index].Reference {
			t.Fatalf("monster %d reference changed: before=%s/%d after=%s/%d", index,
				before.Monsters[index].Reference.Kind, before.Monsters[index].Reference.Index,
				monster.Reference.Kind, monster.Reference.Index)
		}
	}
}

// ── 接受侧：非 path actor 不与路径序号空间冲突 ─────────────────────────────

func TestTournamentOwnerBindTournamentRoomPathNonPathActorsStayOutsidePathSequence(t *testing.T) {
	monsters := []worldmap.HostileReference{
		tournamentOwnerTestMonitorRef(0),
		tournamentOwnerTestMonitorRef(1),
		tournamentOwnerTestMonitorRef(7), // 非路径：order 不引用
	}
	extended := []*worldmap.HostileReference{
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0)),
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(1)),
		nil, // 非路径：非敌对扩展 actor
	}
	order := []worldmap.HostileReference{
		tournamentOwnerTestMonitorRef(0),
		tournamentOwnerTestAPCRef(0),
		tournamentOwnerTestMonitorRef(1),
		tournamentOwnerTestAPCRef(1),
	}
	room := tournamentOwnerTestBuildRoom(monsters, extended, firstDungeonMonsterObjectKey)
	tournament := tournamentOwnerTestTournament([]byte{0, 5, 3, 8})
	scene := tournamentOwnerTestScene(len(monsters), len(extended), order)

	if err := bindTournamentRoomPath(room, tournament, scene, firstDungeonMonsterObjectKey); err != nil {
		t.Fatalf("binding with non-path actors: %v", err)
	}
	tournamentOwnerTestAssertOwnerInvariants(t, room, tournament, firstDungeonMonsterObjectKey, order)

	pathBlockEnd := firstDungeonMonsterObjectKey + uint32(len(order))
	snapshot := room.Snapshot()
	nonPath := 0
	for _, monster := range snapshot.Monsters {
		isPath := false
		for _, reference := range order {
			if monster.Reference == reference {
				isPath = true
				break
			}
		}
		if isPath {
			continue
		}
		nonPath++
		if monster.ObjectKey < pathBlockEnd {
			t.Fatalf("non-path monster key=%d falls inside the path block [%d,%d)",
				monster.ObjectKey, firstDungeonMonsterObjectKey, pathBlockEnd)
		}
		if round := tournament.tournamentRoundForSequence(int(monster.ObjectKey)); round != 0 {
			t.Fatalf("non-path monster key=%d maps to tournament round %d want 0", monster.ObjectKey, round)
		}
		if transition := tournament.tryApplyActorDeath(int(monster.ObjectKey)); transition.accepted() {
			t.Fatalf("non-path monster key=%d was accepted as a tournament death: %+v",
				monster.ObjectKey, transition)
		}
	}
	for _, actor := range snapshot.ExtendedActors {
		if actor.HostileReference != nil {
			continue
		}
		nonPath++
		if actor.ObjectKey < pathBlockEnd {
			t.Fatalf("non-path extended actor key=%d falls inside the path block [%d,%d)",
				actor.ObjectKey, firstDungeonMonsterObjectKey, pathBlockEnd)
		}
	}
	if nonPath != 2 {
		t.Fatalf("non-path actor count=%d want 2", nonPath)
	}
	if got, want := room.highObjectKey(), firstDungeonMonsterObjectKey+6; got != want {
		t.Fatalf("high object key=%d want %d", got, want)
	}
}

// ── 接受侧：序号上界（first+total == 65536，最大 key 0xFFFF）────────────────

func TestTournamentOwnerBindTournamentRoomPathAcceptsMaximumSequenceBlock(t *testing.T) {
	monsters := []worldmap.HostileReference{tournamentOwnerTestMonitorRef(0), tournamentOwnerTestMonitorRef(1)}
	extended := []*worldmap.HostileReference{
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(0)),
		ptrTournamentOwnerRef(tournamentOwnerTestAPCRef(1)),
	}
	order := []worldmap.HostileReference{
		tournamentOwnerTestMonitorRef(0),
		tournamentOwnerTestAPCRef(0),
		tournamentOwnerTestMonitorRef(1),
		tournamentOwnerTestAPCRef(1),
	}
	total := uint32(len(monsters) + len(extended))
	first := uint32(65536) - total
	room := tournamentOwnerTestBuildRoom(monsters, extended, firstDungeonMonsterObjectKey)
	tournament := tournamentOwnerTestTournament([]byte{0, 5, 3, 8})
	scene := tournamentOwnerTestScene(len(monsters), len(extended), order)

	if err := bindTournamentRoomPath(room, tournament, scene, first); err != nil {
		t.Fatalf("binding at the u16 upper boundary (first=%d total=%d): %v", first, total, err)
	}
	tournamentOwnerTestAssertOwnerInvariants(t, room, tournament, first, order)
	snapshot := room.Snapshot()
	last := snapshot.ExtendedActors[len(snapshot.ExtendedActors)-1]
	if last.ObjectKey != 0xFFFF || last.Packet.ObjectKey != 0xFFFF {
		t.Fatalf("last actor owner key=%d packet key=%d want 0xFFFF for both",
			last.ObjectKey, last.Packet.ObjectKey)
	}
	if got := tournament.boundFirstSequence(); got != uint16(first) {
		t.Fatalf("bound first sequence=%d want %d", got, first)
	}

	// 同一房间、同一 first、新的赛制句柄再绑一次：房间侧必须逐字段幂等。
	other := tournamentOwnerTestTournament([]byte{0, 5, 3, 8})
	before := tournamentOwnerTestState(t, room)
	if err := bindTournamentRoomPath(room, other, scene, first); err != nil {
		t.Fatalf("re-binding an unannounced room with the same first: %v", err)
	}
	if after := tournamentOwnerTestState(t, room); !reflect.DeepEqual(before, after) {
		t.Fatalf("re-binding the same room mutated the binding;\nbefore=%+v\nafter =%+v", before, after)
	}
	if got := other.boundFirstSequence(); got != uint16(first) {
		t.Fatalf("re-bind did not bind the new handle's first sequence: got %d want %d", got, first)
	}
}
