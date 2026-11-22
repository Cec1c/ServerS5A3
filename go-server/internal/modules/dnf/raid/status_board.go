// 本文件按 V212 客户端 0x02001B40 的读取顺序构造 class0 / 0x027A (634) 攻坚队状况表推送体。
package raid

import "errors"

const (
	// StatusBoardMsgID 是客户端攻坚队状况表（RaidManager+0x168）的原生 S2C class0 协议号。
	StatusBoardMsgID uint16 = 0x027a

	statusBoardMaxItems = 255
)

// StatusBoardSquad 描述 S2C 634 中的单支攻坚小队状况记录。
// 字段顺序与客户端 0x02001B40 逐字节对应：
// u16 groupIndex, u8 memberCount, memberCount*u16 charID, u32 usedCoins, u32 dungeonProgress, u8 extraCount, extraCount*u32 extraVal。
type StatusBoardSquad struct {
	GroupIndex      uint16
	MemberCharIDs   []uint16
	UsedCoins       uint32
	DungeonProgress uint32
	ExtraValues     []uint32
}

var (
	errStatusBoardTooManySquads  = errors.New("raid status board squad count exceeds 255")
	errStatusBoardTooManyMembers = errors.New("raid status board member count exceeds 255")
	errStatusBoardTooManyExtras  = errors.New("raid status board extra count exceeds 255")
)

// BuildStatusBoardBody 编码 class0 / S2C 634 攻坚队状况表正文。
func BuildStatusBoardBody(squads []StatusBoardSquad) ([]byte, error) {
	if len(squads) > statusBoardMaxItems {
		return nil, errStatusBoardTooManySquads
	}
	var writer packetWriter
	writer.writeByte(byte(len(squads)))
	for _, squad := range squads {
		if len(squad.MemberCharIDs) > statusBoardMaxItems {
			return nil, errStatusBoardTooManyMembers
		}
		if len(squad.ExtraValues) > statusBoardMaxItems {
			return nil, errStatusBoardTooManyExtras
		}
		writer.writeUint16(squad.GroupIndex)
		writer.writeByte(byte(len(squad.MemberCharIDs)))
		for _, charID := range squad.MemberCharIDs {
			writer.writeUint16(charID)
		}
		writer.writeUint32(squad.UsedCoins)
		writer.writeUint32(squad.DungeonProgress)
		writer.writeByte(byte(len(squad.ExtraValues)))
		for _, val := range squad.ExtraValues {
			writer.writeUint32(val)
		}
	}
	return writer.bytes(), nil
}
