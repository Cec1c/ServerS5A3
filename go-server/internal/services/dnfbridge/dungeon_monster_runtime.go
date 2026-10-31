package dnfbridge

import (
	"errors"
	"fmt"
	"sync"

	dnfmonster "longheng.io/server/internal/modules/dnf/monster"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var (
	errDungeonMonsterIDInvalid        = errors.New("dnf dungeon monster id is invalid")
	errDungeonMonsterDefinitionMiss   = errors.New("dnf dungeon monster definition is missing")
	errDungeonMonsterObjectKeyRange   = errors.New("dnf dungeon monster object key exceeds current EXE range")
	errDungeonMonsterObjectNotFound   = errors.New("dnf dungeon monster runtime object is not found")
	errDungeonMonsterAlreadyAnnounced = errors.New("dnf dungeon monster was already announced")
	errDungeonMonsterNotAnnounced     = errors.New("dnf dungeon monster was not announced to the client")
	errDungeonMonsterAlreadyDefeated  = errors.New("dnf dungeon monster was already defeated")
	errDungeonActorAlreadyAnnounced   = errors.New("dnf dungeon actor was already announced")
	errDungeonActorOwnerConflict      = errors.New("dnf dungeon actor owner conflicts with the room session")
	errDungeonActorNotHostile         = errors.New("dnf dungeon actor is not a hostile room owner")
	errDungeonNonHostileRetirement    = errors.New("dnf non-hostile AI actor retirement is not eligible")
)

const firstDungeonMonsterObjectKey = uint32(currentSceneBootstrapObjectKey) + 1

type runtimeDungeonMonsterState string

const (
	runtimeDungeonMonsterPlanned   runtimeDungeonMonsterState = "planned"
	runtimeDungeonMonsterAnnounced runtimeDungeonMonsterState = "announced"
	runtimeDungeonMonsterDefeated  runtimeDungeonMonsterState = "defeated"
)

type runtimeDungeonMonster struct {
	ObjectKey         uint32
	Reference         worldmap.HostileReference
	Spawn             worldmap.MonsterSpawn
	Definition        dnfmonster.Monster
	State             runtimeDungeonMonsterState
	RoomClearSkipByte byte
}

type runtimeDungeonRoomSnapshot struct {
	Coordinate            worldmap.RoomCoordinate
	MapID                 int64
	Monsters              []runtimeDungeonMonster
	ExtendedActors        []runtimeDungeonExtendedActor
	RetainedSpecialSpawns []runtimeDungeonRetainedSpecialSpawn
	Diagnostics           []string
	OpaqueHostiles        []worldmap.HostileReference
	SpecialObjectCount    int
	AICharacterCount      int
}

type runtimeDungeonRoom struct {
	mu                    sync.RWMutex
	deathCommitMu         sync.Mutex
	materialID            string
	materialRecipients    map[uint32][]questMaterialRecipient
	cardGoldKills         currentDungeonCardKillCounts
	combatDeaths          map[uint32]struct{}
	coordinate            worldmap.RoomCoordinate
	mapID                 int64
	monsters              []runtimeDungeonMonster
	byObjectKey           map[uint32]int
	opaqueHostiles        []worldmap.HostileReference
	extendedActors        []runtimeDungeonExtendedActor
	extendedByObjectKey   map[uint32]int
	retainedSpecialSpawns []runtimeDungeonRetainedSpecialSpawn
	diagnostics           []string
	specialObjectCount    int
	aiCharacterCount      int
	announcedSessions     map[*worldmap.DungeonSession]struct{}
	deathBodies           map[uint32][]byte
	guildPractice         *currentGuildPracticeRoom
}

func newRuntimeDungeonRoom(
	scene worldmap.DungeonRoomScene,
	catalog *pvfDungeonMonsterCatalog,
	firstObjectKey uint32,
) (*runtimeDungeonRoom, uint32, error) {
	if catalog == nil {
		return nil, firstObjectKey, errDungeonMonsterCatalogUnavailable
	}
	room := &runtimeDungeonRoom{
		coordinate:  scene.Coordinate,
		mapID:       scene.Map.Map.ID,
		monsters:    make([]runtimeDungeonMonster, 0, len(scene.Monsters)),
		byObjectKey: make(map[uint32]int, len(scene.Monsters)),
	}
	nextObjectKey := firstObjectKey
	for index, spawn := range scene.Monsters {
		if spawn.MonsterID <= 0 {
			return nil, firstObjectKey, fmt.Errorf(
				"%w: map=%d room=%s index=%d id=%d",
				errDungeonMonsterIDInvalid, room.mapID, room.coordinate, index, spawn.MonsterID,
			)
		}
		definition, ok, findErr := catalog.Find(spawn.MonsterID)
		if findErr != nil {
			return nil, firstObjectKey, fmt.Errorf(
				"resolve monster definition: map=%d room=%s index=%d id=%d: %w",
				room.mapID, room.coordinate, index, spawn.MonsterID, findErr,
			)
		}
		if !ok {
			return nil, firstObjectKey, fmt.Errorf(
				"%w: map=%d room=%s index=%d id=%d",
				errDungeonMonsterDefinitionMiss, room.mapID, room.coordinate, index, spawn.MonsterID,
			)
		}
		if nextObjectKey == 0 || nextObjectKey > uint32(^uint16(0)) {
			return nil, firstObjectKey, fmt.Errorf(
				"%w: map=%d room=%s index=%d object_key=%d",
				errDungeonMonsterObjectKeyRange, room.mapID, room.coordinate, index, nextObjectKey,
			)
		}
		reference := worldmap.HostileReference{Kind: worldmap.HostileMonster, Index: index}
		room.byObjectKey[nextObjectKey] = len(room.monsters)
		room.monsters = append(room.monsters, runtimeDungeonMonster{
			ObjectKey:  nextObjectKey,
			Reference:  reference,
			Spawn:      spawn,
			Definition: definition,
			State:      runtimeDungeonMonsterPlanned,
		})
		nextObjectKey++
	}
	for _, reference := range scene.ExpectedHostiles {
		if reference.Kind != worldmap.HostileMonster {
			room.opaqueHostiles = append(room.opaqueHostiles, reference)
		}
	}
	return room, nextObjectKey, nil
}

func (r *runtimeDungeonRoom) MonsterCount() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.monsters)
}

func (r *runtimeDungeonRoom) OpaqueHostileCount() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.opaqueHostiles)
}

func (r *runtimeDungeonRoom) MarkMonsterNonBlocking(monsterIndex int) (runtimeDungeonMonster, error) {
	if r == nil {
		return runtimeDungeonMonster{}, errDungeonMonsterObjectNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if monsterIndex < 0 || monsterIndex >= len(r.monsters) {
		return runtimeDungeonMonster{}, fmt.Errorf("%w: monster_index=%d", errDungeonMonsterObjectNotFound, monsterIndex)
	}
	r.monsters[monsterIndex].RoomClearSkipByte = 1
	return r.monsters[monsterIndex], nil
}

func (r *runtimeDungeonRoom) NonBlockingMonsterReferences() []worldmap.HostileReference {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]worldmap.HostileReference, 0)
	for _, monster := range r.monsters {
		if monster.RoomClearSkipByte != 0 {
			result = append(result, monster.Reference)
		}
	}
	return result
}

func (r *runtimeDungeonRoom) Snapshot() runtimeDungeonRoomSnapshot {
	if r == nil {
		return runtimeDungeonRoomSnapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return runtimeDungeonRoomSnapshot{
		Coordinate:            r.coordinate,
		MapID:                 r.mapID,
		Monsters:              cloneRuntimeDungeonMonsters(r.monsters),
		ExtendedActors:        cloneRuntimeDungeonExtendedActors(r.extendedActors),
		RetainedSpecialSpawns: cloneRuntimeDungeonRetainedSpecialSpawns(r.retainedSpecialSpawns),
		Diagnostics:           append([]string(nil), r.diagnostics...),
		OpaqueHostiles:        append([]worldmap.HostileReference(nil), r.opaqueHostiles...),
		SpecialObjectCount:    r.specialObjectCount,
		AICharacterCount:      r.aiCharacterCount,
	}
}

func (r *runtimeDungeonRoom) AttachExtendedActors(plan runtimeDungeonExtendedActorPlan) error {
	if r == nil {
		return errDungeonWorldMapUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	extendedByObjectKey := make(map[uint32]int, len(plan.Actors))
	handled := make(map[worldmap.HostileReference]struct{})
	for index, actor := range plan.Actors {
		if _, exists := r.byObjectKey[actor.ObjectKey]; exists {
			return fmt.Errorf("%w: object_key=%d normal_and_extended", errDungeonActorOwnerConflict, actor.ObjectKey)
		}
		if _, exists := extendedByObjectKey[actor.ObjectKey]; exists {
			return fmt.Errorf("%w: object_key=%d duplicate_extended", errDungeonActorOwnerConflict, actor.ObjectKey)
		}
		extendedByObjectKey[actor.ObjectKey] = index
		if actor.HostileReference != nil {
			handled[*actor.HostileReference] = struct{}{}
		}
	}
	remaining := make([]worldmap.HostileReference, 0, len(r.opaqueHostiles))
	for _, reference := range r.opaqueHostiles {
		if _, ok := handled[reference]; !ok {
			remaining = append(remaining, reference)
		}
	}
	r.extendedActors = cloneRuntimeDungeonExtendedActors(plan.Actors)
	r.extendedByObjectKey = extendedByObjectKey
	r.retainedSpecialSpawns = cloneRuntimeDungeonRetainedSpecialSpawns(plan.RetainedSpecialSpawns)
	r.diagnostics = append([]string(nil), plan.Diagnostics...)
	r.specialObjectCount = plan.SpecialObjectCount
	r.aiCharacterCount = plan.AICharacterCount
	r.opaqueHostiles = remaining
	return nil
}

// AnnounceAllActors 把房间里的 actor 绑定进**这个会话自己的 scene**（每个客户端都要绑一次），
// 并把房间里的怪从 planned 推进到 announced。
//
// "宣告"是**会话维度**的事实，而队伍级共享实例（dungeon_party_instance.go）下两个会话共用
// 同一个房间对象，所以重复宣告的判据不能只看 monster.State（那是共享数据：队友宣告过 /
// 打死过都会体现在里面）——否则第二个成员进图时会看到 state=announced 而被误判成
// "重复宣告"（阶段二回归：commit current start-map runtime actors after op29 send 失败）。
// 这里改为按会话登记：同一会话重复宣告仍然报 errDungeonActorAlreadyAnnounced，
// 不同会话各宣告一次；已经不存在的怪（defeated）不再绑定，也不会被改回 announced。
func (r *runtimeDungeonRoom) AnnounceAllActors(session *worldmap.DungeonSession) (int, error) {
	if r == nil {
		return 0, errDungeonMonsterObjectNotFound
	}
	if session == nil {
		return 0, worldmap.ErrDungeonRunRequired
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.announcedSessions == nil {
		r.announcedSessions = make(map[*worldmap.DungeonSession]struct{}, 2)
	}
	if _, already := r.announcedSessions[session]; already {
		return 0, fmt.Errorf("%w: session_already_announced", errDungeonActorAlreadyAnnounced)
	}

	type binding struct {
		reference worldmap.HostileReference
		objectKey uint32
	}
	bindings := make([]binding, 0, len(r.monsters)+len(r.extendedActors))
	announced := 0
	for _, monster := range r.monsters {
		if monster.State == runtimeDungeonMonsterDefeated {
			continue
		}
		announced++
		bindings = append(bindings, binding{reference: monster.Reference, objectKey: monster.ObjectKey})
	}
	for _, actor := range r.extendedActors {
		if actor.State == runtimeDungeonMonsterDefeated {
			continue
		}
		announced++
		if actor.HostileReference != nil {
			bindings = append(bindings, binding{reference: *actor.HostileReference, objectKey: actor.ObjectKey})
		}
	}

	scene, ok := session.Scene()
	if !ok {
		return 0, worldmap.ErrDungeonRunRequired
	}
	seenReferences := make(map[worldmap.HostileReference]struct{}, len(bindings))
	seenObjectKeys := make(map[uint32]struct{}, len(bindings))
	for _, candidate := range bindings {
		if !runtimeSceneContainsHostile(scene.ExpectedHostiles, candidate.reference) {
			return 0, fmt.Errorf("%w: kind=%s index=%d object_key=%d reference_missing",
				errDungeonActorOwnerConflict,
				candidate.reference.Kind,
				candidate.reference.Index,
				candidate.objectKey,
			)
		}
		if _, exists := seenReferences[candidate.reference]; exists {
			return 0, fmt.Errorf("%w: kind=%s index=%d duplicate_reference",
				errDungeonActorOwnerConflict,
				candidate.reference.Kind,
				candidate.reference.Index,
			)
		}
		if _, exists := seenObjectKeys[candidate.objectKey]; exists {
			return 0, fmt.Errorf("%w: object_key=%d duplicate_key", errDungeonActorOwnerConflict, candidate.objectKey)
		}
		if previous, exists := scene.RuntimeObjects[candidate.objectKey]; exists {
			return 0, fmt.Errorf("%w: object_key=%d existing_kind=%s existing_index=%d",
				errDungeonActorOwnerConflict,
				candidate.objectKey,
				previous.Kind,
				previous.Index,
			)
		}
		for objectKey, previous := range scene.RuntimeObjects {
			if previous == candidate.reference {
				return 0, fmt.Errorf("%w: kind=%s index=%d existing_object_key=%d",
					errDungeonActorOwnerConflict,
					candidate.reference.Kind,
					candidate.reference.Index,
					objectKey,
				)
			}
		}
		seenReferences[candidate.reference] = struct{}{}
		seenObjectKeys[candidate.objectKey] = struct{}{}
	}

	for _, candidate := range bindings {
		if err := session.BindHostileObject(candidate.reference, candidate.objectKey); err != nil {
			return 0, fmt.Errorf("bind announced dungeon actor: %w", err)
		}
	}
	for index := range r.monsters {
		if r.monsters[index].State == runtimeDungeonMonsterPlanned {
			r.monsters[index].State = runtimeDungeonMonsterAnnounced
		}
	}
	for index := range r.extendedActors {
		if r.extendedActors[index].State == runtimeDungeonMonsterPlanned {
			r.extendedActors[index].State = runtimeDungeonMonsterAnnounced
		}
	}
	r.announcedSessions[session] = struct{}{}
	return announced, nil
}

// adoptAnnouncedSessionsFrom 把另一份房间对象上"已经宣告过"的会话登记并进来。
// 共享实例下本地房间对象可能被队友那份替换（shareOrRegisterDungeonRoom），
// 本会话已经宣告过的事实不能因此丢掉，否则同一间房会被重复绑定而报错。
func (r *runtimeDungeonRoom) adoptAnnouncedSessionsFrom(other *runtimeDungeonRoom) {
	if r == nil || other == nil || r == other {
		return
	}
	other.mu.RLock()
	pending := make([]*worldmap.DungeonSession, 0, len(other.announcedSessions))
	for session := range other.announcedSessions {
		pending = append(pending, session)
	}
	other.mu.RUnlock()
	if len(pending) == 0 {
		return
	}
	r.mu.Lock()
	if r.announcedSessions == nil {
		r.announcedSessions = make(map[*worldmap.DungeonSession]struct{}, len(pending))
	}
	for _, session := range pending {
		r.announcedSessions[session] = struct{}{}
	}
	r.mu.Unlock()
}

func runtimeSceneContainsHostile(values []worldmap.HostileReference, want worldmap.HostileReference) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (r *runtimeDungeonRoom) AnnounceMonster(objectKey uint32, session *worldmap.DungeonSession) error {
	if r == nil {
		return errDungeonMonsterObjectNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index, ok := r.byObjectKey[objectKey]
	if !ok {
		return fmt.Errorf("%w: object_key=%d", errDungeonMonsterObjectNotFound, objectKey)
	}
	monster := &r.monsters[index]
	if monster.State != runtimeDungeonMonsterPlanned {
		return fmt.Errorf("%w: object_key=%d state=%s", errDungeonMonsterAlreadyAnnounced, objectKey, monster.State)
	}
	if session == nil {
		return worldmap.ErrDungeonRunRequired
	}
	if err := session.BindHostileObject(monster.Reference, objectKey); err != nil {
		return err
	}
	monster.State = runtimeDungeonMonsterAnnounced
	return nil
}

func (r *runtimeDungeonRoom) ContainsAnnouncedActorObjectKey(objectKey uint32) bool {
	if r == nil || objectKey == 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if index, ok := r.byObjectKey[objectKey]; ok {
		return r.monsters[index].State != runtimeDungeonMonsterPlanned
	}
	if index, ok := r.extendedByObjectKey[objectKey]; ok {
		return r.extendedActors[index].State != runtimeDungeonMonsterPlanned
	}
	return false
}

func (r *runtimeDungeonRoom) ContainsAnnouncedNonHostileAICharacterObjectKey(objectKey uint32) bool {
	if r == nil || objectKey == 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	index, ok := r.extendedByObjectKey[objectKey]
	if !ok {
		return false
	}
	actor := r.extendedActors[index]
	return actor.Kind == runtimeDungeonActorAICharacter &&
		actor.HostileReference == nil &&
		actor.State == runtimeDungeonMonsterAnnounced
}

// AnnouncedMonster resolves only an ordinary PVF [monster] owned by this room
// and only while it is in the announced state. Extended actors, planned
// monsters, defeated monsters, and stale keys are intentionally excluded.
func (r *runtimeDungeonRoom) AnnouncedMonster(objectKey uint32) (runtimeDungeonMonster, bool) {
	if r == nil || objectKey == 0 {
		return runtimeDungeonMonster{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	index, ok := r.byObjectKey[objectKey]
	if !ok || r.monsters[index].State != runtimeDungeonMonsterAnnounced {
		return runtimeDungeonMonster{}, false
	}
	return r.monsters[index], true
}

// MonsterByReference resolves the PVF monster definition behind a room
// HostileReference regardless of whether the live actor object is the ordinary
// monster row or a current-EXE extended actor bound to the same reference. 86JP
// performs kill reward/quest checks from the logical monster/enemy code after a
// death event; callers still own the actor state transition before using this
// helper for EXP, drops, or quest trigger inputs.
func (r *runtimeDungeonRoom) MonsterByReference(
	reference worldmap.HostileReference,
	allowedStates ...runtimeDungeonMonsterState,
) (runtimeDungeonMonster, bool) {
	if r == nil || reference.Kind != worldmap.HostileMonster || reference.Index < 0 {
		return runtimeDungeonMonster{}, false
	}
	allowed := make(map[runtimeDungeonMonsterState]struct{}, len(allowedStates))
	for _, state := range allowedStates {
		allowed[state] = struct{}{}
	}
	stateAllowed := func(state runtimeDungeonMonsterState) bool {
		if len(allowed) == 0 {
			return true
		}
		_, ok := allowed[state]
		return ok
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, monster := range r.monsters {
		if monster.Reference == reference && stateAllowed(monster.State) {
			return monster, true
		}
	}
	return runtimeDungeonMonster{}, false
}

// runtimeDungeonRewardEnemy is the authoritative enemy type/code supplied to
// quest [enemy reward item] matching. AICharacter.Code is the business identity;
// Packet.Code is only a client-projection mirror.
type runtimeDungeonRewardEnemy struct {
	EnemyType int64
	EnemyCode int64
}

// RewardEnemyByReference resolves the quest-drop identity behind a room-owned
// hostile reference. Ordinary monsters stay type 1. Hostile AI characters stay
// type 2 and are resolved only through their runtime AICharacter record.
func (r *runtimeDungeonRoom) RewardEnemyByReference(
	reference worldmap.HostileReference,
	allowedStates ...runtimeDungeonMonsterState,
) (runtimeDungeonRewardEnemy, bool) {
	if r == nil || reference.Index < 0 {
		return runtimeDungeonRewardEnemy{}, false
	}
	allowed := make(map[runtimeDungeonMonsterState]struct{}, len(allowedStates))
	for _, state := range allowedStates {
		allowed[state] = struct{}{}
	}
	stateAllowed := func(state runtimeDungeonMonsterState) bool {
		if len(allowed) == 0 {
			return true
		}
		_, ok := allowed[state]
		return ok
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	switch reference.Kind {
	case worldmap.HostileMonster:
		for _, monster := range r.monsters {
			if monster.Reference == reference && stateAllowed(monster.State) {
				return runtimeDungeonRewardEnemy{
					EnemyType: 1,
					EnemyCode: monster.Spawn.MonsterID,
				}, true
			}
		}
	case worldmap.HostileAICharacter:
		for _, actor := range r.extendedActors {
			if actor.Kind != runtimeDungeonActorAICharacter ||
				actor.HostileReference == nil ||
				*actor.HostileReference != reference ||
				!stateAllowed(actor.State) {
				continue
			}
			if actor.AICharacter == nil || actor.AICharacter.Code <= 0 {
				return runtimeDungeonRewardEnemy{}, false
			}
			return runtimeDungeonRewardEnemy{
				EnemyType: 2,
				EnemyCode: actor.AICharacter.Code,
			}, true
		}
	}
	return runtimeDungeonRewardEnemy{}, false
}

type runtimeDungeonDeathTarget struct {
	ObjectKey    uint32
	Reference    worldmap.HostileReference
	ResponseKind currentDungeonDeathResponseKind
}

func (r *runtimeDungeonRoom) CommitActorDeathReport(
	objectKey uint32,
	session *worldmap.DungeonSession,
) (runtimeDungeonDeathTarget, bool, error) {
	return r.commitActorDeathReport(objectKey, session, true)
}

func (r *runtimeDungeonRoom) commitActorDeathReport(
	objectKey uint32,
	session *worldmap.DungeonSession,
	recordCombatKill bool,
) (runtimeDungeonDeathTarget, bool, error) {
	return r.commitActorDeathReportPrepared(objectKey, session, recordCombatKill, nil)
}

func (r *runtimeDungeonRoom) commitActorDeathReportPrepared(objectKey uint32, session *worldmap.DungeonSession, recordCombatKill bool, prepare func() error) (runtimeDungeonDeathTarget, bool, error) {
	if r == nil {
		return runtimeDungeonDeathTarget{}, false, errDungeonMonsterObjectNotFound
	}
	r.deathCommitMu.Lock()
	defer r.deathCommitMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	target, state, err := r.deathTargetLocked(objectKey)
	if err != nil {
		return runtimeDungeonDeathTarget{}, false, err
	}
	switch *state {
	case runtimeDungeonMonsterAnnounced:
	case runtimeDungeonMonsterDefeated:
		return runtimeDungeonDeathTarget{}, false, fmt.Errorf("%w: object_key=%d", errDungeonMonsterAlreadyDefeated, objectKey)
	default:
		return runtimeDungeonDeathTarget{}, false, fmt.Errorf("%w: object_key=%d state=%s", errDungeonMonsterNotAnnounced, objectKey, *state)
	}
	if session == nil {
		return runtimeDungeonDeathTarget{}, false, worldmap.ErrDungeonRunRequired
	}
	scene, ok := session.Scene()
	if !ok {
		return runtimeDungeonDeathTarget{}, false, worldmap.ErrDungeonRunRequired
	}
	reference, bound := scene.RuntimeObjects[objectKey]
	if !bound || reference != target.Reference {
		return runtimeDungeonDeathTarget{}, false, fmt.Errorf(
			"%w: object_key=%d expected=%s/%d actual=%s/%d bound=%t",
			errDungeonActorOwnerConflict,
			objectKey,
			target.Reference.Kind,
			target.Reference.Index,
			reference.Kind,
			reference.Index,
			bound,
		)
	}
	var actorType byte
	var classified bool
	if recordCombatKill {
		// Special projections (including tournament opponents) do not use
		// ordinary START_MAP ranks. Their dedicated rewards remain separate;
		// optional card accounting must not reject an otherwise valid death.
		actorType, err = r.cardGoldActorTypeLocked(objectKey)
		classified = err == nil
	}
	if prepare != nil {
		for _, dead := range scene.DefeatedObjects {
			if dead == objectKey {
				return runtimeDungeonDeathTarget{}, false, worldmap.ErrHostileAlreadyDefeated
			}
		}
		// Serialize death commits without holding the room state lock over SQL.
		// Readers/ordinary loot callbacks remain free to inspect this room.
		r.mu.Unlock()
		var prepareErr error
		func() { defer r.mu.Lock(); prepareErr = prepare() }()
		if prepareErr != nil {
			return runtimeDungeonDeathTarget{}, false, prepareErr
		}
		target, state, err = r.deathTargetLocked(objectKey)
		if err != nil {
			return runtimeDungeonDeathTarget{}, false, err
		}
		if *state != runtimeDungeonMonsterAnnounced {
			return runtimeDungeonDeathTarget{}, false, errDungeonMonsterAlreadyDefeated
		}

	}
	cleared, err := session.MarkHostileDefeated(objectKey)
	if err != nil {
		return runtimeDungeonDeathTarget{}, false, err
	}
	*state = runtimeDungeonMonsterDefeated
	if recordCombatKill {
		if r.combatDeaths == nil {
			r.combatDeaths = make(map[uint32]struct{})
		}
		r.combatDeaths[objectKey] = struct{}{}
	}
	if recordCombatKill && classified {
		r.cardGoldKills.addActorType(actorType)
	}
	return target, cleared, nil
}

// CommitRemainingAnnouncedHostilesAfterBoss mirrors the 86JP dungeon-clear
// domain rule used after a real boss death: the boss death is the authoritative
// end of combat, so remaining already-announced hostile actors in the same
// room must not keep the room/settlement chain stuck.  It intentionally does
// not fabricate a boss death; callers invoke it only after CommitActorDeathReport
// accepted the boss actor's own op39.
func (r *runtimeDungeonRoom) CommitRemainingAnnouncedHostilesAfterBoss(
	bossObjectKey uint32,
	session *worldmap.DungeonSession,
) ([]runtimeDungeonDeathTarget, bool, error) {
	if r == nil {
		return nil, false, errDungeonMonsterObjectNotFound
	}
	if session == nil {
		return nil, false, worldmap.ErrDungeonRunRequired
	}
	snapshot := r.Snapshot()
	var forced []runtimeDungeonDeathTarget
	cleared := false
	for _, monster := range snapshot.Monsters {
		if monster.ObjectKey == bossObjectKey || monster.State != runtimeDungeonMonsterAnnounced {
			continue
		}
		target, roomCleared, err := r.commitActorDeathReport(monster.ObjectKey, session, false)
		if err != nil {
			if errors.Is(err, errDungeonMonsterAlreadyDefeated) || errors.Is(err, worldmap.ErrHostileAlreadyDefeated) {
				continue
			}
			return forced, cleared, err
		}
		forced = append(forced, target)
		cleared = cleared || roomCleared
	}
	for _, actor := range snapshot.ExtendedActors {
		if actor.ObjectKey == bossObjectKey || actor.State != runtimeDungeonMonsterAnnounced {
			continue
		}
		if actor.HostileReference == nil {
			// Map-object monster children do not block room clear, but they
			// still need native death ACKs at combat end. Live map58553 key433
			// reported owner=ffff after the boss and otherwise remained active.
			// Preserve friendly AI, passive objects and future abyss templates.
			if actor.Kind != runtimeDungeonActorSpecialMonster || actor.HellPartyOrder != 0 {
				continue
			}
			target, matched, err := r.RetireSpecialMonsterActor(actor.ObjectKey)
			if err != nil {
				return forced, cleared, err
			}
			if matched {
				forced = append(forced, target)
			}
			continue
		}
		target, roomCleared, err := r.commitActorDeathReport(actor.ObjectKey, session, false)
		if err != nil {
			if errors.Is(err, errDungeonMonsterAlreadyDefeated) || errors.Is(err, worldmap.ErrHostileAlreadyDefeated) {
				continue
			}
			return forced, cleared, err
		}
		forced = append(forced, target)
		cleared = cleared || roomCleared
	}
	return forced, cleared, nil
}

// RetireNonHostileAIActor marks an announced, non-blocking character AI as
// retired without touching the hostile-object/session clear state. The
// current client still reports this visual helper through DIE_MONSTER with
// owner=0xffff, but it has no HostileReference and must not be committed via
// MarkHostileDefeated. A repeated report for the same already-retired actor
// returns the same target so the caller can safely replay the ACK.
func (r *runtimeDungeonRoom) RetireNonHostileAIActor(
	objectKey uint32,
) (runtimeDungeonDeathTarget, error) {
	if r == nil {
		return runtimeDungeonDeathTarget{}, errDungeonMonsterObjectNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index, ok := r.extendedByObjectKey[objectKey]
	if !ok {
		return runtimeDungeonDeathTarget{}, fmt.Errorf("%w: object_key=%d", errDungeonMonsterObjectNotFound, objectKey)
	}
	actor := &r.extendedActors[index]
	if actor.Kind != runtimeDungeonActorAICharacter || actor.HostileReference != nil || actor.Packet.Blocking != 0 {
		return runtimeDungeonDeathTarget{}, fmt.Errorf(
			"%w: object_key=%d kind=%s blocking=%d hostile_reference=%t",
			errDungeonNonHostileRetirement,
			objectKey,
			actor.Kind,
			actor.Packet.Blocking,
			actor.HostileReference != nil,
		)
	}
	if actor.State != runtimeDungeonMonsterAnnounced && actor.State != runtimeDungeonMonsterDefeated {
		return runtimeDungeonDeathTarget{}, fmt.Errorf(
			"%w: object_key=%d state=%s",
			errDungeonNonHostileRetirement,
			objectKey,
			actor.State,
		)
	}
	actor.State = runtimeDungeonMonsterDefeated
	return runtimeDungeonDeathTarget{
		ObjectKey:    objectKey,
		ResponseKind: currentDungeonDeathResponseAICharacter,
	}, nil
}

// RetireSpecialMonsterActor acknowledges a PVF [special passive object] child
// [monster] actor that the current client reports through DIE_MONSTER, but that
// is intentionally not a hostile room owner. These actors are visual/runtime
// children of a map object (for example enemies spawned out of a breakable
// building), so retiring them must not call MarkHostileDefeated and must not
// affect room-clear, EXP, drops, or quest kill accounting.
func (r *runtimeDungeonRoom) RetireSpecialMonsterActor(
	objectKey uint32,
) (runtimeDungeonDeathTarget, bool, error) {
	if r == nil {
		return runtimeDungeonDeathTarget{}, false, errDungeonMonsterObjectNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index, ok := r.extendedByObjectKey[objectKey]
	if !ok {
		return runtimeDungeonDeathTarget{}, false, nil
	}
	actor := &r.extendedActors[index]
	if actor.Kind != runtimeDungeonActorSpecialMonster {
		return runtimeDungeonDeathTarget{}, false, nil
	}
	if actor.State == runtimeDungeonMonsterDefeated {
		return runtimeDungeonDeathTarget{}, true, errDungeonMonsterAlreadyDefeated
	}
	if actor.HostileReference != nil {
		return runtimeDungeonDeathTarget{}, true, fmt.Errorf(
			"%w: object_key=%d kind=%s hostile_reference=%t",
			errDungeonNonHostileRetirement,
			objectKey,
			actor.Kind,
			true,
		)
	}
	if actor.State != runtimeDungeonMonsterAnnounced && actor.State != runtimeDungeonMonsterDefeated {
		return runtimeDungeonDeathTarget{}, true, fmt.Errorf(
			"%w: object_key=%d kind=%s state=%s",
			errDungeonNonHostileRetirement,
			objectKey,
			actor.Kind,
			actor.State,
		)
	}
	actor.State = runtimeDungeonMonsterDefeated
	return runtimeDungeonDeathTarget{
		ObjectKey:    objectKey,
		ResponseKind: currentDungeonDeathResponseMonster,
	}, true, nil
}

func (r *runtimeDungeonRoom) deathTargetLocked(
	objectKey uint32,
) (runtimeDungeonDeathTarget, *runtimeDungeonMonsterState, error) {
	if index, ok := r.byObjectKey[objectKey]; ok {
		monster := &r.monsters[index]
		return runtimeDungeonDeathTarget{
			ObjectKey:    objectKey,
			Reference:    monster.Reference,
			ResponseKind: currentDungeonDeathResponseMonster,
		}, &monster.State, nil
	}
	index, ok := r.extendedByObjectKey[objectKey]
	if !ok {
		return runtimeDungeonDeathTarget{}, nil, fmt.Errorf("%w: object_key=%d", errDungeonMonsterObjectNotFound, objectKey)
	}
	actor := &r.extendedActors[index]
	if actor.HostileReference == nil {
		return runtimeDungeonDeathTarget{}, nil, fmt.Errorf("%w: object_key=%d kind=%s", errDungeonActorNotHostile, objectKey, actor.Kind)
	}
	responseKind := currentDungeonDeathResponseMonster
	if actor.Kind == runtimeDungeonActorAICharacter {
		responseKind = currentDungeonDeathResponseAICharacter
	}
	return runtimeDungeonDeathTarget{
		ObjectKey:    objectKey,
		Reference:    *actor.HostileReference,
		ResponseKind: responseKind,
	}, &actor.State, nil
}

// RecordDeathResponseBody 记住这一怪被接受的**权威死亡通知体**：队伍级共享实例下它既用于
// 给同实例队友广播同一份死亡通知，也用于"重复 op39"的幂等重放（客户端需要同一份形状收尾，
// 否则被拒的那一侧会留一只白色残影）。
func (r *runtimeDungeonRoom) RecordDeathResponseBody(objectKey uint32, body []byte) {
	if r == nil || objectKey == 0 || len(body) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deathBodies == nil {
		r.deathBodies = make(map[uint32][]byte, 4)
	}
	r.deathBodies[objectKey] = append([]byte(nil), body...)
}

// DeathResponseBody 取回某个 object key 已经提交过的权威死亡/退役通知体。非敌对 actor 退役后
// 会被移出房间索引，重复 op39 只能靠这份缓存重放（否则客户端把 actor 留在半死状态）。
func (r *runtimeDungeonRoom) DeathResponseBody(objectKey uint32) ([]byte, bool) {
	if r == nil || objectKey == 0 {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	body, ok := r.deathBodies[objectKey]
	if !ok || len(body) == 0 {
		return nil, false
	}
	return append([]byte(nil), body...), true
}

// HasCombatDeath distinguishes accepted combat deaths from forced cleanup.
func (r *runtimeDungeonRoom) HasCombatDeath(objectKey uint32) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.combatDeaths[objectKey]
	return ok
}

// DefeatedDeathTarget 取出某个 object key 的死亡目标（要求房间里的权威状态已经是 defeated），
// 供"重复 op39"的幂等重放重建同一份通知形状。
func (r *runtimeDungeonRoom) DefeatedDeathTarget(objectKey uint32) (runtimeDungeonDeathTarget, bool) {
	if r == nil || objectKey == 0 {
		return runtimeDungeonDeathTarget{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	target, state, err := r.deathTargetLocked(objectKey)
	if err != nil || state == nil || *state != runtimeDungeonMonsterDefeated {
		return runtimeDungeonDeathTarget{}, false
	}
	return target, true
}

// highObjectKey 返回这个房间用掉的最高 objectKey + 1（怪物 + 扩展 actor 一起看）。
// 队伍级共享实例用它把共享发号器推到安全位置：队友在同一个房间被提交之前复用这份
// 房间对象时，发出的 objectKey 不能和房间里的怪撞车。
func (r *runtimeDungeonRoom) highObjectKey() uint32 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	high := uint32(0)
	for _, monster := range r.monsters {
		if monster.ObjectKey >= high {
			high = monster.ObjectKey + 1
		}
	}
	for _, actor := range r.extendedActors {
		if actor.ObjectKey >= high {
			high = actor.ObjectKey + 1
		}
	}
	return high
}

func cloneRuntimeDungeonMonsters(values []runtimeDungeonMonster) []runtimeDungeonMonster {
	out := make([]runtimeDungeonMonster, len(values))
	for index, value := range values {
		out[index] = value
		out[index].Definition.Scalars = cloneFloatMap(value.Definition.Scalars)
		out[index].Definition.Sections = append([]string(nil), value.Definition.Sections...)
	}
	return out
}

func cloneRuntimeDungeonExtendedActors(values []runtimeDungeonExtendedActor) []runtimeDungeonExtendedActor {
	out := make([]runtimeDungeonExtendedActor, len(values))
	for index, value := range values {
		out[index] = value
		if value.HostileReference != nil {
			reference := *value.HostileReference
			out[index].HostileReference = &reference
		}
		if value.AICharacter != nil {
			out[index].AICharacter = cloneRuntimeDungeonAICharacter(*value.AICharacter)
		}
		if value.AICharacterMetadata != nil {
			definition := cloneDungeonAICharacterDefinition(*value.AICharacterMetadata)
			out[index].AICharacterMetadata = &definition
		}
	}
	return out
}

func cloneRuntimeDungeonRetainedSpecialSpawns(values []runtimeDungeonRetainedSpecialSpawn) []runtimeDungeonRetainedSpecialSpawn {
	return append([]runtimeDungeonRetainedSpecialSpawn(nil), values...)
}

func cloneFloatMap(values map[string]float64) map[string]float64 {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]float64, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
