package dnfbridge

import (
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 本文件把副本内那条 class0/op3 USER_STATE 从"只声明自己"扩成"声明本场副本运行时里
// 实际在场的全员"。**默认档逐字节保持今天**（条数恒 1、4 字节体）。
//
// # 厂商规格（本机只读源码，逐行确证）
//
// `S4A21-完整包-20260923\源码\服务端\Server\DfoServer\`
//
//   - `Network\Builders\Dungeon\EnterSelectDungeonStateBuilder.cs:12-26`
//
//     public static byte[] BuildUserState(IReadOnlyList<ushort> userIds, byte userState)
//     {
//     var count = userIds?.Count ?? 0;
//     writer.WriteByte((byte)count);         // u8 条数
//     for (var i = 0; i < count; i++) {
//     writer.WriteUInt16(userIds[i]);    // u16 用户/对象 key
//     writer.WriteByte(userState);       // u8 状态
//     }
//     }
//
//   - `Network\Handlers\Dungeon\DungeonEntryHandler.cs:177-202` 的
//     `BuildEnterSelectDungeonPrefix` 对**每个接收者发全员**：
//     (1) N×NOTI 0x0002 USERINFO → (2) class1 responseType →
//     (3) NOTI 0x0003 USER_STATE = BuildUserState(dungeonUserIds, 0x01) →
//     (4) NOTI 0x001A host slot。
//     同文件 `:761-764` 的注释原文：
//     `// A21 keeps one party-member object per roster slot. Every receiver must see every`
//     `// dungeon participant transition to state 1; a self-only USER_STATE leaves remote`
//     `// slots at 0.`
//   - 顺序（`TryBuildDungeonUserInfoProjectionOrder`，`:84-153`）：**receiver-first**，
//     其余 peer 按 `SlotIndex` **稳定升序**。
//
// # 我们的现状与失败模式
//
// 进图时服务端**双向**都发了队友 actor 投影（`game-dungeon-party-peer-actor-send`）且客户端
// 确实建出了 actor（`local-actor-create phase=after object_key=1`），但两端各自只渲染自己。
// 与厂商注释描述的失败模式一致：那条 USER_STATE 只声明自己（4 字节），队里其它人的 roster
// 槽位停在 0。本文件按同一形状把它扩成全员。
//
// # 成员取源链（不新造第二套 roster）
//
//  1. `runtimeDungeonState.partyInstance`（`dungeon_party_instance.go:109-125`，事件名
//     `game-dungeon-party-instance-created` / `-joined`）—— 它**就是**"当前副本运行时里
//     实际在场的成员"：加入的唯一路径是 `adoptPartyDungeonInstance`
//     （`dungeon_party_instance.go:197-310`，判据是同一队伍 + 同一副本/迷宫/房间/地图/种子），
//     离开的唯一路径是 `releasePartyDungeonInstance`（同文件 `:861-903`，出图回城/阵亡/
//     换角色/断线都会把 `members` 与 `sessions` 一起摘掉）。**不读城市缓存、不读会话缓存凑数。**
//  2. `runtimePartyMembers(runtimePartyStateSnapshot(session))`（`party_online.go:1974-2031`）
//     —— 给出**稳定 slot 序**（客户端队伍框行号 = 这个列表的下标，leader 恒在 slot 0，
//     见该函数注释与 `runtimePartyMemberIndex`（`dungeon_navigation_runtime.go:858-869`）。
//     它只用来**排序与判身份**，不扩员：不在上面那套实例里的成员一个都不发。
//  3. key 口径 = `currentSceneActorObjectKey(charID)`（`character_scene_object.go:29-34`），
//     与今天这条包以及 op2/op9 队友投影用的完全同源。
//
// # 顺序（写死，禁止 map 迭代顺序泄漏）
//
// **receiver-first（自己在前）+ 其余按上述 slot 升序**（与厂商 `TryBuildDungeonUserInfoProjectionOrder`
// 同口径）。所有"谁在场"的判断都在 `present` 集合上做，但发出去的顺序只来自 roster 切片，
// 所以 `instance.members` 是 map 也不影响输出。
//
// # 回落（任何一条都绝不发半条脏数据）
//
// 出现任一下列情形时，`all` 档也**回落成只发自己**（4 字节，与默认档逐字节相同），并在
// `game-dungeon-user-state-members` 事件里带 `reason=`：
//
//	no_active_dungeon_runtime                    本会话没有活跃副本运行时
//	self_object_key_unavailable                  自己这一条的对象 key 不可得（兜底）
//	no_party_dungeon_instance_for_this_run        本场没有队伍级副本实例（单人/无队伍/房间对不上）
//	party_dungeon_member_session_unavailable      实例里有成员的会话指针已失效（身份不可证）
//	self_not_registered_in_party_dungeon_instance 自己不在实例成员表里（状态不一致）
//	sole_member_in_party_dungeon_instance         实例里目前只有自己（队友还没落位/已离开）
//	party_dungeon_member_missing_from_local_roster 实例里有成员在本会话名册里找不到稳定 slot
//	peer_object_key_unavailable                   队友的对象 key 不可得（非零角色 id 下不可达，兜底）
//
// 默认档（`self`）**一条日志都不加**：既有发送点的日志逐字节不变。

// dungeonUserStateMembers 是"这一条 op3 USER_STATE 要写进 body 的成员"。
type dungeonUserStateMembers struct {
	// Keys 是 body 里逐条写出的 u16 key，[0] 恒为自己（receiver-first）。
	Keys []uint16
	// Mode 是 DNFBRIDGE_PARTY_DUNGEON_ALL_MEMBER_USER_STATE 的归一化档位（self/all），
	// 只用于日志判读，不参与组包。
	Mode string
	// Reason 非空 = 这一档本该发全员但已回落成只发自己，值是上面的回落原因之一。
	Reason string
}

// partyDungeonUserStateMode 是启动日志与事件日志里的档位串。
func (s *Service) partyDungeonUserStateMode() string {
	if s != nil && s.options.partyDungeonUserStateAll {
		return partyDungeonUserStateValueAll
	}
	return partyDungeonUserStateValueSelf
}

// dungeonRuntimePointerForUserState 供**不持有 session.dungeon.mu 的发送点**
// （scene_finish_loading.go / game_enter_select_dungeon.go）取一次本会话当前副本运行时的
// 只读指针。它只碰自己的会话锁，不触碰队友会话（锁序仍是 session.dungeon.mu → instance.mu）。
//
// 这里刻意用 `TryLock` 而不是 `Lock`：`sync.Mutex` 不可重入，而进本选择页那条路径
// （`game_enter_select_dungeon.go`）既能从事件循环里被调、也能从已经持有 `session.dungeon.mu`
// 的换场/结算卡分支里被调（例如 `dungeon_card_flow.go:437`，那里在 `:410` 已解锁；
// 但为了不让将来任何一条新调用路径在这里自死锁），拿不到锁就按"这份运行时在此处不可读"
// 处理 ⇒ all 档回落成只发自己并记 `reason=no_active_dungeon_runtime`。
// **已经持有 session.dungeon.mu 的调用方**（scene_post_start_map.go / dungeon_story_pause.go）
// 直接传自己在手的 runtime，绝不要调这个函数。
func dungeonRuntimePointerForUserState(session *gameSession) *runtimeDungeonState {
	if session == nil {
		return nil
	}
	if !session.dungeon.mu.TryLock() {
		return nil
	}
	defer session.dungeon.mu.Unlock()
	return session.dungeon.runtime
}

// buildCurrentDungeonUserStateBody 是默认档与全部既有钉死用例的入口：**恒单条 4 字节**
// （`[1, keyLo, keyHi, 1]`），与加这个开关之前逐字节相同。
func buildCurrentDungeonUserStateBody(objectKey uint16) ([]byte, error) {
	if objectKey == 0 {
		return nil, fmt.Errorf("current dungeon user state requires a nonzero object key")
	}
	return buildCurrentDungeonUserStateBodyForMemberKeys([]uint16{objectKey})
}

// buildCurrentDungeonUserStateBodyForMemberKeys 按厂商 `BuildUserState` 的形状写出
// "u8 条数 + N×(u16 key + u8 state)"。条数为 1 时与 buildCurrentDungeonUserStateBody 相同。
func buildCurrentDungeonUserStateBodyForMemberKeys(memberKeys []uint16) ([]byte, error) {
	if len(memberKeys) == 0 {
		return nil, fmt.Errorf("current dungeon user state requires at least one member key")
	}
	if len(memberKeys) > int(^uint8(0)) {
		return nil, fmt.Errorf("current dungeon user state member count %d exceeds one byte", len(memberKeys))
	}
	var writer packetWriter
	writer.writeByte(byte(len(memberKeys)))
	for _, key := range memberKeys {
		if key == 0 {
			return nil, fmt.Errorf("current dungeon user state requires a nonzero object key")
		}
		writer.writeUint16(key)
		writer.writeByte(currentDungeonPlayerUserState)
	}
	return writer.bytes(), nil
}

// currentDungeonUserStateBody 是三个（含进本选择页共四个）发送点共用的组包入口。
// 默认档返回与今天逐字节相同的 4 字节体；`all` 档按本场副本实例的在场成员逐条声明。
// 只在 `all` 档记 `game-dungeon-user-state-members`（默认档一条日志都不加）。
func (s *Service) currentDungeonUserStateBody(
	session *gameSession,
	runtime *runtimeDungeonState,
	selfKey uint16,
	source string,
) ([]byte, error) {
	members := s.dungeonUserStateMembersForSession(session, runtime, selfKey)
	body, err := buildCurrentDungeonUserStateBodyForMemberKeys(members.Keys)
	if err != nil {
		return nil, err
	}
	s.logDungeonUserStateMembers(session, source, members)
	return body, nil
}

// dungeonUserStateMembersForSession 决定这条 USER_STATE 声明哪些成员。
func (s *Service) dungeonUserStateMembersForSession(
	session *gameSession,
	runtime *runtimeDungeonState,
	selfKey uint16,
) dungeonUserStateMembers {
	members := dungeonUserStateMembers{
		Keys: []uint16{selfKey},
		Mode: s.partyDungeonUserStateMode(),
	}
	if members.Mode != partyDungeonUserStateValueAll {
		return members
	}
	keys, reason := s.dungeonAllMemberUserStateKeys(session, runtime, selfKey)
	if reason != "" || len(keys) < 2 {
		if reason == "" {
			reason = "sole_member_in_party_dungeon_instance"
		}
		members.Reason = reason
		return members
	}
	members.Keys = keys
	return members
}

// dungeonAllMemberUserStateKeys 解析"本场副本运行时里实际在场的成员"的 key 列表。
// 成功时返回 receiver-first + slot 升序的 key（长度 ≥ 2）；回落时返回 (nil, reason)。
func (s *Service) dungeonAllMemberUserStateKeys(
	session *gameSession,
	runtime *runtimeDungeonState,
	selfKey uint16,
) ([]uint16, string) {
	if session == nil {
		return nil, "no_active_dungeon_runtime"
	}
	if selfKey == 0 {
		return nil, "self_object_key_unavailable"
	}
	characterID := session.selectedCharacterID
	if characterID == 0 {
		return nil, "self_not_registered_in_party_dungeon_instance"
	}
	if runtime == nil {
		return nil, "no_active_dungeon_runtime"
	}
	if runtime.partyInstance == nil {
		return nil, "no_party_dungeon_instance_for_this_run"
	}
	instance := runtime.partyInstance
	instance.mu.Lock()
	memberCount := len(instance.members)
	present := make(map[uint16]bool, memberCount)
	unavailable := 0
	for id := range instance.members {
		if id == 0 {
			continue
		}
		if instance.sessions[id] == nil {
			// 会话指针已失效（正常路径由 releasePartyDungeonInstance 同时摘掉两者）：
			// 身份不可证 ⇒ 宁可只发自己，也不猜。
			unavailable++
			continue
		}
		present[id] = true
	}
	instance.mu.Unlock()
	if unavailable > 0 {
		return nil, "party_dungeon_member_session_unavailable"
	}
	if !present[characterID] {
		return nil, "self_not_registered_in_party_dungeon_instance"
	}
	if memberCount < 2 {
		return nil, "sole_member_in_party_dungeon_instance"
	}
	// 顺序只来自 roster 切片（稳定 slot 序），present 只用来判"在不在场"。
	roster := runtimePartyMembers(runtimePartyStateSnapshot(session))
	ordered := dungeonUserStateOrderedMemberIDs(characterID, present, roster)
	if len(ordered) != len(present) {
		// 在场但本会话名册里没有它的稳定 slot ⇒ 顺序只能猜，直接回落。
		return nil, "party_dungeon_member_missing_from_local_roster"
	}
	if len(ordered) < 2 {
		return nil, "sole_member_in_party_dungeon_instance"
	}
	keys := make([]uint16, 0, len(ordered))
	for index, id := range ordered {
		if index == 0 {
			keys = append(keys, selfKey)
			continue
		}
		key := currentSceneActorObjectKey(id)
		if key == 0 {
			return nil, "peer_object_key_unavailable"
		}
		keys = append(keys, key)
	}
	return keys, ""
}

// logDungeonUserStateMembers 记一条可 grep 的成员事件。**只有 `all` 档才记**：
// 默认档（self）保持既有日志与包序逐字节不变。
func (s *Service) logDungeonUserStateMembers(
	session *gameSession,
	source string,
	members dungeonUserStateMembers,
) {
	if s == nil || session == nil || members.Mode != partyDungeonUserStateValueAll {
		return
	}
	selfKey := uint16(0)
	if len(members.Keys) > 0 {
		selfKey = members.Keys[0]
	}
	fields := []any{
		"conn_id", session.connID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"env", partyDungeonAllMemberUserStateEnv,
		"mode", members.Mode,
		"user_state_count", len(members.Keys),
		"user_state", currentDungeonPlayerUserState,
		"self_key", selfKey,
		"member_keys", formatDungeonUserStateMemberKeys(members.Keys),
		"body_len", len(members.Keys)*3 + 1,
		"order", "receiver_first_then_stable_party_slot_ascending",
		"body_source", "vendor_builders_dungeon_enter_select_dungeon_state_builder_build_user_state",
	}
	if members.Reason != "" {
		fields = append(fields, "reason", members.Reason)
	}
	s.logGameEvent(session, "game-dungeon-user-state-members", fields...)
}

// formatDungeonUserStateMemberKeys 把 key 列表渲染成 `99,100` 这样的判读串。
func formatDungeonUserStateMemberKeys(keys []uint16) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strconv.Itoa(int(key)))
	}
	return strings.Join(parts, ",")
}

// dungeonUserStateOrderedMemberIDs 是上面顺序口径的纯函数部分：自己在前，其余按 roster
// 切片（= 稳定 slot 序，leader 在 slot 0）升序，并且只保留 present 里的成员。
// 单独抽出来是为了让"顺序与在场判据"能被直接钉住：**输出顺序与 present（map）的迭代顺序
// 无关**，只由 roster 决定。
func dungeonUserStateOrderedMemberIDs(
	selfCharacterID uint16,
	present map[uint16]bool,
	roster []alignedcmd.PartyMemberState,
) []uint16 {
	ordered := []uint16{selfCharacterID}
	for _, member := range roster {
		if member.UserID == 0 || member.UserID == selfCharacterID {
			continue
		}
		if present != nil && !present[member.UserID] {
			continue
		}
		ordered = append(ordered, member.UserID)
	}
	return ordered
}
