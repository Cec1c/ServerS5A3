package dnfbridge

import (
	"encoding/binary"
	"math"
)

// Current DNF.exe SHA256 534b5ef1...d51b24:
// 1ACF70A/1AD3173 append 20B; only bytes 13..19 are request fields.
// 1ACE730 appends 21B; byte20 is 0=consent, 1=refusal, 2=timeout.
// The native prefix is opaque and is never trusted as an owner/character ID.
type currentWeddingRequest struct {
	Target                    uint32
	Operation, Kind, Duration byte
}

type currentWeddingResponse struct {
	currentWeddingRequest
	Answer byte
}

const (
	currentWeddingRequestOpcode   uint16 = 1032
	currentWeddingResponseOpcode  uint16 = 1033
	currentWeddingCharacterOpcode uint16 = 1035
	currentWeddingRingOpcode      uint16 = 1036
	currentWeddingRequestNotice   uint16 = 997
	currentWeddingResponseNotice  uint16 = 998
	currentWeddingInfoNotice      uint16 = 1000
	currentWeddingRingNotice      uint16 = 1001
)

func decodeCurrentWeddingRequest(body []byte) (currentWeddingRequest, bool) {
	if len(body) != 20 {
		return currentWeddingRequest{}, false
	}
	r := currentWeddingRequest{Target: binary.LittleEndian.Uint32(body[13:17]), Operation: body[17], Kind: body[18], Duration: body[19]}
	if r.Target > math.MaxUint16 {
		return currentWeddingRequest{}, false
	}
	switch r.Operation {
	case 1, 2, 5, 6, 7:
		return r, true
	}
	return currentWeddingRequest{}, false
}

func decodeCurrentWeddingResponse(body []byte) (currentWeddingResponse, bool) {
	if len(body) != 21 {
		return currentWeddingResponse{}, false
	}
	r, ok := decodeCurrentWeddingRequest(body[:20])
	if !ok || (r.Operation != 1 && r.Operation != 2 && r.Operation != 7) || body[20] > 2 {
		return currentWeddingResponse{}, false
	}
	return currentWeddingResponse{r, body[20]}, true
}

// Request ACK 1AD8EC0 and NOTI 1AD5A90 consume the same eleven-byte record.
// Response ACK 1AD6190 and NOTI 1AD5B20 consume the twelve-byte variant.
// The ACK dispatch flag precedes the record; semantic result=0 means success.
func buildCurrentWeddingRequestRecord(r currentWeddingRequest, result int32, ack bool) []byte {
	start := 0
	if ack {
		start = 1
	}
	b := make([]byte, start+11)
	if ack {
		b[0] = 1
	}
	binary.LittleEndian.PutUint32(b[start:], uint32(result))
	binary.LittleEndian.PutUint32(b[start+4:], r.Target)
	b[start+8], b[start+9], b[start+10] = r.Operation, r.Kind, r.Duration
	return b
}

func buildCurrentWeddingResponseRecord(r currentWeddingResponse, result int32, ack bool) []byte {
	b := buildCurrentWeddingRequestRecord(r.currentWeddingRequest, result, ack)
	return append(b, r.Answer)
}

// 1AD9CC0 consumes a packed 15B WeddingInfo, then 1AD9480 projects the
// partner, phase, ceremony kind, marriage date, love points and ring stage.
// 1ABCCF4 -> 1AD59D0 reads the date used in elapsed days; 37E9F60 reads
// love points for the same couple-information panel (1ABCA80).
func buildCurrentWeddingInfo(partner uint32, phase, kind, ring byte, marriedAt, lovePoints uint32) []byte {
	b := make([]byte, 15)
	binary.LittleEndian.PutUint32(b, partner)
	b[4], b[5] = phase, kind
	binary.LittleEndian.PutUint32(b[6:], marriedAt)
	binary.LittleEndian.PutUint32(b[10:], lovePoints)
	b[14] = ring
	return b
}
