package dnfbridge

import (
	"os"
	"strings"
)

// 2026-09-26 逐包对齐厂商登录下行流时发现的第二个缺口：
// 厂商在 `op108`（活动目录）+ `op1206`（消耗时间进度）之后、`op1240`（马战开启态）
// 之前，固定发一条 **msg_id=1303 `game-raid-daily-availability-sent`**：
//
//	body_len=15 layout=three_rows_mode_used_limit_field4_field5
//	anton_mode=1    anton_today_used=0    anton_daily_limit=10
//	luke_mode=2     luke_today_used=0     luke_daily_limit=10
//	fiendwar_mode=3 fiendwar_today_used=0 fiendwar_daily_limit=10
//
// 厂商原始包（`game-000059`，len=31 = 16 字节包头 + 15 字节包体）：
//
//	0017051f000000e585faf73600000000 | 01 00 0a 00 00 | 02 00 0a 00 00 | 03 00 0a 00 00
//
// 即每行 5 字节：mode(u8) used(u8) limit(u8) field4(u8) field5(u8)。
// 这只是旧厂商样本，不能作为 official-24.9 的协议依据。公开 v84 EXE 的
// class0/1303 handler 0x01A5AE80 每行仅读 4 字节，共 12 字节。上述 15 字节
// 在该 handler 中会错位成 mode=1/0/0。当前尚无可信的持久次数及第四字段来源，
// official-24.9 跳过旧包；仅超时空频道投影真实账本的三行12字节快照。
// 其他旧档保留厂商样本行为，`DNFBRIDGE_RAID_DAILY_AVAILABILITY=off` 可回退。
const (
	currentRaidDailyAvailabilityMsgID = uint16(1303)
	currentRaidDailyAvailabilityEnv   = "DNFBRIDGE_RAID_DAILY_AVAILABILITY"

	currentRaidDailyAntonMode     = 1
	currentRaidDailyLukeMode      = 2
	currentRaidDailyFiendwarMode  = 3
	currentRaidDailyDefaultUsed   = 0
	currentRaidDailyDefaultLimit  = 10
	currentRaidDailyDefaultTailA  = 0
	currentRaidDailyDefaultTailB  = 0
	currentRaidDailyAvailabilityN = 3
)

var currentRaidDailyAvailabilityOffValues = map[string]bool{
	"off": true, "0": true, "false": true, "no": true, "legacy": true, "disable": true, "disabled": true,
}

func currentRaidDailyAvailabilityEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(currentRaidDailyAvailabilityEnv)))
	if value == "" {
		return true
	}
	return !currentRaidDailyAvailabilityOffValues[value]
}

// buildCurrentRaidDailyAvailabilityBody 按上面钉住的 5 字节行布局编码三条（安徒恩/
// 卢克/魔兽）。field4/field5 在厂商样本里恒为 0，这里保持原样。
func buildCurrentRaidDailyAvailabilityBody(modes []int, used, limit int) []byte {
	var writer packetWriter
	for _, mode := range modes {
		writer.writeByte(byte(mode))
		writer.writeByte(byte(used))
		writer.writeByte(byte(limit))
		writer.writeByte(currentRaidDailyDefaultTailA)
		writer.writeByte(currentRaidDailyDefaultTailB)
	}
	return writer.bytes()
}

// sendCurrentRaidDailyAvailability 在消耗时间活动状态之后下发 op1303，
// 位置与厂商一致（op108 → op1206 → **op1303** → op1240/op1241/op1242）。
func (s *Service) sendCurrentRaidDailyAvailability(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if s.options.currentClientVersion == "official-24.9" {
		if isCurrentFiendWarChannel(session) && currentRaidDailyAvailabilityEnabled() {
			return s.sendCurrentNativeRaidDailyAvailability(session, source)
		}
		s.logGameEvent(session, "game-raid-daily-availability-skipped",
			"char_id", session.selectedCharacterID,
			"source", source,
			"msg_id", currentRaidDailyAvailabilityMsgID,
			"reason", "current_client_requires_verified_12_byte_rows_and_persisted_counts",
			"legacy_body_len", 15,
			"native_body_len", 12)
		return nil
	}
	if !currentRaidDailyAvailabilityEnabled() {
		s.logGameEvent(session, "game-raid-daily-availability-skipped",
			"char_id", session.selectedCharacterID,
			"source", source,
			"msg_id", currentRaidDailyAvailabilityMsgID,
			"switch", currentRaidDailyAvailabilityEnv,
			"reason", "rollback_switch_off")
		return nil
	}
	body := buildCurrentRaidDailyAvailabilityBody(
		[]int{currentRaidDailyAntonMode, currentRaidDailyLukeMode, currentRaidDailyFiendwarMode},
		currentRaidDailyDefaultUsed,
		currentRaidDailyDefaultLimit,
	)
	s.logGameEvent(session, "game-raid-daily-availability-sent",
		"char_id", session.selectedCharacterID,
		"msg_id", currentRaidDailyAvailabilityMsgID,
		"classification", 0,
		"body_len", len(body),
		"layout", "three_rows_mode_used_limit_field4_field5",
		"anton_mode", currentRaidDailyAntonMode,
		"anton_today_used", currentRaidDailyDefaultUsed,
		"anton_daily_limit", currentRaidDailyDefaultLimit,
		"luke_mode", currentRaidDailyLukeMode,
		"luke_today_used", currentRaidDailyDefaultUsed,
		"luke_daily_limit", currentRaidDailyDefaultLimit,
		"fiendwar_mode", currentRaidDailyFiendwarMode,
		"fiendwar_today_used", currentRaidDailyDefaultUsed,
		"fiendwar_daily_limit", currentRaidDailyDefaultLimit,
		"source", source,
		"body_source", "vendor_packet_log_game_000059_msg1303_three_rows_5_bytes_each")
	return s.sendGameUpperRawClass(session, currentRaidDailyAvailabilityMsgID, body, 0)
}
