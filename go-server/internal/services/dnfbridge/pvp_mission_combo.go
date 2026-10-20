package dnfbridge

import (
	"os"
	"strings"
)

// 2026-09-26 现场：CN 客户端登录进城后弹出「决斗场任务书」（`ui/pvp02/mission/
// missionbook.xui`，说明区是 `Interface2/PvP02/Mission/Mission.img` 里烧死的韩文
// 3v3 团队任务图 + 缺翻译的 `dnf_46688` 字符串键）。
//
// 定性证据链：
//   - 厂商自带客户端 `D:\DNF90-test\client`（本树从未改动）连**我们的**服务端
//     同样弹窗；同一客户端连**厂商**服务端（`D:\DNF90-test\server`，本机只读实例）
//     不弹 ⇒ 触发点在服务端下行内容，不在客户端文件。
//   - 逐包对齐厂商会话（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，
//     session game-000059，2215 个包）后，最大的结构性缺口是
//     **op411 `ENUM_CMDPACKET_PVP_MISSION_COMBO`**：厂商每个会话都发
//     （659 字节 = 82 行），我们**一个都没发**（我们的 296 个包里没有 411）。
//   - 任务书窗口的数据源就是这张表（厂商 schema
//     `character_pvp_missions(character_id, sort_order, mission_id, progress_value)`
//     与此行一一对应：u32 mission_id + u32 progress_value）。
//
// 包体布局（用厂商实际发出的字节逐字节反推并验证）：
//
//	u8  flag = 0x01
//	u32 count                      // 厂商 game-000059 = 82
//	count * { u32 mission_id, u32 progress_value }
//
// 验证方法：把厂商 `hex=` 前 512 字节去掉 16 字节包头后，按上表解析得到 61 行
// 合法 (id,0)，并用同一布局重建 —— 与厂商可见字节**只差 count 字段**（我们 61，
// 厂商 82），其余 492 字节完全一致（见 pvp_mission_combo_test.go 的黄金测试）。
//
// 作用域与回退：默认下发（= 厂商行为）；`DNFBRIDGE_PVP_MISSION_COMBO=off`
// 逐字节回退到"不发"。
const (
	currentPVPMissionComboMsgID  uint16 = 411
	currentPVPMissionComboFlag   byte   = 0x01
	currentPVPMissionComboEnvKey        = "DNFBRIDGE_PVP_MISSION_COMBO"
)

// currentPVPMissionComboOffValues 与仓内其它回退开关保持一致的取值口径。
var currentPVPMissionComboOffValues = map[string]bool{
	"off": true, "0": true, "false": true, "no": true, "legacy": true, "disable": true, "disabled": true,
}

// currentPVPMissionComboEnabled 报告本次是否下发 op411。
func currentPVPMissionComboEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(currentPVPMissionComboEnvKey)))
	if value == "" {
		return true
	}
	return !currentPVPMissionComboOffValues[value]
}

// currentPVPMissionComboIDs 是厂商 op411 里**已逐字节证实**的任务 id 顺序，取自
// `game-000059` 那条 659 字节包（日志按 512 字节截断）。
//
// 可见 62 行的前 61 整行 + 第 62 行的前 3 字节（id=77 = 0x4D）：
// 97..102 → 1..36 → 38..40 → 50..59 → 71..76；厂商 count 字段为 82，表尾还有
// 20 行在被截断的部分（第 62 行已证实是 77），本树尚未取得 ⇒ 只声明已证实的
// 61 条，count 与行数自洽（客户端拿到的是一张合法且非空的表）。
var currentPVPMissionComboIDs = []uint32{
	97, 98, 99, 100, 101, 102,
	1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
	11, 12, 13, 14, 15, 16, 17, 18, 19, 20,
	21, 22, 23, 24, 25, 26, 27, 28, 29, 30,
	31, 32, 33, 34, 35, 36,
	38, 39, 40,
	50, 51, 52, 53, 54, 55, 56, 57, 58, 59,
	71, 72, 73, 74, 75, 76,
}

// buildCurrentPVPMissionComboBody 按上面钉住的布局编码一张任务表。
// progress 为 nil 时全部进度写 0（= 厂商观测到的形态）。
func buildCurrentPVPMissionComboBody(ids []uint32, progress map[uint32]uint32) []byte {
	var writer packetWriter
	writer.writeByte(currentPVPMissionComboFlag)
	writer.writeUint32(uint32(len(ids)))
	for _, id := range ids {
		writer.writeUint32(id)
		if progress == nil {
			writer.writeUint32(0)
			continue
		}
		writer.writeUint32(progress[id])
	}
	return writer.bytes()
}

// sendCurrentPVPMissionCombo 在进城最终 op24 之前下发 op411，位置与厂商一致
// （厂商顺序：… op19/op2 → **op411** → op19/op205/op105/op21/op574 …）。
func (s *Service) sendCurrentPVPMissionCombo(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if !currentPVPMissionComboEnabled() {
		s.logGameEvent(session, "game-pvp-mission-combo-skipped",
			"char_id", session.selectedCharacterID,
			"source", source,
			"msg_id", currentPVPMissionComboMsgID,
			"switch", currentPVPMissionComboEnvKey,
			"reason", "rollback_switch_off")
		return nil
	}
	body := buildCurrentPVPMissionComboBody(currentPVPMissionComboIDs, nil)
	s.logGameEvent(session, "game-pvp-mission-combo-sent",
		"char_id", session.selectedCharacterID,
		"msg_id", currentPVPMissionComboMsgID,
		"classification", 0,
		"body_len", len(body),
		"mission_rows", len(currentPVPMissionComboIDs),
		"vendor_rows_in_reference_packet", 82,
		"source", source,
		"body_source", "vendor_packet_log_game_000059_msg411_layout_u8_flag_u32_count_rows",
		"reason", "client_missionbook_needs_a_non_empty_pvp_mission_table_before_op24")
	return s.sendGameUpperRawClass(session, currentPVPMissionComboMsgID, body, 0)
}
