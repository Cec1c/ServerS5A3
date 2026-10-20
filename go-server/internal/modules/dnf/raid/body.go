// 本文件按 NoPack 0x24F/sub_1D71CA0 的读取顺序构造攻坚队成员刷新体。
package raid

import (
	"errors"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	// RaidMemberRefreshMsgID 是当前 EXE 的 raid 成员刷新 raw upper id，不套用旧 CmdPacket 591 名字。
	RaidMemberRefreshMsgID uint16 = 0x024f
	RaidMemberRefreshMode3 uint32 = 3
	MaxAttackPartyMembers         = 20

	raidMemberNameMaxBytes = 256
)

// MemberRecord 是 sub_17B6EF0 读取的一条攻坚队成员记录。
type MemberRecord struct {
	CharKey           uint16
	Field4            byte
	Name              string
	RawNameUTF16LE    []byte
	Field40           byte
	Field44           byte
	GroupIndex        byte
	SlotOrder         byte
	Field48           uint32
	Field52           byte
	Field53           byte
	Field56           byte
	Field60           uint32
	Field64           uint16
	Field66BoolSource uint32
}

// MemberRefresh 保存 0x24F mode=3 的成员名单刷新数据。
type MemberRefresh struct {
	RaidKey uint32
	Members []MemberRecord
}

var errRaidMemberTooMany = errors.New("raid member count exceeds 20")
var errRaidMemberNameInvalid = errors.New("raid member name encoding invalid")
var errRaidMemberNameTooLong = errors.New("raid member name encoded bytes too long")

// BuildCreateRaidResultBody constructs class1/op664 CREATE_RAID success.
// Current EXE sub_1D04340 consumes the common success byte followed by raid_key:u32.
func BuildCreateRaidResultBody(raidKey uint32) []byte {
	var writer packetWriter
	writer.writeByte(1)
	writer.writeUint32(raidKey)
	return writer.bytes()
}

// BuildLeaveRaidResultBody constructs class1/op665 LEAVE_RAID success.
// Current EXE 01FB5340 consumes the common success byte followed by a one-byte
// forced-exit flag. Both success branches clear own membership at 01A64B80;
// only a set flag displays B6C7 (forced removal). Legacy callers retain their
// previous flag policy independently of current voluntary exits.
func BuildLeaveRaidResultBody(forcedExit bool) []byte {
	var writer packetWriter
	writer.writeByte(1)
	if forcedExit {
		writer.writeByte(1)
	} else {
		writer.writeByte(0)
	}
	return writer.bytes()
}

// BuildMemberRefreshMode3Body 构造 0x24F mode=3：u32 raid_key, u32 mode, u8 count, repeat member。
func BuildMemberRefreshMode3Body(refresh MemberRefresh) ([]byte, error) {
	if len(refresh.Members) > MaxAttackPartyMembers {
		return nil, errRaidMemberTooMany
	}
	var writer packetWriter
	writer.writeUint32(refresh.RaidKey)
	writer.writeUint32(RaidMemberRefreshMode3)
	writer.writeByte(byte(len(refresh.Members)))
	for _, member := range refresh.Members {
		if err := writeMemberRecord(&writer, member); err != nil {
			return nil, err
		}
	}
	return writer.bytes(), nil
}

func writeMemberRecord(writer *packetWriter, member MemberRecord) error {
	nameBytes, err := raidWstrBytes(member)
	if err != nil {
		return err
	}
	writer.writeUint16(member.CharKey)
	writer.writeByte(member.Field4)
	writer.writeRawDstr(nameBytes)
	writer.writeByte(member.Field40)
	writer.writeByte(member.Field44)
	writer.writeByte(member.GroupIndex)
	writer.writeByte(member.SlotOrder)
	writer.writeUint32(member.Field48)
	writer.writeByte(member.Field52)
	writer.writeByte(member.Field53)
	writer.writeByte(member.Field56)
	writer.writeUint32(member.Field60)
	writer.writeUint16(member.Field64)
	writer.writeUint32(member.Field66BoolSource)
	return nil
}

func raidWstrBytes(member MemberRecord) ([]byte, error) {
	// EXE 13B6EF0 -> 3057D20 -> 3057C80 reads a narrow DSTR,
	// then converts to wide text. UTF16 wire names stop at the first zero.
	name := member.Name
	if len(member.RawNameUTF16LE) > 0 {
		if len(member.RawNameUTF16LE)%2 != 0 {
			return nil, errRaidMemberNameInvalid
		}
		units := make([]uint16, len(member.RawNameUTF16LE)/2)
		for index := range units {
			units[index] = uint16(member.RawNameUTF16LE[index*2]) | uint16(member.RawNameUTF16LE[index*2+1])<<8
		}
		name = string(utf16.Decode(units))
	}
	out, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(name))
	if err != nil {
		return nil, errRaidMemberNameInvalid
	}
	return validateRaidWstrBytes(out)
}

func validateRaidWstrBytes(data []byte) ([]byte, error) {
	if len(data) >= raidMemberNameMaxBytes {
		return nil, errRaidMemberNameTooLong
	}
	return append([]byte(nil), data...), nil
}
