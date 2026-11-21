package raid

import "fmt"

const (
	RaidClearAnnouncementMsgID        uint16 = 626
	RaidClearAnnouncementNameMaxBytes        = 255
)

// 现用客户端 0x0200E9D0 读取 u32 类型、团名 DSTR、团长名 DSTR 和两个 u8。
// 团队类型 0/1/2 分别使用文案 60454/67017/75517，单人安徒恩/卢克使用 46877/67052。
// 第二个 u8 由客户端读取，当前祝贺文案不使用该字段。
func BuildRaidClearAnnouncementBody(mode byte, raidName, leaderName []byte, solo bool) ([]byte, error) {
	if mode < 1 || mode > 3 || (solo && mode == 3) || len(raidName) > RaidClearAnnouncementNameMaxBytes || len(leaderName) == 0 || len(leaderName) > RaidClearAnnouncementNameMaxBytes || (!solo && len(raidName) == 0) {
		return nil, fmt.Errorf("团本通关播报参数无效：模式=%d 单人=%t 团名长度=%d 团长名长度=%d", mode, solo, len(raidName), len(leaderName))
	}
	var writer packetWriter
	writer.writeUint32(uint32(mode - 1))
	writer.writeRawDstr(raidName)
	writer.writeRawDstr(leaderName)
	if solo {
		writer.writeByte(1)
	} else {
		writer.writeByte(0)
	}
	writer.writeByte(0)
	return writer.bytes(), nil
}
