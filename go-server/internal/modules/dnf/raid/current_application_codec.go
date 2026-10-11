package raid

import (
	"encoding/binary"
	"fmt"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const CurrentRaidApplicationMsgID uint16 = 755

// CurrentApplicationRecord is the packed 51-byte 0203B290 notification.
// +4/+5 identify the applicant selected channel/advertised server.
// CHANNELINFO fields8/7 populate native globals56A1E80/56A1E74; +10 is the real character
// level, +12/+13 job/grow, +14 a NUL-terminated narrow name. The unused
// reserved bytes and unavailable secondary statistic words retain zero.
// ClientValue/NativeValue are explicitly untrusted display hints, never rules.
type CurrentApplicationRecord struct {
	CharKey     uint16
	Server      byte
	Channel     byte
	ClientValue uint32
	NativeValue uint16
	Level       uint16
	Job         byte
	Grow        byte
	Name        string
}

func BuildCurrentRaidApplicationBody(row CurrentApplicationRecord) ([]byte, error) {
	if row.CharKey == 0 || row.Channel == 0 || row.Level == 0 || strings.ContainsRune(row.Name, 0) {
		return nil, fmt.Errorf("raid applicant has invalid authoritative identity")
	}
	name, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(row.Name))
	if err != nil || len(name) == 0 || len(name) >= 30 {
		return nil, fmt.Errorf("raid applicant name exceeds native packed buffer")
	}
	body := make([]byte, 51)
	binary.LittleEndian.PutUint16(body, row.CharKey)
	body[4], body[5] = row.Channel, row.Server
	binary.LittleEndian.PutUint32(body[6:], row.ClientValue)
	binary.LittleEndian.PutUint16(body[10:], row.NativeValue)
	binary.LittleEndian.PutUint16(body[16:], row.Level)
	body[18], body[19] = row.Job, row.Grow
	copy(body[20:50], name)
	return body, nil
}
