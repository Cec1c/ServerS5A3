package repository

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// ActivityPayloadFingerprintV1 encodes the business fields compared by
// SameActivityEvent. Callers validate new events separately. Historical rows
// can be fingerprinted without reinterpreting their display name or date.
// Fields have bounded persisted widths; each UTF-8 value has a four-byte
// big-endian byte length, and integers use their exact decimal representation.
func ActivityPayloadFingerprintV1(e ActivityEvent) [32]byte {
	fields := [...]string{e.AccountID, e.ID, e.CharacterID, e.Metric,
		strconv.FormatInt(e.Amount, 10), strconv.FormatInt(e.DungeonID, 10),
		strconv.FormatUint(uint64(e.ItemID), 10), strconv.FormatInt(e.Rarity, 10), e.Source}
	h := sha256.New()
	var size [4]byte
	for _, field := range fields {
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(field))
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
