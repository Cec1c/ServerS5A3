package dnfbridge

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// registerInitialTownPrivateAreaPresence 在"登录 / 初次入城"这条路径上，为**每个角色一份的
// 私有房间**（赛丽亚的房间）按角色实例登记在线存在。
//
// 背景：登录路径（sendCurrentInitialTownRoute / currentCharacterListLoginTransition）此前
// 完全不碰 OnlinePlayerManager —— 只有客户端的过图提交（op36 SET_USER_AREA）和出图重绑才会
// 登记。于是"角色持久化位置就在赛丽亚房间"时，本人在区域表里是**完全没有登记**的半个状态：
//   - 表面上也是看不见的（没登记 → 共享枚举里自然没有他），所以不是可见性泄漏；
//   - 但区域归属只由客户端后续的 op36 决定，任何人（包括本人重新登录/换连接）都无法从
//     服务端状态判断"这个人此刻在自己的私房里"，与 op36 路径的实例化判据（townAreaInstanceForMapPath）
//     不对称，也无法用日志/用例断言。
//
// 这里把登录路径对齐到同一条判据：目标区域地图是私有房间时，以 Instance = 本人角色 ID
// 登记进自己的实例（别人进同一 (town,area) 也落在各自的实例里，看不见他；共享区域枚举
// GetAreaPlayers 固定 Instance=0，同样看不到）。
//
// 共享城镇区域在登录路径上**维持既有行为**（不登记，等客户端 op36 再登记）：登录即广播
// 会改变已经线上验证过的同屏/时序（本地角色还没进场景就给别人播 actor），不属于本次修复范围。
func (s *Service) registerInitialTownPrivateAreaPresence(
	session *gameSession,
	characterID uint16,
	character dnfrepo.CharacterRecord,
	townID byte,
	areaID byte,
	mapPath string,
	row currentSceneTransitionRow,
	source string,
) bool {
	if s == nil || s.onlinePlayers == nil || session == nil || characterID == 0 {
		return false
	}
	areaInstance := townAreaInstanceForMapPath(mapPath, characterID)
	if areaInstance == 0 {
		return false
	}
	playerInfo := &onlinePlayerInfo{
		CharacterID: characterID,
		AccountID:   character.AccountID,
		Name:        character.Name,
		Job:         byte(numericCharacterStat(character.Job)),
		GrowType:    characterGrowTypeByte(character),
		Level:       byte(character.Level),
		TownID:      townID,
		AreaID:      areaID,
		// 登录落点也带频道：不同频道的两个角色即使同城同区也各在自己的桶里，
		// 私房实例（Instance=角色ID）与频道两个分量互不干扰。
		ChannelID: currentTownPresenceChannelID(session),
		Instance:  areaInstance,
		PositionX: row.Value1,
		PositionY: row.Value2,
		Direction: row.Value3,
		AreaState: row.Value4,
		Session:   session,
	}
	// 私有实例的键里带着本人角色 ID，因此这里永远只会返回"自己"，不会有同实例的别人；
	// 保留广播调用是为了万一键规则将来变化时行为仍然正确（空列表时它立刻返回）。
	newOthers := s.onlinePlayers.EnterArea(playerInfo)
	if len(newOthers) > 0 {
		s.broadcastTownPlayerEnter(playerInfo, newOthers)
	}
	s.logGameEvent(session, "game-town-private-area-entered",
		"char_id", characterID,
		"town_id", townID,
		"area_id", areaID,
		"map_path", mapPath,
		"area_key_channel", currentTownPresenceChannelID(session),
		"area_instance", areaInstance,
		"source", source,
		"same_instance_peers", len(newOthers),
		"reason", "login_initial_town_private_room_keyed_to_the_owner_character")
	return true
}

// initialTownSharedAreaPresenceEnv 是"登录落点在**共享**城镇区域时也收口发布在线存在感"
// 的回退开关。默认开启；取 off/0/false/no/legacy 时回到改动前行为（共享落点不登记，
// 等客户端 op36）。
const initialTownSharedAreaPresenceEnv = "DNFBRIDGE_INITIAL_TOWN_SHARED_PRESENCE"

// initialTownSharedAreaPresenceSource 是这条路由记进 `game-town-presence-published` 的 source，
// 与既有两条路由（`prev_village_return_from_private_seria_room` /
// `channel_reconnect_town_actor_route_complete`）并列，便于线上按来源统计。
const initialTownSharedAreaPresenceSource = "initial_town_login_shared_area"

func currentInitialTownSharedAreaPresenceEnabled() bool {
	return !currentTownPresencePublishDisabled(initialTownSharedAreaPresenceEnv)
}

// registerInitialTownSharedAreaPresence 在"登录 / 初次入城"这条路径上，当落点是**共享**
// 城镇区域（`townAreaInstanceForMapPath` 判为 0，即不是"每个角色一份"的私房）时，
// 按落点收口发布在线存在感。
//
// # 为什么必须补（与既有定稿口径直接相关）
//
// `town_copresence_actor_resend.go` 的 `publishTownPresenceInArea` 文件头写明了口径：
// **每一条服务端自己把角色放进城镇的路由都必须自己收口发布存在感**（登录私房、op36、
// 出副本回城、赛丽亚出房门、频道重连进城）。理由是 `PeerInSameArea` 是城镇内一切
// 点对点交互的**硬闸门**（邀请、查看信息、交易、选择器 fan-out、整队进副本）：
// 一个玩家只要"人在城里、但服务端区域表里没有他"，邀请就会被静默吞掉，
// 客户端表现正是"组不上队 / 点了没反应"。
//
// 登录路径此前只在落点是私房时登记（`registerInitialTownPrivateAreaPresence`），
// 共享落点维持"等客户端随后补一次 op36"的旧假设。该假设对**默认**三级落点成立
// （默认落点是私房，客户端进城一定会发 op36），但对第二级"沿用持久化落点"
// （`DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep`，见 initial_town_login_owner.go）**不成立**：
// 那条分支把角色直接放进共享城镇区域，客户端认为自己已经就位、**不会再发 op36**，
// 于是服务端的区域表里永远没有这个人。
//
// 现场实测（2026-09-27，双客户端自动化 p2）：两名角色同城同区（town 39 / area 0，
// `new_Hendon_Main.map`）登录成功、组队名册也下发成功，但
// `game-town-presence-published` **一条都没有**，双方客户端互相看不到对方的 actor。
//
// 时序与既有的赛丽亚出房门路由（`publishTownPresenceAfterSeriaRoomReturn`）完全一致：
// 都是"服务端先发布、客户端随后才进场景"，因此这里不引入新的时序风险。
func (s *Service) registerInitialTownSharedAreaPresence(
	session *gameSession,
	characterID uint16,
	character dnfrepo.CharacterRecord,
	townID byte,
	areaID byte,
	mapPath string,
	row currentSceneTransitionRow,
	source string,
) bool {
	if s == nil || s.onlinePlayers == nil || session == nil || characterID == 0 {
		return false
	}
	if !currentInitialTownSharedAreaPresenceEnabled() {
		return false
	}
	if townAreaInstanceForMapPath(mapPath, characterID) != 0 {
		// 私房由 registerInitialTownPrivateAreaPresence 负责（实例键 = 角色 ID）。
		return false
	}
	_ = character
	s.publishTownPresenceInArea(
		session,
		townID,
		areaID,
		row.Value1,
		row.Value2,
		row.Value3,
		row.Value4,
		source,
	)
	return true
}
