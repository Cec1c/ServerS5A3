package dnfbridge

import (
	"os"
	"strings"
)

// 「进场景尾巴」是否必须等客户端发 legacy type1345（scene ready）才下发。
//
// 现场证据（2026-09-27，操作者账号 dnf:10001 / Lv90 角色，会话 game-000003）：
//   - 选角后我们的 `sendDeferredSelectSceneTail` 被
//     `currentInitialTownWaitsForLegacySceneReady()` 挡住，
//     日志为 `game-upper-select-character-scene-tail-deferred
//     reason=wait_for_current_exe_legacy_type1345_u32_2_scene_ready`；
//   - 而该客户端整局**从未发过 legacy type1345 / op29**
//     （请求只有 1262/593/8/441/645/4/63/140/120/31/143/197）；
//   - 结果：场景尾巴永不下发 ⇒ 客户端进了赛丽亚房间但**卡死**（面板 + 输入无响应），
//     任务管理器显示网络 0 Mbps、CPU 4%、磁盘 0.1 MB/s（= 在干等，不是 I/O 饿死）。
//
// 这与 op646、op140 是同一类问题：把流程硬挂在客户端不会发的包上。修法是**软化前置**：
// 默认不再要求 type1345，尾巴按路由条件正常下发；需要复现旧行为时设
// DNFBRIDGE_SCENE_TAIL_REQUIRE_LEGACY_1345=on。
const legacySceneReadyGateEnv = "DNFBRIDGE_SCENE_TAIL_REQUIRE_LEGACY_1345"

// 默认**保持旧契约**（要求 legacy type1345）；线上由
// DNFBRIDGE_SCENE_TAIL_REQUIRE_LEGACY_1345=off 软化为"不等它"，从而让当前
// 不发该包的客户端也能收到场景尾巴（修复进房间卡死）。这样代码库既有契约与
// 用例不变，修复以运行时配置生效，可一键回退。
func currentSceneTailRequiresLegacySceneReady() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(legacySceneReadyGateEnv))) {
	case "off", "0", "false", "no", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}
