package dnfbridge

import (
	"encoding/binary"
	"fmt"
)

// The fixed v158 mode8 USERINFO sequence carries one cross-scene actor made of
// the existing typed class0/op2 mode0 and mode3 records. The native dispatcher
// reads a u16 record count and then processes each pair in mode0/mode3 order;
// both input builders already write the server and owner-channel prefix used by
// the native local-actor branch. This builder only frames those established
// records, so it does not interpret or rewrite their entries.
const (
	currentCrossSceneMode8Subtype byte   = 8
	currentCrossSceneMode0Subtype byte   = 0
	currentCrossSceneMode3Subtype byte   = 3
	currentCrossSceneMode8Count   uint16 = 1

	currentCrossSceneSourcePrefixBytes = 3
	currentCrossSceneRecordHeaderBytes = 5
	currentCrossSceneMode0RawStateLen  = 0x47
	currentCrossSceneMode0KeyOffset    = currentCrossSceneRecordHeaderBytes + currentCrossSceneMode0RawStateLen
	currentCrossSceneMode3KeyOffset    = 13
)

// buildCurrentCrossSceneUserInfoBody frames one mode0/mode3 pair as a native
// mode8 body. The returned body owns its storage and neither input slice is
// modified. The record tails are copied byte-for-byte; their internal layouts
// stay owned by the typed builders.
func buildCurrentCrossSceneUserInfoBody(mode0, mode3 []byte) ([]byte, error) {
	mode0Key, err := validateCurrentCrossSceneRecordHeader(
		"mode0",
		mode0,
		currentCrossSceneMode0Subtype,
		currentCrossSceneMode0KeyOffset,
	)
	if err != nil {
		return nil, err
	}
	mode3Key, err := validateCurrentCrossSceneRecordHeader(
		"mode3",
		mode3,
		currentCrossSceneMode3Subtype,
		currentCrossSceneMode3KeyOffset,
	)
	if err != nil {
		return nil, err
	}
	if mode0[3] != mode3[3] {
		return nil, fmt.Errorf(
			"cross-scene USERINFO server mismatch: mode0=%d mode3=%d",
			mode0[3],
			mode3[3],
		)
	}
	if mode0[4] != mode3[4] {
		return nil, fmt.Errorf(
			"cross-scene USERINFO channel mismatch: mode0=%d mode3=%d",
			mode0[4],
			mode3[4],
		)
	}
	if mode0Key != mode3Key {
		return nil, fmt.Errorf(
			"cross-scene USERINFO object key mismatch: mode0=%d mode3=%d",
			mode0Key,
			mode3Key,
		)
	}

	var writer packetWriter
	writer.writeByte(currentCrossSceneMode8Subtype)
	writer.writeUint16(currentCrossSceneMode8Count)
	writer.writeBytes(mode0[currentCrossSceneSourcePrefixBytes:])
	writer.writeBytes(mode3[currentCrossSceneSourcePrefixBytes:])
	return writer.bytes(), nil
}

func validateCurrentCrossSceneRecordHeader(name string, body []byte, wantSubtype byte, keyOffset int) (uint16, error) {
	if len(body) < keyOffset+2 {
		return 0, fmt.Errorf(
			"%s body is too short for its single-record header and object key: %d bytes, need at least %d",
			name,
			len(body),
			keyOffset+2,
		)
	}
	if body[0] != wantSubtype {
		return 0, fmt.Errorf("%s body subtype is %d, want %d", name, body[0], wantSubtype)
	}
	if count := binary.LittleEndian.Uint16(body[1:3]); count != currentCrossSceneMode8Count {
		return 0, fmt.Errorf("%s body record count is %d, want %d", name, count, currentCrossSceneMode8Count)
	}
	key := binary.LittleEndian.Uint16(body[keyOffset : keyOffset+2])
	if key == 0 {
		return 0, fmt.Errorf("%s body object key is zero", name)
	}
	return key, nil
}
