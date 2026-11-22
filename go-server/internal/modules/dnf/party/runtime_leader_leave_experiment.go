package party

import (
	"os"
	"strings"
)

// leaderLeaveSameIDEnv 控制「队长离队时是否保持同一个 party id」。
//
// 默认 off = 现行为：队长离队且仍有幸存者时 retired + rebuildLocked，幸存者被
// 重建到**新的 party id**。bridge 因此 close 旧中继、按新 party 重建腿，端口从
// 30001 重新排；而客户端会缓存第一次拿到的对端端口（2026-11-08 现场结论），
// 于是剩下两名成员互相红电脑。
//
// 打开后改为「同 partyId 转让」：只把 leader 位交给槽位最靠前的幸存者，不重建。
// bridge 走 syncRuntimePartyUDPRelay 的成员减少路径：只关掉离队者的腿，幸存
// 两人的腿 (owner,peer) 不变、端口不变，客户端缓存继续有效。
const leaderLeaveSameIDEnv = "DNFBRIDGE_PARTY_LEADER_LEAVE_SAME_ID"

func leaderLeaveSameID() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(leaderLeaveSameIDEnv)), "on")
}
