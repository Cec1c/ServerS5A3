package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWeddingCodecNativeRequestResponseAndInfo(t *testing.T) {
	b := bytes.Repeat([]byte{0xa5}, 21)
	binary.LittleEndian.PutUint32(b[13:], 54321)
	b[17], b[18], b[19], b[20] = 2, 1, 2, 0
	r, ok := decodeCurrentWeddingRequest(b[:20])
	if !ok || r.Target != 54321 || r.Operation != 2 || r.Kind != 1 || r.Duration != 2 {
		t.Fatalf("request %+v %v", r, ok)
	}
	response, ok := decodeCurrentWeddingResponse(b)
	if !ok || response.Answer != 0 || response.currentWeddingRequest != r {
		t.Fatalf("response %+v %v", response, ok)
	}
	if got := buildCurrentWeddingRequestRecord(r, 0, true); !bytes.Equal(got, []byte{1, 0, 0, 0, 0, 0x31, 0xd4, 0, 0, 2, 1, 2}) {
		t.Fatalf("request ACK %x", got)
	}
	if got := buildCurrentWeddingResponseRecord(response, -2, false); !bytes.Equal(got, []byte{0xfe, 0xff, 0xff, 0xff, 0x31, 0xd4, 0, 0, 2, 1, 2, 0}) {
		t.Fatalf("response notice %x", got)
	}
	got := buildCurrentWeddingInfo(0x1234, 4, 2, 5, 0x01020304, 999)
	if !bytes.Equal(got, []byte{0x34, 0x12, 0, 0, 4, 2, 4, 3, 2, 1, 0xe7, 3, 0, 0, 5}) {
		t.Fatalf("info %x", got)
	}
	if got := buildCurrentWeddingPeerInfo(7, 8, 4, 5, 1000); !bytes.Equal(got, []byte{7, 0, 0, 0, 8, 0, 0, 0, 4, 5, 0xe8, 3, 0, 0}) {
		t.Fatalf("peer info %x", got)
	}
}

func TestWeddingCodecRejectsShortTrailingAndUnownedIDs(t *testing.T) {
	b := make([]byte, 21)
	b[17] = 1
	for _, size := range []int{0, 7, 13, 19, 21} {
		if _, ok := decodeCurrentWeddingRequest(b[:size]); ok {
			t.Fatalf("accepted size %d", size)
		}
	}
	binary.LittleEndian.PutUint32(b[13:], 65536)
	if _, ok := decodeCurrentWeddingRequest(b[:20]); ok {
		t.Fatal("accepted 32-bit runtime ID")
	}
	binary.LittleEndian.PutUint32(b[13:], 3)
	b[20] = 3
	if _, ok := decodeCurrentWeddingResponse(b); ok {
		t.Fatal("accepted unknown consent value")
	}
	for _, op := range []byte{0, 3, 4, 255} {
		b[17] = op
		if _, ok := decodeCurrentWeddingRequest(b[:20]); ok {
			t.Fatalf("accepted client phase %d", op)
		}
	}
	ring := make([]byte, 18)
	binary.LittleEndian.PutUint32(ring[13:], 105)
	ring[17] = 5
	if slot, stage, ok := decodeCurrentWeddingRing(ring); !ok || slot != 105 || stage != 5 {
		t.Fatalf("ring %d/%d %v", slot, stage, ok)
	}
	if _, _, ok := decodeCurrentWeddingRing(append(ring, 0)); ok {
		t.Fatal("accepted extra ring payload")
	}
}
