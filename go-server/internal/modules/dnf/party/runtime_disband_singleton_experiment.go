package party

import (
	"os"
	"strings"
)

// disbandOnSingletonEnv 控制「队伍减员到 1 人时立即解散」。
//
// 默认 off = 现行为：`2 -> 1` 时保留一个只有 1 名成员的 runtime party，并给
// 最后一人下发 1 人 op9（kind0）。DNF 90 客户端没有「1 人普通队伍」这种状态：
//  `NOTI 9` 因本地玩家仍在 roster 内而跳过整段退队清场，
//  `CPartyHud::UpdateHorizontal` (0x224A260) 又在 `cmp eax,1` 处提前 ret，
// 于是第二个人血条永久残留、且再也清不掉。
//
// 打开后：任何来源（主动退队 / 选角 / 断线 / 副本预期减员）只要减到 1 人，
// 都把队伍解散，并把解散前的快照放进 RuntimePartyResult.Retired，让桥层走既有
// 退役路径对幸存者下发 kind3 清场。
const disbandOnSingletonEnv = "DNFBRIDGE_PARTY_DISBAND_ON_SINGLETON"

func disbandOnSingleton() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(disbandOnSingletonEnv))) {
	case "0", "off", "false", "no", "legacy":
		return false
	default:
		return true
	}
}
