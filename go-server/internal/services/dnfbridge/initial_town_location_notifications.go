package dnfbridge

import "encoding/binary"

const (
	// These are current notification IDs, not CmdPacket enum values.  The
	// generated Go enum only contains the C2S SET_USER_POSITION/AREA commands.
	// Current NoPack registers the matching S2C readers at sub_1D83990 and
	// sub_1D89590 respectively.
	currentTownUserPositionNotificationMsgID uint16 = 0x0016
	currentTownUserAreaNotificationMsgID     uint16 = 0x0017

	// currentTownInitialMovementRate 是 0x0016 最后一个 u16 的**默认/初始化值**：
	// 只用于"服务端自己投影某人的当前坐标"那条路径（op36 过图收尾与登录落点，
	// 两端都只发给本人，见 town_move.go:296 与 sendCurrentInitialTownLocationNotificationsLocked）。
	// 那条路径上没有客户端上报过这个字段，所以 100 是唯一已知的安全值。
	//
	// 它**不是**同屏广播该用的值：见下面的 townPeerMotionStateEnv。
	currentTownInitialMovementRate uint16 = 100
)

// townPeerMotionStateEnv 是「城镇同屏广播 0x0016 时，最后一个 u16 用**移动方自己
// 上报的值**还是恒定的 currentTownInitialMovementRate」的开关。
//
// 现场报障：「在城镇看其他玩家的移动速度不正确 对方自己的视角看很正常速度很快
// 我的视角看对方走的很慢不正常」。根因就在这一个 u16 上。
//
// **厂商口径（逐字节可对照的真源）**：
//
//	ServerS4A21-master/Server/DfoServer/Network/Handlers/TownHandler.cs:168-197
//	  Handle_ENUM_CMDPACKET_SET_USER_POSITION(session, header, body):
//	      var direction   = body[4];
//	      var motionState = BitConverter.ToUInt16(body, 5);          // ← C2S op35 body[5:7]
//	      ...
//	      TownAreaNotificationBuilder.BuildUserPosition(snap, motionState);  // ← S2C noti0x16 body[7:9]
//
//	ServerS4A21-master/Server/DfoServer/Network/Builders/Town/TownAreaNotificationBuilder.cs:70-84
//	  BuildUserPosition(TownUserSnapshot snapshot) => BuildUserPosition(snapshot, 0x0064);
//	  BuildUserPosition(TownUserSnapshot snapshot, ushort motionState) {
//	      writer.WriteUInt16(snapshot.UserId);
//	      writer.WriteInt16(snapshot.PosX);
//	      writer.WriteInt16(snapshot.PosY);
//	      writer.WriteByte(snapshot.Direction);
//	      writer.WriteUInt16(motionState);                            // ← 原样透传
//	  }
//
// 即：C2S op35 的 body[5:7] 与 S2C noti0x16 的 body[7:9] 是**同一个字段**
// （厂商叫 motionState），厂商把它逐字节原样回填；0x0064 只是那个不带 motionState
// 参数的重载里的默认值。
//
// 我们此前把该字段解析进 currentTownSetUserPositionRequest.OpaqueScaledU16 之后就丢掉了
// （town_position.go 只把它记进日志、传进快照），广播时恒写 100。客户端**只能**从这一条
// 0x0016 学到同屏 actor 的移动速率——op24 AREA_USERS（buildTownAreaUserListBody）与
// op23 noti0x17（buildCurrentTownUserAreaNotificationBody）的布局里都没有这个字段
// ——于是观者侧永远按 100 走，而移动方自己走的是本机速度，两边因此不对称。
//
// 默认值 = 修复后的行为（透传客户端值）；回退字面量 legacy（也接受 0/off/false/no）
// = 逐字节回到恒写 currentTownInitialMovementRate。
const townPeerMotionStateEnv = "DNFBRIDGE_TOWN_PEER_MOTION_STATE"

// currentTownPeerMotionStateFromClient 每次调用都读环境变量（与同包其它城镇开关一致，
// 档位判定复用 town_copresence_actor_resend.go 的 currentTownPresencePublishDisabled，
// 回退字面量与全仓其它开关保持同一组）。
func currentTownPeerMotionStateFromClient() bool {
	return !currentTownPresencePublishDisabled(townPeerMotionStateEnv)
}

// currentTownPeerMotionStateMode 是判读事件/测试用的档位串。
func currentTownPeerMotionStateMode() string {
	if currentTownPeerMotionStateFromClient() {
		return "client_op35_motion_state"
	}
	return "legacy_constant_100"
}

// buildCurrentTownUserPositionNotificationBody matches current NoPack
// class0/noti0x16 -> sub_1D83990:
//
//	u16 actor_key, u16 x, u16 y, u8 direction, u16 motion_state
//
// 这个重载保留"恒定 currentTownInitialMovementRate"的语义，供服务端自己的坐标投影
// （op36/登录落点，只发给本人）与既有测试使用；同屏广播必须走带 motionState 的那个，
// 见 townPeerMotionStateEnv。
func buildCurrentTownUserPositionNotificationBody(
	actorKey uint16,
	x uint16,
	y uint16,
	direction byte,
) []byte {
	return buildCurrentTownUserPositionNotificationBodyWithMotionState(
		actorKey,
		x,
		y,
		direction,
		currentTownInitialMovementRate,
	)
}

// buildCurrentTownUserPositionNotificationBodyWithMotionState 是 0x0016 的唯一编码点。
func buildCurrentTownUserPositionNotificationBodyWithMotionState(
	actorKey uint16,
	x uint16,
	y uint16,
	direction byte,
	motionState uint16,
) []byte {
	body := make([]byte, 9)
	binary.LittleEndian.PutUint16(body[0:2], actorKey)
	binary.LittleEndian.PutUint16(body[2:4], x)
	binary.LittleEndian.PutUint16(body[4:6], y)
	body[6] = direction
	binary.LittleEndian.PutUint16(body[7:9], motionState)
	return body
}

// buildCurrentTownUserAreaNotificationBody matches current NoPack
// class0/noti0x17 -> sub_1D89590:
//
//	u16 actor_key, u8 town, u8 area, u16 x, u16 y, u8 direction, u8 state_bits
//
// sub_1D89590 passes the final two bytes to sub_23DBA50 in that order:
// offset+6 is switched as the 2/3/4/6/8/9 direction enum, while offset+7 is
// consumed as a bit field.  Swapping them corrupts the selected town actor's
// movement state and prevents the camera from following it.
func buildCurrentTownUserAreaNotificationBody(
	actorKey uint16,
	townID byte,
	areaID byte,
	x uint16,
	y uint16,
	direction byte,
	stateBits byte,
) []byte {
	body := make([]byte, 10)
	binary.LittleEndian.PutUint16(body[0:2], actorKey)
	body[2] = townID
	body[3] = areaID
	binary.LittleEndian.PutUint16(body[4:6], x)
	binary.LittleEndian.PutUint16(body[6:8], y)
	body[8] = direction
	body[9] = stateBits
	return body
}

// sendCurrentTownLocationNotifications publishes the current EXE's local
// position/area pair.  Op23 is not redundant with the following op24: its
// local-actor branch stores the active town/area/coordinates and rebinds the
// controlled scene actor before AREA_USERS applies the area roster.
func (s *Service) sendCurrentTownLocationNotifications(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
	row currentSceneTransitionRow,
	source string,
) error {
	actorKey := currentSceneActorObjectKey(characterID)
	positionBody := buildCurrentTownUserPositionNotificationBody(
		actorKey,
		row.Value1,
		row.Value2,
		row.Value3,
	)
	if err := s.sendCurrentSceneFixedClass0Packet(
		session,
		currentTownUserPositionNotificationMsgID,
		positionBody,
		source+"_user_position_current_exe_sub_1D83990",
	); err != nil {
		return err
	}
	areaBody := buildCurrentTownUserAreaNotificationBody(
		actorKey,
		townID,
		areaID,
		row.Value1,
		row.Value2,
		row.Value3,
		row.Value4,
	)
	if err := s.sendCurrentSceneFixedClass0Packet(
		session,
		currentTownUserAreaNotificationMsgID,
		areaBody,
		source+"_user_area_current_exe_sub_1D89590",
	); err != nil {
		return err
	}
	return nil
}

// sendCurrentInitialTownLocationNotificationsLocked publishes the selected
// actor's validated persisted location before the existing AREA_USERS/op24 snapshot.
// The fixed16 zero-tail envelope is the same current scene envelope used by
// the working Python service and by Go's repository-backed ITEM_LIST packets.
// The caller holds session.townMu.
func (s *Service) sendCurrentInitialTownLocationNotificationsLocked(
	session *gameSession,
	characterID uint16,
	townID byte,
	areaID byte,
	row currentSceneTransitionRow,
) error {
	return s.sendCurrentInitialTownLocationAtStageLocked(session, characterID, townID, areaID, row, currentInitialTownRoutePlayerStatePrepared)
}

func (s *Service) sendCurrentInitialTownLocationAtStageLocked(session *gameSession, characterID uint16, townID, areaID byte, row currentSceneTransitionRow, minimum currentInitialTownRouteStage) error {
	if session == nil || characterID == 0 ||
		session.initialTownRouteCharacterID != characterID ||
		session.initialTownRouteStage < minimum ||
		session.initialTownLocationNotificationsSent {
		return nil
	}
	actorKey := currentSceneActorObjectKey(characterID)
	if err := s.sendCurrentTownLocationNotifications(
		session,
		characterID,
		townID,
		areaID,
		row,
		"initial_town_location",
	); err != nil {
		return err
	}
	// 房间 op24 之前先把"归属外城"写进客户端的世界地图上下文缓存：op24 自身也会调
	// sub_3406800，但城镇 38 在 PVF 里带 `[exclude user pretown info]`，setter 不会覆盖
	// 已有缓存 —— 冷登录时缓存是空的，只能在这里补。失败必须直接返回，**不能**先推进
	// initialTownLocationNotificationsSent，否则重试会永久丢掉这条通知。
	// 回滚档见 currentTownSeriaWorldTownEnv（默认=修复档）。
	if err := s.sendCurrentInitialTownSeriaWorldTownContextLocked(session, characterID, townID, areaID); err != nil {
		return err
	}
	session.initialTownLocationNotificationsSent = true
	s.logGameEvent(session, "game-initial-town-location-notifications-sent",
		"char_id", characterID,
		"actor_object_key", actorKey,
		"town_id", townID,
		"area_id", areaID,
		"position_x", row.Value1,
		"position_y", row.Value2,
		"direction", row.Value3,
		"area_state", row.Value4,
		"world_town_mode", currentTownSeriaWorldTownMode(),
		"sequence", "noti16_user_position_then_noti17_user_area_then_optional_seria_world_town_then_current_op24_area_users")
	return nil
}
