package pet

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// CurrentCreatureNameUTF16Limit is the largest name that fits the current
// client's 13-WCHAR destination buffer after its terminating NUL.
const CurrentCreatureNameUTF16Limit = 12

// The peer reader also limits the encoded DSTR to 25 bytes. BMP characters
// outside GBK can need four GB18030 bytes despite using only one WCHAR.
const CurrentCreatureNameByteLimit = 25

// CreatureNameForWire returns the longest valid UTF-8 rune prefix that fits in
// the current client's creature-name buffer. A NUL ends the name, and invalid
// UTF-8 is never copied to the wire.
func CreatureNameForWire(name string) string {
	if end := strings.IndexByte(name, 0); end >= 0 {
		name = name[:end]
	}
	end := 0
	units := 0
	encodedBytes := 0
	for end < len(name) {
		r, size := utf8.DecodeRuneInString(name[end:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(string(r)))
		if err != nil || units+runeUnits > CurrentCreatureNameUTF16Limit || encodedBytes+len(encoded) > CurrentCreatureNameByteLimit {
			break
		}
		units += runeUnits
		encodedBytes += len(encoded)
		end += size
	}
	return name[:end]
}

// DefaultCreatureNameForWire keeps the default-name template suffix when an
// overlong name contains ASCII spacing. Names already within the current
// client's limit keep their spacing unchanged.
func DefaultCreatureNameForWire(name string) string {
	logicalName := name
	if end := strings.IndexByte(logicalName, 0); end >= 0 {
		logicalName = logicalName[:end]
	}
	if CreatureNameForWire(logicalName) != logicalName {
		logicalName = strings.ReplaceAll(logicalName, " ", "")
	}
	return CreatureNameForWire(logicalName)
}

// CreatureNameRawForWire validates a persisted GB18030 name by round-tripping
// it and projects it to the current client's UTF-16 limit. Invalid encodings
// return nil. A short canonical encoding is returned byte-for-byte in an
// independent slice; a long encoding is re-encoded from complete runes.
func CreatureNameRawForWire(raw []byte) []byte {
	decoded, ok := decodeCreatureNameRaw(raw)
	if !ok {
		return nil
	}
	logicalRaw := raw
	logicalName := decoded
	if end := bytes.IndexByte(raw, 0); end >= 0 {
		logicalRaw = logicalRaw[:end]
		logicalName = logicalName[:strings.IndexByte(logicalName, 0)]
	}
	if creatureNameUTF16Units(logicalName) <= CurrentCreatureNameUTF16Limit && len(logicalRaw) <= CurrentCreatureNameByteLimit {
		projected := make([]byte, len(logicalRaw))
		copy(projected, logicalRaw)
		return projected
	}

	projectedName := CreatureNameForWire(logicalName)
	projected, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(projectedName))
	if err != nil {
		return nil
	}
	out := make([]byte, len(projected))
	copy(out, projected)
	return out
}

func decodeCreatureNameRaw(raw []byte) (string, bool) {
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return "", false
	}
	canonical, err := simplifiedchinese.GB18030.NewEncoder().Bytes(decoded)
	if err != nil || !bytes.Equal(canonical, raw) {
		return "", false
	}
	return string(decoded), true
}

func creatureNameUTF16Units(name string) int {
	units := 0
	for len(name) > 0 {
		r, size := utf8.DecodeRuneInString(name)
		if r == utf8.RuneError && size == 1 {
			break
		}
		if r > 0xffff {
			units += 2
		} else {
			units++
		}
		name = name[size:]
	}
	return units
}

// validateCreatureRenameNameWire applies the byte-preserving rename checks to
// the canonical GB18030 name and verifies its current-client UTF-16 bound.
func validateCreatureRenameNameWire(raw []byte) error {
	decoded, ok := decodeCreatureNameRaw(raw)
	if !ok {
		return fmt.Errorf("creature name is not canonical GB18030")
	}
	if units := len(utf16.Encode([]rune(decoded))); units > CurrentCreatureNameUTF16Limit {
		return fmt.Errorf("creature name is %d UTF-16 units, maximum is %d", units, CurrentCreatureNameUTF16Limit)
	}
	return nil
}
