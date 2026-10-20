package raid

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// CurrentInfoRequest describes the published 24.9 client CREATE_RAID writer
// 01DF0F9D. UIFlags are UI +34/+35/+38, in wire order. They are deliberately
// not interpreted as the legacy solo flag. MODIFY_RAID has a separate writer
// and omits +38; it must not reuse this three-byte tail.
type CurrentInfoRequest struct {
	Route     byte
	NameBytes []byte
	UIFlags   [3]byte
}

// CurrentOtherChannelJoinRequest is the ten-byte 01A54720 writer. Target
// identity is UID/selected channel/advertised server. The actor getter
// 02964680 resolves channel (global56A1E80 from CHANNELINFO field8);
// 02964560 resolves advertised server (global56A1E74 from field7).
// The 01A58AD5 caller and 0353C833 pass those contexts in this order.
// The two client statistics are
// informational; they must never authorize membership or replace repository data.
type CurrentOtherChannelJoinRequest struct {
	TargetServer  byte
	TargetCharKey uint16
	ClientValue   uint32
	NativeValue   uint16
	TargetChannel byte
}

func DecodeCurrentOtherChannelJoinRequest(body []byte) (CurrentOtherChannelJoinRequest, error) {
	if len(body) != 10 {
		return CurrentOtherChannelJoinRequest{}, errRaidOtherChannelJoinBodyShort
	}
	return CurrentOtherChannelJoinRequest{TargetChannel: body[0], TargetCharKey: binary.LittleEndian.Uint16(body[1:3]), ClientValue: binary.LittleEndian.Uint32(body[3:7]), NativeValue: binary.LittleEndian.Uint16(body[7:9]), TargetServer: body[9]}, nil
}

func DecodeCurrentCreateRaidRequest(body []byte) (CurrentInfoRequest, error) {
	if len(body) < 8 {
		return CurrentInfoRequest{}, errRaidInfoBodyShort
	}
	n := uint64(binary.LittleEndian.Uint32(body[1:5]))
	if n+8 != uint64(len(body)) {
		return CurrentInfoRequest{}, errRaidInfoNameTruncated
	}
	end := 5 + int(n)
	return CurrentInfoRequest{Route: body[0], NameBytes: append([]byte(nil), body[5:end]...), UIFlags: [3]byte{body[end], body[end+1], body[end+2]}}, nil
}

// CurrentModifyInfoRequest describes 01DF1110, the published client's op670
// writer: route, narrow DSTR name, then UI +34/+35. The third creation flag
// is absent, so modifying a name must preserve it in the owning raid state.
type CurrentModifyInfoRequest struct {
	Route     byte
	NameBytes []byte
	UIFlags   [2]byte
}

func DecodeCurrentModifyRaidInfoRequest(body []byte) (CurrentModifyInfoRequest, error) {
	if len(body) < 7 {
		return CurrentModifyInfoRequest{}, errRaidInfoBodyShort
	}
	n := uint64(binary.LittleEndian.Uint32(body[1:5]))
	if n+7 != uint64(len(body)) {
		return CurrentModifyInfoRequest{}, errRaidInfoNameTruncated
	}
	end := 5 + int(n)
	return CurrentModifyInfoRequest{Route: body[0], NameBytes: append([]byte(nil), body[5:end]...), UIFlags: [2]byte{body[end], body[end+1]}}, nil
}

// CurrentMemberRecord adds the byte and narrow DSTR read after the legacy
// core by 01A5AB40. ExtraName is assigned to object +44 (a wide string);
// the preceding byte's meaning and this string's authoritative owner still
// need bridge evidence. This codec accepts explicit data, not fabricated guilds.
type CurrentMemberRecord struct {
	Core      MemberRecord
	ExtraByte byte
	ExtraName string
}

// CurrentMetadata is the 01A5CA60 wire layout. Flags map to +B4/+B6/+B7/+B8;
// +B5 is derived by the client from RaidKey. Request flags cannot be copied
// here until their semantic correspondence is established.
type CurrentMetadata struct {
	RaidKey   uint32
	NameBytes []byte
	Route     byte
	State     byte
	Phase     byte
	Field34   uint32
	Flags     [4]byte
	Leader    CurrentMemberRecord
	Members   []CurrentMemberRecord
}

func writeCurrentMemberRecord(writer *packetWriter, member CurrentMemberRecord) error {
	if member.Core.CharKey == 0 {
		return fmt.Errorf("current raid member has no character owner")
	}
	// The native second DSTR uses a 17-wide-character buffer, including NUL.
	if strings.ContainsRune(member.ExtraName, 0) || len(utf16.Encode([]rune(member.ExtraName))) > 16 {
		return fmt.Errorf("current raid extra name exceeds native buffer")
	}
	extra, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(member.ExtraName))
	if err != nil {
		return errRaidMemberNameInvalid
	}
	if err := writeMemberRecord(writer, member.Core); err != nil {
		return err
	}
	writer.writeByte(member.ExtraByte)
	writer.writeRawDstr(extra)
	return nil
}

func writeCurrentRoster(writer *packetWriter, members []CurrentMemberRecord) error {
	if len(members) > MaxAttackPartyMembers {
		return errRaidMemberTooMany
	}
	seen := make(map[uint16]bool, len(members))
	writer.writeByte(byte(len(members)))
	for _, member := range members {
		if seen[member.Core.CharKey] {
			return fmt.Errorf("current raid roster repeats character owner")
		}
		seen[member.Core.CharKey] = true
		if err := writeCurrentMemberRecord(writer, member); err != nil {
			return err
		}
	}
	return nil
}

func BuildCurrentMemberRefreshMode3Body(raidKey uint32, members []CurrentMemberRecord) ([]byte, error) {
	if raidKey == 0 {
		return nil, fmt.Errorf("current raid roster has no raid owner")
	}
	var writer packetWriter
	writer.writeUint32(raidKey)
	writer.writeUint32(RaidMemberRefreshMode3)
	if err := writeCurrentRoster(&writer, members); err != nil {
		return nil, err
	}
	return writer.bytes(), nil
}

func BuildCurrentMetadataBody(info CurrentMetadata, mode uint32) ([]byte, error) {
	if info.RaidKey == 0 || info.Leader.Core.CharKey == 0 || (mode != 0 && mode != 2) {
		return nil, fmt.Errorf("current raid metadata has no owner or invalid mode")
	}
	if mode == 0 {
		found := false
		for _, member := range info.Members {
			found = found || member.Core.CharKey == info.Leader.Core.CharKey
		}
		if !found {
			return nil, fmt.Errorf("current raid leader missing from roster")
		}
	}
	var writer packetWriter
	writer.writeUint32(info.RaidKey)
	writer.writeUint32(mode)
	if err := writeCurrentMetadataObject(&writer, info); err != nil {
		return nil, err
	}
	if mode == 0 {
		if err := writeCurrentRoster(&writer, info.Members); err != nil {
			return nil, err
		}
	}
	return writer.bytes(), nil
}

func writeCurrentMetadataObject(writer *packetWriter, info CurrentMetadata) error {
	writer.writeUint32(info.RaidKey)
	writer.writeRawDstr(info.NameBytes)
	writer.writeByte(info.Route)
	writer.writeByte(info.State)
	writer.writeByte(info.Phase)
	writer.writeUint32(info.Field34)
	writer.writeRaw(info.Flags[:])
	return writeCurrentMemberRecord(writer, info.Leader)
}

// BuildCurrentInfoReplyBody is ACK1359 (01FB6070), not NOTI591. The
// dispatcher consumes success, then the handler reads selector and raid key.
// Selector1 reads only a roster; selector2 reads metadata followed by roster.
// NOTI591 mode3 assigns the client's own raid and must not answer a view.
func BuildCurrentInfoReplyBody(info CurrentMetadata, selector byte) ([]byte, error) {
	if info.RaidKey == 0 || (selector != 1 && selector != 2) {
		return nil, fmt.Errorf("current raid info has no owner or invalid selector")
	}
	if selector == 2 {
		found := false
		for _, member := range info.Members {
			found = found || member.Core.CharKey == info.Leader.Core.CharKey
		}
		if info.Leader.Core.CharKey == 0 || !found {
			return nil, fmt.Errorf("current raid info leader missing from roster")
		}
	}
	var writer packetWriter
	writer.writeByte(1)
	writer.writeByte(selector)
	writer.writeUint32(info.RaidKey)
	if selector == 2 {
		if err := writeCurrentMetadataObject(&writer, info); err != nil {
			return nil, err
		}
	}
	if err := writeCurrentRoster(&writer, info.Members); err != nil {
		return nil, err
	}
	return writer.bytes(), nil
}

// BuildCurrentDirectoryBody is the current NOTI590 callback 0202D870:
// u32 count, then the 01A5CA60 object and u8 member count for each row.
// It does not contain the outer raid-key/mode pair used by NOTI591, nor a
// roster. Entries must come from the actual raid owner, including leader.
func BuildCurrentDirectoryBody(entries []CurrentMetadata) ([]byte, error) {
	var writer packetWriter
	writer.writeUint32(uint32(len(entries)))
	seen := make(map[uint32]bool, len(entries))
	for _, entry := range entries {
		if entry.RaidKey == 0 || seen[entry.RaidKey] || len(entry.Members) == 0 || len(entry.Members) > MaxAttackPartyMembers {
			return nil, fmt.Errorf("invalid current raid directory owner or member count")
		}
		seen[entry.RaidKey] = true
		leaderFound := false
		members := make(map[uint16]bool, len(entry.Members))
		for _, member := range entry.Members {
			if member.Core.CharKey == 0 || members[member.Core.CharKey] {
				return nil, fmt.Errorf("invalid current raid directory member owner")
			}
			members[member.Core.CharKey] = true
			leaderFound = leaderFound || member.Core.CharKey == entry.Leader.Core.CharKey
		}
		if !leaderFound {
			return nil, fmt.Errorf("current raid directory leader missing from members")
		}
		if err := writeCurrentMetadataObject(&writer, entry); err != nil {
			return nil, err
		}
		writer.writeByte(byte(len(entry.Members)))
	}
	return writer.bytes(), nil
}
