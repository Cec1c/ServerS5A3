package raid

import (
	"encoding/binary"
	"fmt"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const CurrentRaidProfilesMsgID uint16 = 752

// CurrentProfileRecord supplies character properties to the actual class0/752
// reader at 02019900. The 57-byte record creates a category1 raid UI actor,
// separate from the town actor manager. Channel/server are the actual raid
// context, and all character properties must come from authoritative records.
type CurrentProfileRecord struct {
	CharKey uint16
	Channel byte
	Server  byte
	Level   uint16
	Job     byte
	Grow    byte
	Name    string
}

// BuildCurrentRaidProfilesBody encodes u32 count and fixed packed records.
// Native row+2 cache metadata, row+9 auxiliary property and row+40 auxiliary
// name are unavailable in this projection and remain zero/empty. They are not
// used to assert online status, admission, guild membership or adventure data.
func BuildCurrentRaidProfilesBody(rows []CurrentProfileRecord) ([]byte, error) {
	if len(rows) > MaxAttackPartyMembers {
		return nil, fmt.Errorf("raid profiles exceed native member limit")
	}
	body := make([]byte, 4+57*len(rows))
	binary.LittleEndian.PutUint32(body, uint32(len(rows)))
	seen := make(map[uint16]bool, len(rows))
	for i, row := range rows {
		if row.CharKey == 0 || seen[row.CharKey] || row.Channel == 0 || row.Level == 0 || strings.ContainsRune(row.Name, 0) {
			return nil, fmt.Errorf("raid profile has invalid authoritative identity")
		}
		seen[row.CharKey] = true
		name, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(row.Name))
		if err != nil || len(name) == 0 || len(name) >= 30 {
			return nil, fmt.Errorf("raid profile name exceeds native packed buffer")
		}
		packed := body[4+57*i : 4+57*(i+1)]
		binary.LittleEndian.PutUint16(packed, row.CharKey)
		packed[3] = row.Channel
		binary.LittleEndian.PutUint16(packed[4:], row.Level)
		packed[6], packed[7], packed[8] = row.Grow, row.Job, row.Server
		copy(packed[10:40], name)
	}
	return body, nil
}
