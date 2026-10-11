package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 进城后客户端会发 op645 `CHARAC_VIEW_HIDDEN_CHARAC_INFO`（"隐藏角色信息"探测），
// 服务端用 op646 `REBIRTH_HARDCORE_CHARAC` 回答，**这个回答是"决斗场(하드코어)角色数据"
// 是否存在的开关**。
//
// 2026-09-27 现场证据链：
//   - 我们的 op646 = class1 / body `01`，与厂商逐字节相同（厂商日志 game-000059 等会话）。
//   - 但厂商那个 `01` 是**他们角色的真实数据**（他们库里真有决斗场角色记录）；
//     我们的角色没有这类数据，却同样回 `01` ⇒ 客户端认为"有决斗场内容"，
//     随后渲染决斗场任务书（`ui/pvp02/mission/missionbook.xui` + 未翻译的 `dnf_46688`
//   - `0秒` 等待计时）并在那里停住 —— 操作者看到的就是"弹窗 + 卡死"。
//   - 实测：把 646（连同 441/63/143/455/102/14）屏蔽后，**面板消失**；
//     但静默会让客户端一直等，所以正确修法是**回"没有数据"的失败体**，而不是不回。
//
// 修法：op646 体改为失败体 `00 00`（复用 sendGameUpperFailure），语义 = "该角色没有
// 决斗场/하드코어数据"。开关：
//
//	DNFBRIDGE_HARDCORE_CHARAC_ACK=failure  默认：回"没有数据"（修复态）
//	                            success    回退到旧行为（body 01，厂商数据态）
//	                            off        完全不回（等价于屏蔽，仅用于对照）
const hardcoreCharacAckEnvKey = "DNFBRIDGE_HARDCORE_CHARAC_ACK"

const (
	hardcoreCharacAckFailure = "failure"
	hardcoreCharacAckSuccess = "success"
	hardcoreCharacAckOff     = "off"
)

// currentHardcoreCharacAckMode 返回本会话该用哪种回答方式，默认 failure（修复态）。
func currentHardcoreCharacAckMode() string {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(hardcoreCharacAckEnvKey)))
	switch value {
	case hardcoreCharacAckSuccess, hardcoreCharacAckOff, hardcoreCharacAckFailure:
		return value
	case "":
		return hardcoreCharacAckFailure
	default:
		// 未知取值不改变生产行为：按默认（修复态）处理，避免拼错开关把弹窗放回来。
		return hardcoreCharacAckFailure
	}
}

// sendHiddenCharacHardcoreAck 按当前档位回答 op645。
// 返回 sent 表示是否真的写了包（off 档不写）。
func (s *Service) sendHiddenCharacHardcoreAck(session *gameSession) (bool, error) {
	msgID := uint16(dnfenum.UpperMsgRebirthHardcoreCharac)
	switch currentHardcoreCharacAckMode() {
	case hardcoreCharacAckOff:
		return false, nil
	case hardcoreCharacAckSuccess:
		return true, s.sendGameUpperSuccess(session, msgID, nil)
	default:
		// 失败体 `00 00`：客户端据此判定"该角色没有决斗场/하드코어数据"，
		// 既不会打开任务书占位面板，也不会因为等不到回答而卡住。
		return true, s.sendGameUpperFailure(session, msgID, 0)
	}
}
