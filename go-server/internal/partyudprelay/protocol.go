package partyudprelay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"
)

// Wire v2 control protocol constants matching dnfbridge/party_udp_relay_remote.go.
const (
	OpHello    byte = 0x01
	OpSync     byte = 0x02
	OpClose    byte = 0x03
	OpHelloOK  byte = 0x81
	OpSyncOK   byte = 0x82
	OpSyncFail byte = 0x83
	OpCloseOK  byte = 0x84

	ProtocolVersion byte = 2
	ScopeParty      byte = 0
	SyncModeMembers byte = 0

	MaxFrameBytes    = 8 * 1024
	FrameHeaderBytes = 3
	SyncHeaderBytes  = 8
	MaxRoomMembers   = 8
	SecretHexLength  = 64
	SecretByteLength = 32
	HelloMACContext  = "A21RELAY-HELLO/v1"

	SyncFailInvalidMode    byte = 1
	SyncFailInvalidRoom    byte = 2
	SyncFailInvalidMembers byte = 3
	SyncFailPortExhausted  byte = 4
	SyncFailServerClosed   byte = 5
)

// ParseSecretHex validates and decodes a 64-hex (32-byte) shared secret.
// The status string is safe for logging and never contains secret material.
func ParseSecretHex(raw string) ([]byte, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, "missing"
	}
	if len(trimmed) != SecretHexLength {
		return nil, "invalid"
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil || len(decoded) != SecretByteLength {
		return nil, "invalid"
	}
	return decoded, "configured"
}

func computeHelloMAC(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(HelloMACContext))
	return mac.Sum(nil)
}

func verifyHelloPayload(secret, payload []byte) bool {
	if len(secret) != SecretByteLength || len(payload) != 1+sha256.Size || payload[0] != ProtocolVersion {
		return false
	}
	expected := computeHelloMAC(secret)
	return hmac.Equal(payload[1:], expected)
}

func normalizeMembers(keys []uint16) []uint16 {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[uint16]struct{}, len(keys))
	result := make([]uint16, 0, len(keys))
	for _, key := range keys {
		if key == 0 {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var header [FrameHeaderBytes]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := int(binary.BigEndian.Uint16(header[1:3]))
	if length > MaxFrameBytes {
		return header[0], nil, fmt.Errorf("relay frame length %d exceeds max %d", length, MaxFrameBytes)
	}
	var payload []byte
	if length > 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			return header[0], nil, err
		}
	}
	return header[0], payload, nil
}

func writeFrame(conn net.Conn, opcode byte, payload []byte, timeout time.Duration) error {
	if len(payload) > MaxFrameBytes {
		return fmt.Errorf("relay response length %d exceeds max %d", len(payload), MaxFrameBytes)
	}
	frame := make([]byte, FrameHeaderBytes+len(payload))
	frame[0] = opcode
	binary.BigEndian.PutUint16(frame[1:3], uint16(len(payload)))
	copy(frame[FrameHeaderBytes:], payload)
	if timeout > 0 {
		if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
	}
	for written := 0; written < len(frame); {
		n, err := conn.Write(frame[written:])
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		written += n
	}
	return nil
}

type syncRequest struct {
	scope   byte
	roomID  uint32
	mode    byte
	members []uint16
}

func decodeSyncRequest(payload []byte) (syncRequest, bool) {
	if len(payload) < SyncHeaderBytes {
		return syncRequest{}, false
	}
	count := int(binary.BigEndian.Uint16(payload[6:8]))
	if len(payload) != SyncHeaderBytes+count*2 {
		return syncRequest{}, false
	}
	members := make([]uint16, count)
	for i := 0; i < count; i++ {
		off := SyncHeaderBytes + i*2
		members[i] = binary.BigEndian.Uint16(payload[off : off+2])
	}
	return syncRequest{
		scope:   payload[0],
		roomID:  binary.BigEndian.Uint32(payload[1:5]),
		mode:    payload[5],
		members: members,
	}, true
}

func decodeCloseRequest(payload []byte) (byte, uint32, bool) {
	if len(payload) != 5 {
		return 0, 0, false
	}
	return payload[0], binary.BigEndian.Uint32(payload[1:5]), true
}

type legAllocation struct {
	owner uint16
	peer  uint16
	port  uint16
}

func encodeSyncOK(roomID uint32, legs []legAllocation) []byte {
	payload := make([]byte, 6+len(legs)*6)
	binary.BigEndian.PutUint32(payload[0:4], roomID)
	binary.BigEndian.PutUint16(payload[4:6], uint16(len(legs)))
	for i, leg := range legs {
		off := 6 + i*6
		binary.BigEndian.PutUint16(payload[off:off+2], leg.owner)
		binary.BigEndian.PutUint16(payload[off+2:off+4], leg.peer)
		binary.BigEndian.PutUint16(payload[off+4:off+6], leg.port)
	}
	return payload
}

func encodeSyncFail(roomID uint32, reason byte) []byte {
	payload := make([]byte, 5)
	binary.BigEndian.PutUint32(payload[0:4], roomID)
	payload[4] = reason
	return payload
}

func encodeCloseOK(roomID uint32) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload[0:4], roomID)
	return payload
}
