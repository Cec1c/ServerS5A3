package dnfbridge

import (
	"os"
	"strings"
)

// dungeonPeerRecoveryEnv 控制副本队友 6 轮 0x7f14 (ARQ1) 盲重投链开关：
//   - 默认（空 / off / 0 / false）：关闭盲重投链。当前客户端 EXE 不含 0x7f14 (ARQ1/ARR1)
//     回执补丁，开启会在每个房间对每个队友连续重发 6 轮全量 mode0/mode1/subtype0/op9
//     （4 人队 15 秒内轰炸 18 次），导致客户端主线程周期性重建角色卡顿。
//   - on / 1 / true / legacy：恢复原有 6 轮重投链，便于随时 A/B 对比。
//
// 本开关只控制 scheduleDungeonPeerActorRecovery 的延迟补投链，完全不影响正常的初次
// 队友投影（projectDungeonPartyPeerActors）、进图落位、组队状态机与同屏逻辑。
const dungeonPeerRecoveryEnv = "DNFBRIDGE_DUNGEON_PEER_RECOVERY"

func dungeonPeerActorRecoveryEnabled() bool {
	return parseDungeonPeerActorRecoveryEnabled(os.Getenv(dungeonPeerRecoveryEnv))
}

func parseDungeonPeerActorRecoveryEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "1", "true", "yes", "legacy":
		return true
	default:
		return false
	}
}
