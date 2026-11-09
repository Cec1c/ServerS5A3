package dnfbridge

import (
	"encoding/binary"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 动态友方（消耗品人偶）的死亡上报。
//
// 现场（2026-11-09 测试服，幽暗密林 3 / 雷鸣废墟 5）：玩家只用人偶打怪，客户端发出
// owner=ffff、恰好一条 14 字节贡献、贡献值非零的 op39。贡献分两种形态：
//   - 人偶本体出手：kind=0x111（AI 角色），键就是人偶自己的对象键，房间内固定；
//   - 人偶召唤物出手（遗迹少女会召机器人）：kind=0x211（怪物阵营），键是每一台召唤物
//     各自的对象键，同一房间里会连续出现多个不同键（现场 18 / 35 / 77）。
//
// 这些键都由客户端在本地分配（贡献键取自贡献者对象虚表 +0x54），服务端没有登记来源：
// `CmdPacketSummonMonster` 在本仓无 handler，抓包中客户端也从不发。原逻辑因此以
// `non_hostile_ai_request_shape_unproven` 拒收，怪在服务端仍是活怪、房间不清，客户端却
// 已本地判死 —— 白怪且过不了门。
//
// 服务端能证明的不是"这个键属于谁"，而是"这一局确实存在一个被消耗的召唤源"。所以这里
// 只给一个有界租约：消耗凭证 + 单条贡献 + 本房阻挡怪 + 每房有上限的键集合。不全局放开
// sentinel 形状，不接受未知键冒名已宣告 actor，不因此清房或跳过奖励。
const (
	// 现场两种贡献类型：AI 角色（人偶本体）与怪物阵营（人偶的召唤物）。
	currentDungeonPuppetContributionKindAICharacter = uint16(0x111)
	currentDungeonPuppetContributionKindMonster     = uint16(0x211)
	// 同一房间内允许出现的召唤物键上限（人偶可能连续召唤，且每台各有自己的键）。
	currentDungeonPuppetLeaseKeysPerRoom = 32
	// 每房最多回发多少条"客户端独有怪"的死亡通知（地图 act 召唤，服务端无实体）。
	currentDungeonPuppetNotificationOnlyLimit = 64
	// [summon apc] 缺省寿命；28 号该段写的是 900000ms。
	currentDungeonPuppetDefaultLifetime = 15 * time.Minute
)

// currentDungeonPuppetLease 是某个角色的召唤源凭证。
//
// 键按房间分组：换房时客户端会重建召唤物 actor 并重新分配键，因此进入新房间后重新收集；
// 同一房间内允许多个键（人偶本体的键 + 各台召唤物的键），但有数量上限。
type currentDungeonPuppetLease struct {
	CharacterID uint16
	ItemID      uint32
	APC         int64
	ExpiresAt   time.Time
	Room        string
	Keys        map[uint32]struct{}
	Accepted    int
	Echoed      int
}

// recordCurrentDungeonPuppetLease 在 op44 成功回执之后登记召唤源。
// 只认 PVF 明确写了 `[summon apc]` 的消耗品；不是按道具 ID 白名单。
// 调用方不持有 session.dungeon.mu（game_aligned 域事务之后），这里自己取。
func (s *Service) recordCurrentDungeonPuppetLease(session *gameSession, opcode uint16, result alignedcmd.Result) {
	if s == nil || session == nil || opcode != uint16(dnfenum.CmdPacketUseStackable) {
		return
	}
	fact := result.StackableConsumption
	if !result.Handled || !result.ResponseAllowed || fact == nil ||
		fact.ItemID <= 0 || fact.ItemID > int64(^uint32(0)) {
		return
	}
	if fact.CharacterID != strconv.FormatUint(uint64(session.selectedCharacterID), 10) {
		return
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil || catalog.source == nil {
		return
	}
	item, err := catalog.ResolveItem(uint32(fact.ItemID))
	if err != nil {
		return
	}
	apc, lifetime, ok := currentDungeonPuppetSummon(item, catalog.source)
	if !ok {
		return
	}
	now := s.currentDungeonPuppetNow()
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	if runtime == nil || !dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return
	}
	session.puppetLease = &currentDungeonPuppetLease{
		CharacterID: session.selectedCharacterID,
		ItemID:      uint32(fact.ItemID),
		APC:         apc,
		ExpiresAt:   now.Add(lifetime),
	}
	s.logInfo("dnfbridge puppet summon lease armed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"item_id", fact.ItemID,
		"apc", apc,
		"lifetime_ms", lifetime.Milliseconds())
}

// currentDungeonPuppetAllyDeath 判定一条 sentinel+贡献 的 op39 是否由"已证明存在"的
// 动态友方（人偶本体或其召唤物）发出。租约是唯一放行来源，且目标必须是本房已宣告、
// 仍存活、阻挡型的普通怪。
// 调用方持有 session.dungeon.mu（handleDungeonMonsterDeath 已加锁）。
func (s *Service) currentDungeonPuppetAllyDeath(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	request dungeoncmd.DieMonsterRequest,
) bool {
	if s == nil || session == nil || runtime == nil || runtime.Room == nil ||
		s.options.currentClientVersion != "official-24.9" {
		return false
	}
	if request.OwnerObjectKey != ^uint16(0) ||
		request.Layout != dungeoncmd.DieMonsterRequestLayoutVariableCombat ||
		request.CombatEntryCount != 1 ||
		len(request.CombatEntries) != dungeoncmd.Official249DieMonsterEntrySize ||
		len(request.OpaqueTail) != 0 {
		return false
	}
	entry := request.CombatEntries
	kind := binary.LittleEndian.Uint16(entry)
	if kind != currentDungeonPuppetContributionKindAICharacter &&
		kind != currentDungeonPuppetContributionKindMonster {
		return false
	}
	if binary.LittleEndian.Uint64(entry[4:]) == 0 {
		return false
	}
	// 形状已吻合。此后每一步失败都记录原因：这些事件只出现在"有召唤源形状但没被接受"
	// 的场合，量很小，且下一次现场复现可以凭一行日志定位卡点。
	key := uint32(binary.LittleEndian.Uint16(entry[2:]))
	room := runtime.Room.Snapshot().Coordinate.String()
	reject := func(reason string) bool {
		s.logInfo("dnfbridge puppet ally contribution rejected",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"map_id", runtime.Room.Snapshot().MapID,
			"room", room,
			"target_key", request.RuntimeObjectKey,
			"contribution_kind", kind,
			"contribution_key", key,
			"reason", reason)
		return false
	}
	if key == 0 || key == request.RuntimeObjectKey {
		return reject("key_invalid")
	}
	// 冒用本房已宣告 actor 的键：那是另一条已被严格校验的路径，不在这里兜底。
	if runtime.Room.ContainsAnnouncedActorObjectKey(key) {
		return reject("key_is_announced_actor")
	}
	// 目标证明用房间级"在册活怪"：普通 [monster] 行与运行期扩展 actor（例如地图
	// [special passive object] 投放出来的怪）都覆盖。只认普通怪行会漏掉后者，
	// 现场表现就是"同一房间前几只结算、后几只仍白"。
	if !currentDungeonRoomAnnouncedLiveHostile(runtime, scene, request.RuntimeObjectKey) {
		// 完全不在册：地图 act 脚本 [SUMMON MONSTER] 在客户端本地召出来的怪。服务端
		// 没有实体，结算不了；只回发一条死亡通知让客户端收尸，不改服务端状态。
		s.echoCurrentDungeonPuppetClientSpawnedDeath(session, runtime, request.RuntimeObjectKey, room)
		return reject("target_not_in_room_table_notified")
	}
	reference, bound := scene.RuntimeObjects[request.RuntimeObjectKey]
	if !bound {
		return reject("target_unbound")
	}
	if !runtimeSceneContainsHostile(scene.BlockingHostiles, reference) {
		return reject("target_not_blocking")
	}
	now := s.currentDungeonPuppetNow()
	lease := session.puppetLease
	if lease == nil || lease.CharacterID != session.selectedCharacterID {
		return reject("no_lease")
	}
	if !now.Before(lease.ExpiresAt) {
		session.puppetLease = nil
		return reject("lease_expired")
	}
	if !currentDungeonPuppetLeaseAdmits(lease, room, key) {
		return reject("lease_key_budget_exhausted")
	}
	// Boss 与清房条件目标也放行：人偶把人打死之后如果拒收，客户端会留一只白 Boss，
	// 本局直接作废——这比"人偶能打 Boss"的收益严重得多。边界仍由租约、本房在册、
	// 阻挡怪三条守住；结算走原有 Boss 链路（bossDieCheck / 清房条件照旧）。
	bossTarget, _, _ := currentDungeonRuntimeObjectBossActor(runtime, request.RuntimeObjectKey, runtimeDungeonMonsterAnnounced)
	lease.Accepted++
	s.logInfo("dnfbridge puppet ally contribution accepted",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"map_id", runtime.Room.Snapshot().MapID,
		"room", room,
		"target_key", request.RuntimeObjectKey,
		"contribution_kind", kind,
		"contribution_key", key,
		"target_is_boss", bossTarget,
		"item_id", lease.ItemID,
		"accepted", lease.Accepted)
	return true
}

// echoCurrentDungeonPuppetClientSpawnedDeath 处理"客户端独有"的房间怪：地图 act 脚本
// 用 [SUMMON MONSTER] 在客户端本地召出来的怪，服务端没有对应实体，既不能提交死亡、
// 也给不了奖励。这里只回发一条死亡通知让客户端收尸；不改服务端状态、不触发清房，
// 因此不会产生掉落、经验或过门副作用。
// 调用方持有 session.dungeon.mu。
func (s *Service) echoCurrentDungeonPuppetClientSpawnedDeath(
	session *gameSession,
	runtime *runtimeDungeonState,
	objectKey uint32,
	room string,
) {
	now := s.currentDungeonPuppetNow()
	lease := session.puppetLease
	if lease == nil || lease.CharacterID != session.selectedCharacterID || !now.Before(lease.ExpiresAt) {
		return
	}
	if lease.Room != room {
		lease.Room = room
		lease.Keys = nil
		lease.Echoed = 0
	}
	if lease.Echoed >= currentDungeonPuppetNotificationOnlyLimit {
		return
	}
	body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(objectKey, currentDungeonDeathResponseMonster, nil)
	if err != nil {
		return
	}
	lease.Echoed++
	if err := s.replayPartyDungeonAuthoritativeDeathWithBodyLocked(session, runtime, objectKey, body,
		"client_only_room_monster_under_puppet_lease"); err != nil {
		s.logInfo("dnfbridge puppet ally notification-only failed",
			"char_id", session.selectedCharacterID,
			"target_key", objectKey,
			"reason", "write_failed")
		return
	}
	s.logInfo("dnfbridge puppet ally notification-only sent",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"room", room,
		"target_key", objectKey,
		"echoed", lease.Echoed)
}

// currentDungeonPuppetLeaseAdmits 实现"每房一组键"：换房时客户端会重建召唤物 actor 并
// 重新分配键，所以进入新房间后清空重新收集；同一房间内允许多个键（人偶本体 + 各台召唤物），
// 但不超过 currentDungeonPuppetLeaseKeysPerRoom。
func currentDungeonPuppetLeaseAdmits(lease *currentDungeonPuppetLease, room string, key uint32) bool {
	if lease == nil || key == 0 {
		return false
	}
	if lease.Room != room {
		lease.Room = room
		lease.Keys = nil
	}
	if lease.Keys == nil {
		lease.Keys = make(map[uint32]struct{}, 8)
	}
	if _, known := lease.Keys[key]; known {
		return true
	}
	if len(lease.Keys) >= currentDungeonPuppetLeaseKeysPerRoom {
		return false
	}
	lease.Keys[key] = struct{}{}
	return true
}

// currentDungeonSceneDeclaresFriendlyAICharacter 判断当前房间是否声明了非敌对 AI 角色。
// 敌对声明（faction=monster）不算友方来源；当前仅用于日志，不参与放行判定。
func currentDungeonSceneDeclaresFriendlyAICharacter(scene worldmap.DungeonRoomScene) bool {
	for index := range scene.AICharacters {
		if normalizeDungeonPVFSymbol(scene.AICharacters[index].Faction) != "monster" {
			return true
		}
	}
	return false
}

// currentDungeonPuppetSummon 读取消耗品的 `[summon apc]`：
// `<apc code> <level> <level> <lifetime ms> <count>`。
// 没有该段就不是召唤源，直接拒绝。
func currentDungeonPuppetSummon(
	item dungeonDropItemDefinition,
	source dnfpvf.Source,
) (int64, time.Duration, bool) {
	if source == nil || item.Kind != dungeonDropItemStackable || item.PVFPath == "" {
		return 0, 0, false
	}
	text, err := source.ReadText(item.PVFPath)
	if err != nil {
		return 0, 0, false
	}
	document, err := dnfpvf.Parse(item.PVFPath, text)
	if err != nil || document == nil {
		return 0, 0, false
	}
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "summon apc") {
			continue
		}
		tokens := document.Tokens[section.Start:section.End]
		if len(tokens) == 0 || tokens[0].Kind != dnfpvf.TokenInt || tokens[0].Int <= 0 {
			return 0, 0, false
		}
		lifetime := currentDungeonPuppetDefaultLifetime
		for _, token := range tokens[1:] {
			if token.Kind != dnfpvf.TokenInt || token.Int <= 0 {
				continue
			}
			// 寿命是第一个大于等级量级的毫秒值；等级/数量都在 1e5 以下。
			if token.Int >= 60_000 {
				lifetime = time.Duration(token.Int) * time.Millisecond
				break
			}
		}
		return tokens[0].Int, lifetime, true
	}
	return 0, 0, false
}

// logCurrentSentinelDeathShape 记录"带贡献项"或"owner 既不是哨兵也不是本人"的 op39 形状。
// 正常玩家击杀是 owner=<角色键> + 0 条贡献，艾肯阵营互杀是哨兵 + 0 条贡献，都不记录；
// 因此这条只在友方/召唤物代打、脚本击杀、未知 owner 等场合出现。不依赖
// DNFBRIDGE_PACKET_LOG，默认档也能读回，用于定位"只有部分房间失败"这类现场。
func logCurrentSentinelDeathShape(
	s *Service,
	session *gameSession,
	runtime *runtimeDungeonState,
	request dungeoncmd.DieMonsterRequest,
) {
	if s == nil || session == nil {
		return
	}
	playerKey := currentSceneActorObjectKey(session.selectedCharacterID)
	if request.CombatEntryCount == 0 &&
		(request.OwnerObjectKey == ^uint16(0) || request.OwnerObjectKey == playerKey) {
		return
	}
	fields := []any{
		"char_id", session.selectedCharacterID,
		"target_key", request.RuntimeObjectKey,
		"owner_key", request.OwnerObjectKey,
		"player_key", playerKey,
		"layout", request.Layout,
		"entry_count", request.CombatEntryCount,
	}
	if runtime != nil && runtime.Room != nil {
		room := runtime.Room.Snapshot()
		fields = append(fields, "dungeon_id", runtime.Dungeon.ID, "map_id", room.MapID, "room", room.Coordinate.String())
	}
	for index, offset := 0, 0; index < 3 && offset+dungeoncmd.Official249DieMonsterEntrySize <= len(request.CombatEntries); index, offset = index+1, offset+dungeoncmd.Official249DieMonsterEntrySize {
		entry := request.CombatEntries[offset:]
		fields = append(fields,
			"entry"+strconv.Itoa(index)+"_kind", binary.LittleEndian.Uint16(entry),
			"entry"+strconv.Itoa(index)+"_key", binary.LittleEndian.Uint16(entry[2:]),
			"entry"+strconv.Itoa(index)+"_nonzero", binary.LittleEndian.Uint64(entry[4:]) != 0)
	}
	s.logInfo("dnfbridge sentinel death request", fields...)
}

// currentDungeonPuppetNow 统一取时钟；测试夹具可能没有注入 gameplayTimers。
func (s *Service) currentDungeonPuppetNow() time.Time {
	if s != nil && s.gameplayTimers != nil {
		return s.gameplayTimers.Now()
	}
	return time.Now()
}
