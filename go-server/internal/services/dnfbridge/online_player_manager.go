package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// onlinePlayerInfo is one player's town presence state.
type onlinePlayerInfo struct {
	CharacterID uint16
	AccountID   string
	Name        string
	Job         byte
	GrowType    byte
	Level       byte
	TownID      byte
	AreaID      byte
	// Instance 把"每个角色一份"的私有房间与共享城镇区域分开：0 = 普通共享区域，
	// 非 0 = 该私有房间拥有者的角色 ID。
	//
	// 现场报障「其他玩家可以进入别人的赛丽亚房间」：赛丽亚的房间在客户端是私有房间，
	// 但服务端原先只用 (TownID, AreaID) 做区域键，第二个角色进去后落进同一个桶，
	// 于是互相可见/互相影响。线上封包实测该房间地图路径 =
	// Cataclysm/Town/Elvengard/new_seria_room.map（set-user-area 提交 26 次）。
	Instance           uint32
	RaidBattleInstance string
	// ChannelID 是这条城镇存在所属的**频道**（= 该会话 game 连接本地端口对应的
	// channelcatalog 项，见 game.go 的 handleGameConn/channelForConn）。
	//
	// 城镇同屏的语义是"同城 + 同区 + 同频道 + 同实例"：不同频道的玩家在客户端是
	// 完全不同的世界，互相不可见、不可交互。现场双开实测报障
	// 「10 线的玩家会见到 121 线的玩家」就是因为分桶键里没有频道——两个会话
	// （channel_id=10 的角色 3 / channel_id=121 的角色 5）先后提交 (town 38, area 2)
	// 后落进同一个桶，服务端于是互相推送 mode0/mode1/op9 actor 与 op23 位置
	// （证据：/opt/dnf90/logs/packet_log.20260924-224523.txt 的
	// `game-town-copresence-actor-state-send conn_id=game-000021 channel_id=10
	// target_char_id=3 actor_char_id=5` 与反向那条）。
	//
	// 由 EnterArea 通过 resolvePresenceChannelID 从 Session 解析并回写；Session 为空的
	// 登记（单元测试夹具、历史调用点）用这里的显式值，0 表示"未绑定频道"。
	ChannelID int
	PositionX uint16
	PositionY uint16
	Direction byte
	AreaState byte
	// MotionState 是移动方最近一次 C2S op35 上报的 body[5:7]（厂商口径里的 motionState，
	// 见 initial_town_location_notifications.go 的 townPeerMotionStateEnv 文件头）。
	//
	// 它不是可选装饰：观者客户端**只能**从 0x0016 学到同屏 actor 的移动速率
	// （op24 AREA_USERS / op23 noti0x17 的布局里都没有这个字段），所以广播时必须原样透传。
	// 这里刻意**不做** "0 就回落 100" 的兜底：厂商（TownHandler.cs:174/184）也是原样回填，
	// 加兜底会引入一条没有字节证据的分支。
	MotionState uint16
	Session     *gameSession
}

// areaKey identifies a unique town area instance.
//
// 四个分量缺一不可：TownID/AreaID 是城镇与区域，ChannelID 是频道，Instance 是
// "每角色一份"的私有房间。频道进键是本次修复的核心——同城同区不同频道的两个玩家
// 只有落在不同的桶里，才会自然地互相看不见、不互相广播。
type areaKey struct {
	TownID             byte
	AreaID             byte
	ChannelID          int
	Instance           uint32
	RaidBattleInstance string
}

// currentTownPresenceChannelID 返回会话当前所在频道的**完整**数值。
//
// 回落顺序与 character_protocol_constants.go 的 currentClientSelectedChannel 一致
// （会话频道优先，其次连接期已提交的 town actor owner context），但**不截断成 u8**：
// 分桶键必须区分频道 256 与"未绑定"的 0，而 u8 会把它们合并。
func currentTownPresenceChannelID(session *gameSession) int {
	if session == nil {
		return 0
	}
	if channelID := session.channel.ID; channelID != 0 {
		return channelID
	}
	return int(currentConnectionTownActorOwnerContext(session))
}

// resolvePresenceChannelID 决定一次 EnterArea 登记应该落在哪个频道桶。
//
// 权威来源是**会话本身**（一个 game 连接只属于一个频道端口），而不是调用方填的字段：
// 报障的根因正是"某个登记点漏了频道"，所以这里不允许漏——只要带了 Session 就一定
// 取到真实频道。显式 ChannelID 只用于没有 Session 的登记（测试夹具）。
func resolvePresenceChannelID(info *onlinePlayerInfo) int {
	if info == nil {
		return 0
	}
	if info.Session != nil {
		if channelID := currentTownPresenceChannelID(info.Session); channelID != 0 {
			return channelID
		}
	}
	return info.ChannelID
}

// OnlinePlayerManager tracks online players grouped by (TownID, AreaID)
// and provides broadcast capabilities for town multiplayer visibility.
type OnlinePlayerManager struct {
	mu     sync.RWMutex
	byArea map[areaKey]map[uint16]*onlinePlayerInfo // areaKey -> characterID -> info
	byChar map[uint16]areaKey                       // characterID -> current area
}

func newOnlinePlayerManager() *OnlinePlayerManager {
	return &OnlinePlayerManager{
		byArea: make(map[areaKey]map[uint16]*onlinePlayerInfo),
		byChar: make(map[uint16]areaKey),
	}
}

// EnterArea registers a player in an area and returns the list of other
// players already in that area (for sending the full area list to the newcomer).
func (m *OnlinePlayerManager) EnterArea(info *onlinePlayerInfo) []onlinePlayerInfo {
	if m == nil || info == nil || info.CharacterID == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := areaKey{
		TownID:             info.TownID,
		AreaID:             info.AreaID,
		ChannelID:          resolvePresenceChannelID(info),
		Instance:           info.Instance,
		RaidBattleInstance: info.RaidBattleInstance,
	}
	info.ChannelID = key.ChannelID

	// Remove from previous area if any.
	m.removeFromAreaLocked(info.CharacterID)

	// Add to new area.
	if m.byArea[key] == nil {
		m.byArea[key] = make(map[uint16]*onlinePlayerInfo)
	}
	m.byArea[key][info.CharacterID] = info
	m.byChar[info.CharacterID] = key

	// Collect other players in the area.
	others := make([]onlinePlayerInfo, 0, len(m.byArea[key])-1)
	for id, other := range m.byArea[key] {
		if id != info.CharacterID {
			others = append(others, *other)
		}
	}
	return others
}

// LeaveArea removes a player from their current area and returns the list of
// remaining players (for broadcasting the leave notification).
func (m *OnlinePlayerManager) LeaveArea(characterID uint16) (areaKey, []onlinePlayerInfo) {
	if m == nil || characterID == 0 {
		return areaKey{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key, ok := m.byChar[characterID]
	if !ok {
		return areaKey{}, nil
	}
	m.removeFromAreaLocked(characterID)

	// Collect remaining players.
	others := make([]onlinePlayerInfo, 0, len(m.byArea[key]))
	for _, other := range m.byArea[key] {
		others = append(others, *other)
	}
	return key, others
}

// LeaveAreaSession removes presence only when the disconnecting transport is
// still the transport that registered the town actor.  The current client
// opens short-lived same-character auxiliary game connections (for example
// the cross-channel/raid directory probe); those connections must not evict
// the resident town connection when they close.
func (m *OnlinePlayerManager) LeaveAreaSession(characterID uint16, session *gameSession) (areaKey, []onlinePlayerInfo, bool) {
	if m == nil || characterID == 0 || session == nil {
		return areaKey{}, nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key, ok := m.byChar[characterID]
	if !ok {
		return areaKey{}, nil, false
	}
	area := m.byArea[key]
	current := area[characterID]
	if current == nil || current.Session != session {
		return areaKey{}, nil, false
	}
	m.removeFromAreaLocked(characterID)
	others := make([]onlinePlayerInfo, 0, len(m.byArea[key]))
	for _, other := range m.byArea[key] {
		others = append(others, *other)
	}
	return key, others, true
}

func (m *OnlinePlayerManager) SessionForCharacter(characterID uint16) *gameSession {
	if m == nil || characterID == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	key, ok := m.byChar[characterID]
	if !ok {
		return nil
	}
	info := m.byArea[key][characterID]
	if info == nil {
		return nil
	}
	return info.Session
}

func (m *OnlinePlayerManager) PlayerForCharacter(characterID uint16) (onlinePlayerInfo, bool) {
	if m == nil || characterID == 0 {
		return onlinePlayerInfo{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	key, ok := m.byChar[characterID]
	if !ok {
		return onlinePlayerInfo{}, false
	}
	info := m.byArea[key][characterID]
	if info == nil {
		return onlinePlayerInfo{}, false
	}
	return *info, true
}

// UpdatePosition updates a player's position and returns the list of other
// players in the same area (for broadcasting the move notification).
func (m *OnlinePlayerManager) UpdatePosition(characterID uint16, posX, posY uint16) []onlinePlayerInfo {
	if m == nil || characterID == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key, ok := m.byChar[characterID]
	if !ok {
		return nil
	}
	info := m.byArea[key][characterID]
	if info == nil {
		return nil
	}
	info.PositionX = posX
	info.PositionY = posY

	others := make([]onlinePlayerInfo, 0, len(m.byArea[key])-1)
	for id, other := range m.byArea[key] {
		if id != characterID {
			others = append(others, *other)
		}
	}
	return others
}

// GetAreaPlayers returns all players in a specific shared area **on one channel**.
//
// 两个收窄条件都必须显式给出：
//   - Instance 固定 0：共享区域查询不包含私有房间（赛丽亚的房间等），私有房间只能
//     通过拥有者自己的会话访问；
//   - ChannelID 必须匹配调用方所在频道：跨频道玩家不算同一个区域，否则 op24 名册会
//     把别的频道的角色塞进本地场景（这正是"10 线能见到 121 线"的另一半）。
func (m *OnlinePlayerManager) GetAreaPlayers(townID, areaID byte, channelID int) []onlinePlayerInfo {
	return m.GetAreaScopePlayers(townID, areaID, channelID, 0, "")
}

func (m *OnlinePlayerManager) GetAreaScopePlayers(townID, areaID byte, channelID int, instance uint32, battleID string) []onlinePlayerInfo {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	key := areaKey{TownID: townID, AreaID: areaID, ChannelID: channelID, Instance: instance, RaidBattleInstance: battleID}
	players := make([]onlinePlayerInfo, 0, len(m.byArea[key]))
	for _, info := range m.byArea[key] {
		players = append(players, *info)
	}
	return players
}

// PeerInSameArea verifies that both characters are registered in the same
// concrete town area **on the same channel**. A visible remote actor is only
// interactive inside this scope: an online session in another area *or another
// channel* is not a valid peer target (it is not drawn on this client at all).
//
// 组队闸门也一律用这条判据：口径是**彻底隔离——跨频道完全不能组队**
// （操作者 2026-09-24 拍板）。城镇同屏与组队都只限同频道。
func (m *OnlinePlayerManager) PeerInSameArea(sourceID, targetID uint16) bool {
	if m == nil || sourceID == 0 || targetID == 0 || sourceID == targetID {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	sourceKey, sourceOK := m.byChar[sourceID]
	targetKey, targetOK := m.byChar[targetID]
	if !sourceOK || !targetOK || sourceKey != targetKey {
		return false
	}
	area := m.byArea[sourceKey]
	return area != nil && area[sourceID] != nil && area[targetID] != nil
}

// townPresenceAreaText 把角色此刻在城镇在线表里的分桶键渲染成一行诊断文本
// （`town/area/channel/instance`）；found=false = 这个角色此刻没有存在记录。
//
// 它读的就是 `PeerInSameArea` 判据用的那张表与那个键（town/area/channel/instance），
// 所以日志里并排出现的两行就是那道闸门的现场证据：两行不同 = 客户端场景里没有对方
// actor，邀请提示送过去也画不出原生框（`instance != 0` 即"每角色一份"的私有落点房，
// 此时同城同区也不算同桶，见 town_copresence_actor_resend.go 的口径注释）。
func (s *Service) townPresenceAreaText(characterID uint16) (string, bool) {
	if s == nil || s.onlinePlayers == nil || characterID == 0 {
		return "", false
	}
	info, found := s.onlinePlayers.PlayerForCharacter(characterID)
	if !found {
		return "", false
	}
	return fmt.Sprintf("%d/%d/ch%d/inst%d", info.TownID, info.AreaID, info.ChannelID, info.Instance), true
}

// partyMemberChannelMismatchReason 是"跨频道组队被拒"的统一原因串：邀请、接受、
// 直接入队、队伍目录快速加入、整队进副本、队员选择器同步全部记这一个 reason，
// 复测时 `grep -a party_member_channel_mismatch` 就能看到每一次拒绝。
const partyMemberChannelMismatchReason = "party_member_channel_mismatch"

// partyMemberAreaMismatchReason 是"**同频道但不同场景桶**"的拒绝原因：判据
// `PeerInSameArea` 比的是城镇在线表的四元桶键（town/area/channel/instance），
// 所以频道相同也可能不同桶（典型：一方在每角色一份的私有落点房里）。
// 2026-10-02 (V22) 之前这种情况被统一写成频道不符，日志里出现过两个相同的频道号，
// 误导复测；现在按真实差异分流。
const partyMemberAreaMismatchReason = "party_member_area_mismatch"

// partyChannelMismatch 报告两个会话是否处在**不同频道**。
//
// 口径（操作者 2026-09-24 拍板）：**彻底隔离——跨频道完全不能组队**。
// 城镇可见性、同屏广播、op24 名册、分解机店铺、交易/查看信息等交互一律只限同频道；
// 组队（邀请 request peer、接受 response peer、直接入队 entry into party、队伍目录
// 快速加入、整队进副本、队员选择器同步、回城队友 actor 投影）同样只限同频道，
// 跨频道一律**明确拒绝并回失败包**，不静默、也不建出跨频道队伍。
//
// 频道取的是会话自己的传输频道（game 连接本地端口对应的 channelcatalog 项），
// 与分桶键用的是同一个值，所以"能看见"和"能组队"永远是同一个集合。
func (s *Service) partyChannelMismatch(first, second *gameSession) bool {
	if first == nil || second == nil {
		return false
	}
	return currentTownPresenceChannelID(first) != currentTownPresenceChannelID(second)
}

// PeersInSameArea returns a stable snapshot of every other player registered
// beside characterID. Callers use it for appearance-only projections that do
// not change movement state or area ownership.
func (m *OnlinePlayerManager) PeersInSameArea(characterID uint16) []onlinePlayerInfo {
	if m == nil || characterID == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	key, ok := m.byChar[characterID]
	if !ok {
		return nil
	}
	area := m.byArea[key]
	peers := make([]onlinePlayerInfo, 0, len(area))
	for id, info := range area {
		if id == characterID || info == nil {
			continue
		}
		peers = append(peers, *info)
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].CharacterID < peers[j].CharacterID
	})
	return peers
}

func (m *OnlinePlayerManager) removeFromAreaLocked(characterID uint16) {
	key, ok := m.byChar[characterID]
	if !ok {
		return
	}
	delete(m.byChar, characterID)
	if area := m.byArea[key]; area != nil {
		delete(area, characterID)
		if len(area) == 0 {
			delete(m.byArea, key)
		}
	}
}

// --- Broadcast packet builders ---
//
// IDA-verified wire formats (current 23.4.15.0 NoPack EXE):
//
//	NOTI 0x0016 (sub_1D83990): u16 actorKey, u16 x, u16 y, u8 direction, u16 movementRate
//	NOTI 0x0017 (sub_1D89590): u16 actorKey, u8 town, u8 area, u16 x, u16 y, u8 areaState, u8 direction
//	NOTI 0x0006 (C# ref):      u16 userId

const (
	currentTownAreaUserListMsgID uint16 = 0x0018
	currentTownUserLeaveMsgID    uint16 = 0x0006
)

// buildTownAreaUserListBody builds the current EXE sub_1D901D0 layout:
// u8 town, u8 area, u16 count, repeat(u16 actorKey, u16 x, u16 y, u8 dir, u8 state).
//
// This is more than a co-presence roster. The final row for the selected
// character rebinds the local scene actor used by town camera tracking.
func buildTownAreaUserListBody(townID, areaID byte, players []onlinePlayerInfo) []byte {
	count := len(players)
	if count > 0xFFFF {
		count = 0xFFFF
	}
	body := make([]byte, 4+count*8)
	body[0] = townID
	body[1] = areaID
	binary.LittleEndian.PutUint16(body[2:4], uint16(count))
	for i := 0; i < count; i++ {
		offset := 4 + i*8
		binary.LittleEndian.PutUint16(body[offset:offset+2], currentSceneActorObjectKey(players[i].CharacterID))
		binary.LittleEndian.PutUint16(body[offset+2:offset+4], players[i].PositionX)
		binary.LittleEndian.PutUint16(body[offset+4:offset+6], players[i].PositionY)
		body[offset+6] = players[i].Direction
		body[offset+7] = players[i].AreaState
	}
	return body
}

// buildTownUserLeaveBody builds NOTI 0x0006 body: u16 userId.
func buildTownUserLeaveBody(characterID uint16) []byte {
	body := make([]byte, 2)
	binary.LittleEndian.PutUint16(body, characterID)
	return body
}

// currentTownRemoteActorOwnerContext returns the receiving session's committed
// town-channel owner. Current NoPack sub_2009160 treats an owner equal to the
// resident town channel as a global scene actor and creates it by its distinct
// uint16 object key through sub_20036C0. A character-ID-derived owner instead
// selects the auxiliary context-object branch; that object can be drawn, but it
// is absent from the native global table used by player interaction menus.
func currentTownRemoteActorOwnerContext(target *gameSession, _ uint16) byte {
	return currentTownActorOwnerContext(target)
}

// sendTownRemoteActorState installs one repository-backed peer actor in the
// target client's town scene. 86JP establishes co-presence with both appearance
// and full user-info records before the area-state notification. The current
// 90CN client uses its typed mode0/mode1 equivalents instead of 86JP's legacy
// USERINFO subtype0/subtype1 bodies.
func (s *Service) sendTownRemoteActorState(
	target *gameSession,
	actor onlinePlayerInfo,
	source string,
) error {
	return s.sendTownRemoteActorStateWithPartyFrame(target, actor, source, true)
}

// FiendWar admission restores actors before its first endpoint/roster sequence.
// That caller must not publish a peer party frame before op11 seeds endpoints.
// Ordinary town projections retain their existing party-frame policy.
func (s *Service) sendTownRemoteActorStateWithPartyFrame(
	target *gameSession,
	actor onlinePlayerInfo,
	source string,
	partyFrame bool,
) error {
	if target == nil || target.conn == nil || actor.Session == nil || actor.CharacterID == 0 {
		return nil
	}
	targetCharacterID := target.selectedCharacterID

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	ctx, err := s.currentRemoteActorEquipmentSnapshot(ctx, actor.Session, actor.CharacterID)
	if err != nil {
		return err
	}
	charID, charName, character, hasCharacter := s.characterForEnter(
		ctx,
		actor.Session,
		actor.CharacterID,
	)
	if charID != actor.CharacterID || !hasCharacter {
		return fmt.Errorf(
			"remote town actor repository record unavailable: requested=%d loaded=%d found=%t",
			actor.CharacterID,
			charID,
			hasCharacter,
		)
	}

	remoteOwner := currentTownRemoteActorOwnerContext(target, charID)
	if remoteOwner == currentSceneObjectContext {
		return fmt.Errorf("remote town actor owner channel is not committed")
	}
	mode0Body, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(
		ctx,
		actor.Session,
		charID,
		charName,
		character,
		hasCharacter,
		remoteOwner,
	)
	if err != nil {
		return fmt.Errorf("build remote town actor mode0: %w", err)
	}
	adventureSummary := s.currentAccountAdventureGroupSummaryForPacket(
		ctx,
		actor.Session,
		character,
		hasCharacter,
	)
	if peerSkillPageTownEnabled() {
		// Mark this ctx as "the body built from it goes to a peer" so the mode1 tail
		// carries the real skill/switch page (same gate the dungeon-party path uses).
		ctx = withDungeonPartyPeerSkillPageTarget(ctx, charID)
	}
	if peerSkillPageTownEnabled() {
		// 城镇同屏（game-town-copresence-actor-state-send）这条链此前没有打"发给对端"的
		// 标记，于是 mode1 尾块的技能页槽写成 0xff/0/0（空）⇒ 玩家在城镇里点别人看不到
		// 对方的 buff 强化/换装页。这里只为【被同屏的那个人】的 body 打标记，
		// 与队友那条链（dungeon_party_copresence.go）同构；默认档关闭 ⇒ 逐字节等于现状。
		ctx = withDungeonPartyPeerSkillPageTarget(ctx, charID)
	}
	mode1Body := s.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		ctx,
		actor.Session,
		character,
		hasCharacter,
		charID,
		uint32(adventureSummary.ManageLevel),
		remoteOwner,
		true,
	)
	s.logPacketEvent("game-town-copresence-actor-state-send",
		"conn_id", target.connID,
		"channel_id", target.channel.ID,
		"source", source,
		"target_char_id", target.selectedCharacterID,
		"target_local_owner", currentTownActorOwnerContext(target),
		"actor_char_id", charID,
		// actor_channel_id 是"被投影的 actor 自己所在的频道"。频道隔离的判定式：
		// source 以 town_copresence_ 开头的同屏投影必须 channel_id == actor_channel_id；
		// 只有 town_remote_party_actor_* 这类"异地区队友"允许两者不同（本来就不在同一场景）。
		"actor_channel_id", actor.ChannelID,
		"object_key", currentSceneActorObjectKey(charID),
		"remote_owner", remoteOwner,
		"actor_job", numericCharacterStat(character.Job),
		"actor_grow_type", characterGrowTypeStat(character),
		"actor_level", character.Level,
		"mode0_body_len", len(mode0Body),
		"mode1_body_len", len(mode1Body),
		"body_source", "current_exe_mode0_create_then_mode1_bind_then_same_owner_op9_party_state_before_area_state")
	// 判读事件：城镇同屏走的是与副本队友投影**同一个** mode0 外观载体，所以队友
	// 时装缺链接这件事在城里同样成立（party_peer_appearance.go 文件头）。
	//
	// subtype0Body 是第二条外观载体（被投影角色自己的 USERINFO subtype0 外观列表）；
	// 厂商的同屏口径就是这一条（TownHandler.cs:502-515），见
	// party_peer_appearance_subtype0.go 的文件头。
	subtype0Body := s.peerActorUserInfoSubtype0BodyForProjection(
		ctx, target, actor.Session, character, hasCharacter, charID, charName, mode0Body,
	)
	if target.selectedCharacterID != targetCharacterID || actor.Session.selectedCharacterID != charID {
		return fmt.Errorf("remote town actor selection changed during construction: actor=%d receiver=%d", charID, targetCharacterID)
	}
	s.logCurrentPeerActorAppearanceEvent(target, actor.Session, source, mode0Body, mode1Body, subtype0Body)
	if err := s.sendGameUpperRawClass(
		target,
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		mode0Body,
		0,
	); err != nil {
		return fmt.Errorf("send remote town actor mode0: %w", err)
	}
	if err := s.sendGameUpperRawClass(
		target,
		uint16(dnfenum.CmdPacketSetUDPIPPort),
		mode1Body,
		0,
	); err != nil {
		return fmt.Errorf("send remote town actor mode1: %w", err)
	}
	if len(subtype0Body) != 0 {
		s.sendPeerUserInfoSubtype0Appearance(target, subtype0Body, source+"_peer_userinfo_subtype0", charID)
	}
	if len(subtype0Body) != 0 && currentSceneObjectAppearanceBodyHasCloneLink(mode0Body) {
		if err := s.sendGameUpperRawClass(target, uint16(dnfenum.CmdPacketSetUDPIPPort), mode0Body, 0); err != nil {
			return err
		}
		s.logGameEvent(target, "game-party-clone-appearance-after-equipment", "peer_char_id", charID, "source", source)
	}
	// Current EXE sub_1D64CA0 kind0 does not create an actor. Resolve and send
	// the actor's actual runtime party state only after mode0 globally created
	// and mode1 bound the remote object. An empty state clears stale join-party
	// menu state; an explicitly created solo party remains joinable.
	//
	// 2026-09-27：这里**不能**无条件用会话本地缓存 —— 它经常是空的（同角色第二条连接、
	// 替换 socket、非发布路径都读不到权威名单），于是给在场那位补发的是"这名队友不在队伍里"，
	// 客户端据此判定队伍无效并**自己 op13 退队**（四步单变量二分的结论，见
	// town_copresence_party_state.go）。official-24.9 默认使用权威名单，显式 off 可回退。
	//
	// 另有 p14/p15 两次对照证明：当观察方与该 newcomer 本来就在同一支队伍时，
	// 这条"再声明一次队伍"的 op9 本身就是触发客户端退队的那一下（换成权威内容仍然退队）
	// ⇒ 该情形下跳过它。见 town_copresence_party_state.go 的
	// `DNFBRIDGE_TOWN_COPRESENCE_SKIP_SAME_PARTY_FRAME`（official-24.9 城镇默认开，其他档默认 off）。
	if !partyFrame {
		return nil
	}
	if s.shouldSkipTownCopresencePartyFrame(target, actor.Session) {
		s.logPacketEvent("game-town-copresence-party-frame-skipped",
			"conn_id", target.connID,
			"channel_id", target.channel.ID,
			"source", source,
			"target_char_id", target.selectedCharacterID,
			"actor_char_id", charID,
			"object_key", currentSceneActorObjectKey(charID),
			"reason", "observer_and_actor_share_the_same_runtime_party",
			"switch", townCopresenceSkipSamePartyFrameEnv)
		return nil
	}
	if _, err := s.sendCurrentPartyActorFrameProjection(
		target,
		actor.Session,
		s.townCopresenceActorPartyState(actor.Session),
		"town_copresence_actor_initial_party_state",
	); err != nil {
		return fmt.Errorf("send remote town actor op9 display: %w", err)
	}
	return nil
}

// rebindTownCopresenceAfterDungeonReturn re-registers a session that came back
// from a dungeon into the town area it just returned to.
//
// The dungeon return path writes its own typed op24 transition and therefore
// never goes through SET_USER_AREA, which is the only place that registers a
// player in the online-player manager. Without this step the server still
// believes the player is in the dungeon area, the copresence broadcast never
// runs, and the two players cannot see each other until somebody manually
// crosses a town area boundary again (the reported "出图后互相看不见，要走一段
// 城镇地图才看得到").
func (s *Service) rebindTownCopresenceAfterDungeonReturn(
	session *gameSession,
	townID byte,
	areaID byte,
	source string,
	expected ...*currentSelectorReturnPresence,
) bool {
	if s == nil || s.onlinePlayers == nil || session == nil || session.selectedCharacterID == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID, _, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	if characterID == 0 {
		characterID = session.selectedCharacterID
	}
	if !hasCharacter {
		s.logGameEvent(session, "game-dungeon-return-copresence-deferred",
			"char_id", characterID,
			"town_id", townID,
			"area_id", areaID,
			"source", source,
			"reason", "character_unavailable")
		return false
	}
	session.townMu.Lock()
	position := session.townPositionSnapshot
	session.townMu.Unlock()
	areaState := byte(currentActorDefaultUserStateBits)
	if value := numericCharacterStatValue(character, "area_state"); value >= 0 && value <= 0xff {
		areaState = byte(value)
	}
	// 私有房间（赛丽亚的房间）在登录/出图重绑这条路径上同样要按角色隔离：
	// 目标区域能解析出地图路径时套用同一条判据，解析不到就按共享区域处理（与城镇过图一致）。
	areaInstance := uint32(0)
	if area, found := s.townArea(int64(townID), int64(areaID)); found {
		var err error
		areaInstance, err = s.currentTownAreaInstance(ctx, session, character, area.MapPath)
		if err != nil {
			s.logGameEvent(session, "game-dungeon-return-guild-hall-rejected", "source", source, "error", err)
			s.retireCurrentGuildHallPresence(session)
			return false
		}
	}
	raidBattle := ""
	playerInfo := &onlinePlayerInfo{
		CharacterID: characterID,
		AccountID:   character.AccountID,
		Name:        character.Name,
		Job:         byte(numericCharacterStat(character.Job)),
		GrowType:    characterGrowTypeByte(character),
		Level:       byte(character.Level),
		TownID:      townID,
		AreaID:      areaID,
		// 出图重绑同样按**本会话的频道**落桶：出副本回城是一次跨场景重建，
		// 若这里回落到 0 或沿用副本期的上下文，回城后的存在会跑进别的频道桶
		// （表现为"回城后同频道的队友看不见本人 / 别频道的人突然出现"）。
		ChannelID:          currentTownPresenceChannelID(session),
		Instance:           areaInstance,
		RaidBattleInstance: raidBattle,
		PositionX:          position.PositionX,
		PositionY:          position.PositionY,
		Direction:          position.MovementCode,
		AreaState:          areaState,
		Session:            session,
	}
	partyStateBefore := runtimePartyStateSnapshot(session)
	// 回城这一趟必须先**提交本会话的 town actor owner 上下文**，再做同屏投影。
	//
	// 现场证据（2026-09-25 03:52 双开，频道 10，packet_log.txt:3376）：
	//
	//	game-town-copresence-actor-state-failed conn_id=game-000021 channel_id=10
	//	source=town_copresence_actor_state_existing_to_newcomer actor_char_id=3
	//	error=remote town actor owner channel is not committed
	//
	// 同一个回城窗口给 game-000017 那一条是成功的（`target_local_owner=10`）⇒ 先回城的
	// 那个人拿不到对方的 actor，操作者看到的就是"回城后队长看不到队员、队员还能看到
	// 队长"的单向丢失。根因是 `sendCurrentCompletedDungeonReturnToTown` 只在
	// `Run.Status == DungeonRunCompleted`（通关回城）的首次尝试里把
	// `townActorOwnerChannel` 设成连接频道；**放弃副本/直接返回城镇**走的是
	// op42 → 这里，那个字节还停在副本期的 0，而 `sendTownRemoteActorState` 明确
	// fail-closed（owner==0 ⇒ 不建 actor）。
	//
	// 这里补上与登录/通关回城同一个真源（`currentConnectionTownActorOwnerContext`
	// = 连接期提交的频道）的提交动作；已经提交过的正常回城路径因此逐字节不变。
	if currentTownActorOwnerContext(session) == currentSceneObjectContext {
		session.townMu.Lock()
		session.townActorOwnerChannel = currentConnectionTownActorOwnerContext(session)
		session.townMu.Unlock()
	}
	raidInstance, raidBattle, admitted, releaseRaid := s.lockCurrentAntonWaitingAreaScope(session, townID, areaID)
	if !admitted {
		return false
	}
	if len(expected) > 0 {
		owner := expected[0]
		if owner == nil || owner.raidKey != raidInstance || owner.battleID != raidBattle ||
			owner.channelID != session.channel.ID || owner.identity.character != characterID ||
			!s.boundGameSessionCharacterCurrent(owner.identity) {
			releaseRaid()
			return false
		}
	}
	if raidInstance != 0 {
		playerInfo.Instance = raidInstance
	}
	playerInfo.RaidBattleInstance = raidBattle
	_, oldOthers := s.onlinePlayers.LeaveArea(characterID)
	newOthers := s.onlinePlayers.EnterArea(playerInfo)
	releaseRaid()
	if len(oldOthers) > 0 {
		s.broadcastTownPlayerAreaChange(playerInfo, oldOthers, partyStateBefore)
	}
	s.broadcastTownPlayerEnter(playerInfo, newOthers)
	partyStateAfter := s.townCopresenceActorPartyState(session)
	if partyStateAfter.PartyID > 0 {
		if err := s.sendTownRemotePartyActors(session, partyStateAfter); err != nil {
			s.logGameEvent(session, "game-dungeon-return-copresence-roster-failed",
				"char_id", characterID,
				"party_id", partyStateAfter.PartyID,
				"source", source,
				"error", err)
		}
		if err := s.sendRuntimePartyRosterLocal(session, partyStateAfter, source+"_dungeon_return_roster_rebind"); err != nil {
			s.logGameEvent(session, "game-dungeon-return-copresence-roster-failed",
				"char_id", characterID,
				"party_id", partyStateAfter.PartyID,
				"source", source,
				"error", err)
		}
		for i := range newOthers {
			peer := newOthers[i].Session
			if peer == nil {
				continue
			}
			peerState := s.townCopresenceActorPartyState(peer)
			if err := s.sendRuntimePartyRosterLocal(peer, peerState, source+"_dungeon_return_peer_roster_rebind"); err != nil {
				continue
			}
		}
	}
	s.logGameEvent(session, "game-dungeon-return-copresence-rebound",
		"char_id", characterID,
		"town_id", townID,
		"area_id", areaID,
		"area_key_channel", currentTownPresenceChannelID(session),
		"area_instance", areaInstance,
		"same_area_peers", len(newOthers),
		"party_id", partyStateAfter.PartyID,
		"source", source)
	return true
}

// broadcastTownPlayerEnter sends each remote actor's current typed mode0,
// equipment-bearing mode1, same-owner op9 display registration, then
// incremental 0x0017 state. The town transition owner has already sent one
// complete authoritative op24 packet; replaying a second self-containing op24
// here rebinds the local actor and breaks camera ownership.
func (s *Service) broadcastTownPlayerEnter(newPlayer *onlinePlayerInfo, others []onlinePlayerInfo) {
	if s == nil || s.onlinePlayers == nil || newPlayer == nil || newPlayer.Session == nil {
		return
	}
	peers := append([]onlinePlayerInfo(nil), others...)
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].CharacterID < peers[j].CharacterID
	})
	actorKey := currentSceneActorObjectKey(newPlayer.CharacterID)

	// To each existing player: newcomer's mode0/mode1/op9, then 0x0017.
	enterBody := buildCurrentTownUserAreaNotificationBody(
		actorKey, newPlayer.TownID, newPlayer.AreaID,
		newPlayer.PositionX, newPlayer.PositionY,
		newPlayer.Direction, newPlayer.AreaState,
	)
	for i := range peers {
		if peers[i].Session == nil || (s.currentAntonWaitingArea(newPlayer.TownID, newPlayer.AreaID) && !s.onlinePlayers.PeerInSameArea(newPlayer.CharacterID, peers[i].CharacterID)) {
			continue
		}
		if err := s.sendTownRemoteActorState(
			peers[i].Session,
			*newPlayer,
			"town_copresence_actor_state_newcomer_to_existing",
		); err != nil {
			s.logGameEvent(peers[i].Session, "game-town-copresence-actor-state-failed",
				"source", "town_copresence_actor_state_newcomer_to_existing",
				"actor_char_id", newPlayer.CharacterID,
				"error", err)
			continue
		}
		_ = s.sendCurrentSceneFixedClass0Packet(peers[i].Session,
			currentTownUserAreaNotificationMsgID, enterBody,
			"town_copresence_enter_0x0017_to_existing")
	}

	// To newcomer: each existing player's mode0/mode1/op9, then 0x0017.
	for i := range peers {
		if peers[i].Session == nil || (s.currentAntonWaitingArea(newPlayer.TownID, newPlayer.AreaID) && !s.onlinePlayers.PeerInSameArea(newPlayer.CharacterID, peers[i].CharacterID)) {
			continue
		}
		if err := s.sendTownRemoteActorState(
			newPlayer.Session,
			peers[i],
			"town_copresence_actor_state_existing_to_newcomer",
		); err != nil {
			s.logGameEvent(newPlayer.Session, "game-town-copresence-actor-state-failed",
				"source", "town_copresence_actor_state_existing_to_newcomer",
				"actor_char_id", peers[i].CharacterID,
				"error", err)
			continue
		}
		otherKey := currentSceneActorObjectKey(peers[i].CharacterID)
		otherBody := buildCurrentTownUserAreaNotificationBody(
			otherKey, peers[i].TownID, peers[i].AreaID,
			peers[i].PositionX, peers[i].PositionY,
			peers[i].Direction, peers[i].AreaState,
		)
		_ = s.sendCurrentSceneFixedClass0Packet(newPlayer.Session,
			currentTownUserAreaNotificationMsgID, otherBody,
			"town_copresence_enter_0x0017_existing_to_newcomer")
	}

	// 城镇入场路径的公共出口（op36 SET_USER_AREA / 赛丽亚房间出房门 / 出副本回城重绑）
	// 在这里给新来的那个人补一次"本区域已经摆好的分解机/附魔师门店"投影。
	// 缺陷取证与回退开关见 expert_job_store.go 文件头。
	s.projectCurrentExpertJobStoresToNewcomer(
		newPlayer.Session,
		newPlayer,
		"town_area_entry_store_projection",
	)
}

// sendTownRemotePartyActors restores party members that are online in another
// town area after SET_USER_AREA rebuilt the receiving client's actor manager.
// Current sub_1D64CA0's same-owner party rows contain only user ids, so every
// referenced user must first exist through the normal mode0/mode1/op9 actor
// path. The following op23 carries the member's real town/area and position;
// it does not claim co-presence in the receiver's destination area.
//
// 频道隔离（彻底隔离口径）：**跨频道队友完全不投影进本地城镇场景**。
// op23 只携带 (town, area, 位置)、没有频道字段，对跨频道队友发它等于把他声明成
// 本地区域的一员；mode0/mode1 还会在本地创建他的 actor。他在队伍框里的条目由
// op9 名单的跨频道身份块负责，不需要城镇 actor。
func (s *Service) sendTownRemotePartyActors(receiver *gameSession, state alignedcmd.PartyState) error {
	if s == nil || s.onlinePlayers == nil || receiver == nil || receiver.selectedCharacterID == 0 || state.PartyID <= 0 {
		return nil
	}
	receiverChannelID := currentTownPresenceChannelID(receiver)
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == 0 || member.UserID == receiver.selectedCharacterID {
			continue
		}
		actor, ok := s.onlinePlayers.PlayerForCharacter(member.UserID)
		if !ok || actor.Session == nil {
			continue
		}
		if actor.ChannelID != receiverChannelID {
			continue
		}
		if s.townPrivateRoomPeerProjectionBlocked(receiver, actor) {
			continue
		}
		// A party actor fallback must not recreate another raid's actor inside
		// the same physical waiting map after the ordinary roster was isolated.
		if source, found := s.onlinePlayers.PlayerForCharacter(receiver.selectedCharacterID); found &&
			s.currentAntonWaitingArea(source.TownID, source.AreaID) && s.currentAntonWaitingArea(actor.TownID, actor.AreaID) &&
			(source.Instance != actor.Instance || source.RaidBattleInstance != actor.RaidBattleInstance) {
			continue
		}
		if source, found := s.onlinePlayers.PlayerForCharacter(receiver.selectedCharacterID); found &&
			s.currentGuildHallArea(source.TownID, source.AreaID) && s.currentGuildHallArea(actor.TownID, actor.AreaID) &&
			(source.Instance == 0 || source.Instance != actor.Instance) {
			continue
		}
		if s.onlinePlayers.PeerInSameArea(receiver.selectedCharacterID, member.UserID) {
			continue
		}
		if err := s.sendTownRemoteActorState(receiver, actor, "town_remote_party_actor_after_set_user_area"); err != nil {
			return err
		}
		areaBody := buildCurrentTownUserAreaNotificationBody(
			currentSceneActorObjectKey(actor.CharacterID),
			actor.TownID,
			actor.AreaID,
			actor.PositionX,
			actor.PositionY,
			actor.Direction,
			actor.AreaState,
		)
		if err := s.sendCurrentSceneFixedClass0Packet(
			receiver,
			currentTownUserAreaNotificationMsgID,
			areaBody,
			"town_remote_party_actor_real_area_after_state",
		); err != nil {
			return err
		}
	}
	return nil
}

// NOTI23 carries town/area but no private-instance key. Recreating a party
// actor in the same physical Seria map would therefore put another owner's
// actor inside the receiver's room. Keep the party roster and remote actors
// in different physical areas; only this ambiguous private-room projection
// is blocked, including stale shared presence left before a room rebind.
func (s *Service) townPrivateRoomPeerProjectionBlocked(receiver *gameSession, actor onlinePlayerInfo) bool {
	if s == nil || s.onlinePlayers == nil || receiver == nil ||
		receiver.selectedCharacterID == 0 || actor.CharacterID == receiver.selectedCharacterID {
		return false
	}
	local, found := s.onlinePlayers.PlayerForCharacter(receiver.selectedCharacterID)
	if !found || local.Session != receiver || local.TownID != actor.TownID || local.AreaID != actor.AreaID {
		return false
	}
	area, found := s.townArea(int64(local.TownID), int64(local.AreaID))
	return found && townMapPathLooksLikeSeria(area.MapPath)
}

// broadcastTownPlayerLeave sends NOTI 0x0006 (u16 userId) to remaining
// players so they remove the departed player's town actor.
func (s *Service) broadcastTownPlayerLeave(characterID uint16, others []onlinePlayerInfo) {
	if s == nil || s.onlinePlayers == nil || len(others) == 0 {
		return
	}
	leaveBody := buildTownUserLeaveBody(characterID)
	for i := range others {
		if others[i].Session != nil {
			_ = s.sendCurrentSceneFixedClass0Packet(others[i].Session,
				currentTownUserLeaveMsgID, leaveBody,
				"town_copresence_leave_0x0006")
		}
	}
}

// broadcastTownPlayerAreaChange removes an ordinary old-area actor, but keeps
// an active party member bound to the stationary client's party manager. The
// current client treats op6 as both a town-actor destroy and a party-member
// removal. For party peers the native op23 area update is sufficient: it moves
// the existing actor out of the rendered area while retaining the roster and
// minimap identity. Recreating that actor after op6 is unsafe because the
// client answers the synthetic cross-area rebuild with LEAVE_PARTY.
func (s *Service) broadcastTownPlayerAreaChange(
	mover *onlinePlayerInfo,
	oldOthers []onlinePlayerInfo,
	state alignedcmd.PartyState,
) {
	if s == nil || mover == nil || len(oldOthers) == 0 {
		return
	}
	// The caller snapshot can be empty or stale on replacement sockets. Resolve the
	// mover's party from the same central state used by town actor projections so
	// a current same-party cross-area move never falls into the destructive op6
	// branch, while a genuinely stale/old party still does.
	if mover.Session != nil {
		state = s.townCopresenceActorPartyState(mover.Session)
	}
	partyMembers := make(map[uint16]struct{}, len(runtimePartyMembers(state)))
	if state.PartyID > 0 {
		for _, member := range runtimePartyMembers(state) {
			if member.UserID != 0 && member.UserID != mover.CharacterID {
				partyMembers[member.UserID] = struct{}{}
			}
		}
	}
	leaveBody := buildTownUserLeaveBody(mover.CharacterID)
	areaBody := buildCurrentTownUserAreaNotificationBody(
		currentSceneActorObjectKey(mover.CharacterID),
		mover.TownID,
		mover.AreaID,
		mover.PositionX,
		mover.PositionY,
		mover.Direction,
		mover.AreaState,
	)
	for i := range oldOthers {
		peer := oldOthers[i].Session
		if peer == nil {
			continue
		}
		_, sameAuthoritativeParty := partyMembers[oldOthers[i].CharacterID]
		peerState := runtimePartyStateSnapshot(peer)
		if sameAuthoritativeParty {
			// The stationary peer may be a replacement/auxiliary transport
			// whose local projection was lost. Use the same authoritative
			// policy as the town actor projection before deciding to destroy
			// its party member. Official default repairs the cache; explicit
			// off and the legacy profile retain their previous behavior.
			peerState = s.townCopresenceActorPartyState(peer)
		}
		if sameAuthoritativeParty && peerState.PartyID == state.PartyID {
			_ = s.sendCurrentSceneFixedClass0Packet(
				peer,
				currentTownUserAreaNotificationMsgID,
				areaBody,
				"town_party_member_area_change_0x0017",
			)
			continue
		}
		_ = s.sendCurrentSceneFixedClass0Packet(
			peer,
			currentTownUserLeaveMsgID,
			leaveBody,
			"town_copresence_leave_0x0006",
		)
	}
}

// broadcastTownPlayerMove sends NOTI 0x0016 (sub_1D83990 position update) to
// all other players in the area.
//
// 最后一个 u16（厂商 motionState）必须是**移动方自己上报的那个值**，不能是常量：
// 观者客户端只从这一条包学到同屏 actor 的移动速率，恒写 100 会让观者看到的人
// 一直按 100 走（现场报障「我视角看对方走得很慢」），而移动方自己走本机速度
// （「对方自己视角很正常速度很快」）——两边因此不对称。
// 逐字节证据与厂商对照见 initial_town_location_notifications.go 的 townPeerMotionStateEnv。
func (s *Service) broadcastTownPlayerMove(mover *onlinePlayerInfo, others []onlinePlayerInfo) {
	if s == nil || s.onlinePlayers == nil || mover == nil || len(others) == 0 {
		return
	}
	actorKey := currentSceneActorObjectKey(mover.CharacterID)
	// 默认档 = 透传客户端值；legacy 档 = 恒写 currentTownInitialMovementRate，
	// 逐字节回到改动前（那条路径的唯一差异字节就是 body[7:9]）。
	motionState := mover.MotionState
	if !currentTownPeerMotionStateFromClient() {
		motionState = currentTownInitialMovementRate
	}
	moveBody := buildCurrentTownUserPositionNotificationBodyWithMotionState(
		actorKey, mover.PositionX, mover.PositionY, mover.Direction, motionState,
	)
	for i := range others {
		if others[i].Session != nil && (!s.currentAntonWaitingArea(mover.TownID, mover.AreaID) || s.onlinePlayers.PeerInSameArea(mover.CharacterID, others[i].CharacterID)) {
			_ = s.sendCurrentSceneFixedClass0Packet(others[i].Session,
				currentTownUserPositionNotificationMsgID, moveBody,
				"town_copresence_move_0x0016")
		}
	}
}

// cleanupOnlinePlayer removes a player from the online player manager on
// disconnect and broadcasts the leave notification to remaining players.
func (s *Service) cleanupOnlinePlayer(session *gameSession) {
	if s == nil || s.onlinePlayers == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	s.closeCurrentExpertJobStoreSession(session, false)
	_, others, removed := s.onlinePlayers.LeaveAreaSession(session.selectedCharacterID, session)
	if !removed {
		return
	}
	if len(others) > 0 {
		s.broadcastTownPlayerLeave(session.selectedCharacterID, others)
	}
}
