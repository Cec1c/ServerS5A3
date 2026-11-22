package dnfbridge

import (
	"os"
	"strings"
)

// dungeonPeerOp37ResendEnv 控制 op37 finish-loading 阶段是否执行队友 actor 补投
// （finishDungeonPartyPeerActorProjection）：
//   - 默认（空 / off / 0 / false）：彻底跳过 op37 补投，仅保留进图落位时的初次
//     projectDungeonPartyPeerActors（TCP 可靠投递一次即可，消除首房 162~228ms 与换房重投）；
//   - run：仅首房补投一次，同场副本换房跳过；
//   - room / on / 1 / true / legacy：恢复原有逐房间补投，便于随时 A/B 回退对比。
const dungeonPeerOp37ResendEnv = "DNFBRIDGE_DUNGEON_PEER_OP37_RESEND"

type dungeonPeerOp37ResendMode byte

const (
	dungeonPeerOp37ResendOff dungeonPeerOp37ResendMode = iota
	dungeonPeerOp37ResendRun
	dungeonPeerOp37ResendRoom
)

func currentDungeonPeerOp37ResendMode() dungeonPeerOp37ResendMode {
	return parseDungeonPeerOp37ResendMode(os.Getenv(dungeonPeerOp37ResendEnv))
}

func parseDungeonPeerOp37ResendMode(raw string) dungeonPeerOp37ResendMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "run":
		return dungeonPeerOp37ResendRun
	case "room", "on", "1", "true", "yes", "legacy":
		return dungeonPeerOp37ResendRoom
	default:
		return dungeonPeerOp37ResendOff
	}
}

func normalizeDungeonPeerActorResendKey(key dungeonPartyRunKey) dungeonPartyRunKey {
	if currentDungeonPeerOp37ResendMode() == dungeonPeerOp37ResendRun {
		key.RoomX = 0
		key.RoomY = 0
		key.MapID = 0
	}
	return key
}
