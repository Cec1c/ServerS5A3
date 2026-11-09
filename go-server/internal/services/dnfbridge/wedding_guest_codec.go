package dnfbridge

import "encoding/binary"

const currentWeddingSendLetterOpcode uint16 = 1038
const currentWeddingMoneyGiftOpcode uint16 = 1034
const currentWeddingGuestLetterNotice uint16 = 1005
const currentWeddingGuestTargetsNotice uint16 = 1006

type currentWeddingGuestLetterRequest struct {
	Random    bool
	Target    uint32
	RuntimeID uint16
	Hint      byte
}

func decodeCurrentWeddingGuestLetterRequest(body []byte) (currentWeddingGuestLetterRequest, bool) {
	if len(body) != 21 || body[13] > 1 {
		return currentWeddingGuestLetterRequest{}, false
	}
	q := currentWeddingGuestLetterRequest{Random: body[13] == 1, Target: binary.LittleEndian.Uint32(body[14:]), RuntimeID: binary.LittleEndian.Uint16(body[18:]), Hint: body[20]}
	if q.Random {
		return q, q.Target == 0 && q.RuntimeID == 0 && q.Hint == 0
	}
	return q, q.Target > 0 && q.Target <= 65535
}

type currentWeddingMoneyGiftRequest struct {
	Key  uint32
	Gold int64
	Wish string
}

func decodeCurrentWeddingMoneyGiftRequest(body []byte) (currentWeddingMoneyGiftRequest, bool) {
	if len(body) < 12 {
		return currentWeddingMoneyGiftRequest{}, false
	}
	// Server-side bound shared with the native comment-board text field; this
	// does not claim that the original 1034 input widget itself limits to 255.
	// 1034 writes scalar fields after Begin's outer 13-byte transport header.
	// ParseChannelPacket has already removed that header; unlike raw-struct
	// commands, this body does not contain another 13-byte prefix.
	wish, valid := parseCurrentGuildText(body[8:], 255)
	q := currentWeddingMoneyGiftRequest{Key: binary.LittleEndian.Uint32(body), Gold: int64(binary.LittleEndian.Uint32(body[4:])), Wish: wish}
	return q, valid && q.Key != 0
}

// These native readers consume result after either discriminator. The success
// suffix is present only for flag1/result0; all failures retain their i32 code.
func buildCurrentWeddingGuestLetterACK(flag byte, result int32, remaining uint16) []byte {
	body := make([]byte, 5)
	body[0] = flag
	binary.LittleEndian.PutUint32(body[1:], uint32(result))
	if flag == 1 && result == 0 {
		body = append(body, byte(remaining), byte(remaining>>8))
	}
	return body
}

func buildCurrentWeddingMoneyGiftACK(flag byte, result int32, key uint32) []byte {
	body := make([]byte, 5)
	body[0] = flag
	binary.LittleEndian.PutUint32(body[1:], uint32(result))
	if flag == 1 && result == 0 {
		body = binary.LittleEndian.AppendUint32(body, key)
	}
	return body
}

func buildCurrentWeddingGuestLetter(key, area, deadline, channel uint32, kind byte, firstName, secondName []byte, gifted bool) []byte {
	body := make([]byte, 82)
	binary.LittleEndian.PutUint32(body, key)
	binary.LittleEndian.PutUint32(body[4:], area)
	binary.LittleEndian.PutUint32(body[8:], deadline)
	binary.LittleEndian.PutUint32(body[12:], channel)
	body[16] = kind
	copy(body[17:46], firstName)
	copy(body[47:76], secondName)
	if gifted {
		body[77] = 1
	}
	// +78 is retained as zero: only storage/copy, not a semantic consumer, has
	// been proved for the current EXE. It is not a fabricated gold/character ID.
	return body
}

func buildCurrentWeddingGuestTargets(ids []uint32) ([]byte, bool) {
	if len(ids) > 255 {
		return nil, false
	}
	body := []byte{byte(len(ids))}
	seen := map[uint32]bool{}
	for _, id := range ids {
		if id == 0 || id > 65535 || seen[id] {
			return nil, false
		}
		seen[id] = true
		body = binary.LittleEndian.AppendUint32(body, id)
	}
	return body, true
}
