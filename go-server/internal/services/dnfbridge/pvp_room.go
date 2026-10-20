package dnfbridge

import (
	"fmt"
)

const (
	currentPVPRoomInfoMsgID     uint16 = 0x0029
	currentPVPRoomStateMsgID    uint16 = 0x002A
	currentPVPSeatStateMsgID    uint16 = 0x002B
	currentPVPUserAreaMsgID     uint16 = 0x0017
	currentPVPUserState         byte   = 0x02
	currentPVPRoomState         byte   = 0x01
	currentPVPRoomManager       byte   = 0x00
	currentPVPRoomSeatCount            = 8
	currentPVPEntireRoomID      uint16 = 1
	currentPVPDefaultBattleMode byte   = 2
)

type currentPVPMakeRoomRequest struct {
	roomNameType      byte
	roomName          []byte
	mapIndex          int16
	hasPassword       bool
	password          []byte
	specialBattleMode byte
	dojoDifficulty    byte
}

func parseCurrentPVPMakeRoomRequest(body []byte) (currentPVPMakeRoomRequest, error) {
	var request currentPVPMakeRoomRequest
	if len(body) == 0 {
		return request, fmt.Errorf("pvp room request is empty")
	}
	offset := 0
	readByte := func() (byte, error) {
		if offset >= len(body) {
			return 0, fmt.Errorf("pvp room request truncated at %d", offset)
		}
		value := body[offset]
		offset++
		return value, nil
	}
	readInt16 := func() (int16, error) {
		if len(body)-offset < 2 {
			return 0, fmt.Errorf("pvp room request missing map index")
		}
		value := int16(uint16(body[offset]) | uint16(body[offset+1])<<8)
		offset += 2
		return value, nil
	}
	readBytes := func(max int, field string) ([]byte, error) {
		if len(body)-offset < 4 {
			return nil, fmt.Errorf("pvp room request missing %s length", field)
		}
		length := int(uint32(body[offset]) | uint32(body[offset+1])<<8 | uint32(body[offset+2])<<16 | uint32(body[offset+3])<<24)
		offset += 4
		if length < 0 || length > max || len(body)-offset < length {
			return nil, fmt.Errorf("pvp room request invalid %s length %d", field, length)
		}
		value := append([]byte(nil), body[offset:offset+length]...)
		offset += length
		return value, nil
	}

	var err error
	if request.roomNameType, err = readByte(); err != nil {
		return request, err
	}
	if request.roomNameType > 0x7f {
		return request, fmt.Errorf("pvp room name type exceeds signed-byte range")
	}
	if request.roomNameType == 0 {
		if request.roomName, err = readBytes(29, "room name"); err != nil {
			return request, err
		}
	}
	if request.mapIndex, err = readInt16(); err != nil {
		return request, err
	}
	var hasPassword byte
	if hasPassword, err = readByte(); err != nil {
		return request, err
	}
	if hasPassword > 1 {
		return request, fmt.Errorf("pvp room password flag is not boolean")
	}
	request.hasPassword = hasPassword == 1
	if request.hasPassword {
		if request.password, err = readBytes(8, "password"); err != nil {
			return request, err
		}
	}
	if request.specialBattleMode, err = readByte(); err != nil {
		return request, err
	}
	// sub_10AD640 special3 writes the PVF difficulty+1; other writers use zero.
	if request.specialBattleMode == 3 {
		if len(body)-offset != 1 || body[offset] < 1 || body[offset] > 3 {
			return request, fmt.Errorf("pvp dojo request has invalid difficulty")
		}
		request.dojoDifficulty = body[offset]
		offset++
	} else if len(body)-offset == 1 && body[offset] == 0 {
		offset++
	}
	if offset != len(body) {
		return request, fmt.Errorf("pvp room request has %d trailing bytes", len(body)-offset)
	}
	return request, nil
}

func buildCurrentPVPUserStateBody(objectKey uint16) ([]byte, error) {
	return buildCurrentPVPUserStateBodyWithState(objectKey, currentPVPUserState)
}

func buildCurrentPVPUserStateBodyWithState(objectKey uint16, state byte) ([]byte, error) {
	if objectKey == 0 {
		return nil, fmt.Errorf("pvp user state requires a nonzero object key")
	}
	return []byte{1, byte(objectKey), byte(objectKey >> 8), state}, nil
}

func buildCurrentPVPUserAreaBody(objectKey, x, y uint16, townID, direction, areaState byte) []byte {
	return buildCurrentTownUserAreaNotificationBody(
		objectKey,
		townID,
		0xfe,
		x,
		y,
		direction,
		areaState,
	)
}

func buildCurrentPVPRoomInfoBody(room *currentPVPRoom) []byte {
	request := room.request
	var writer packetWriter
	writer.writeUint16(1)
	writer.writeUint16(room.id)
	writer.writeByte(request.roomNameType)
	if request.roomNameType == 0 {
		writer.writeInt32(len(request.roomName))
		writer.writeBytes(request.roomName)
	}
	writer.writeByte(room.state())
	writer.writeByte(room.manager)
	writer.writeUint16(uint16(request.mapIndex))
	writer.writeByte(room.battleMode)
	// Current EXE sub_1D67AF0 reads only u8 state + u16 actor per seat.
	for seat := 0; seat < currentPVPRoomSeatCount; seat++ {
		if member := room.members[seat]; member != nil {
			writer.writeByte(room.seatState[seat])
			writer.writeUint16(member.characterID)
		} else {
			writer.writeByte(0xff)
			writer.writeUint16(0xffff)
		}
	}
	if request.hasPassword {
		writer.writeByte(1)
	} else {
		writer.writeByte(0)
	}
	writer.writeInt32(0)
	return writer.bytes()
}

func currentPVPBattleModeForMake(request currentPVPMakeRoomRequest) byte {
	if request.specialBattleMode == 1 {
		return 6
	}
	if request.specialBattleMode == 3 {
		// Native sub_10AFA10 mode10 starts the PVF arcade round list.
		return 10
	}
	return currentPVPDefaultBattleMode
}

func normalizeCurrentPVPBattleMode(mode byte) byte {
	if mode != 6 && !isCurrentPVPManualBattleMode(mode) {
		return currentPVPDefaultBattleMode
	}
	return mode
}

func isCurrentPVPManualBattleMode(mode byte) bool {
	// Current EXE 0x155932B sends 8 for Btn_TotalMatch; 6/10 are practice.
	return (mode >= 1 && mode <= 5) || mode == 8
}

func buildCurrentPVPAllSeatStatesBody(room *currentPVPRoom) []byte {
	// 0x1D5A110 consumes every seat before applying the mode. Publish them
	// together because 0x2738430 infers teams from the first occupied seat.
	body := []byte{byte(room.id), byte(room.id >> 8), room.battleMode, 0}
	for seat, member := range room.members {
		if member != nil {
			body[3]++
			body = append(body, byte(seat), room.seatState[seat], byte(member.characterID), byte(member.characterID>>8))
		}
	}
	return body
}

func buildCurrentPVPSeatStateBody(roomID uint16, battleMode, seat, state byte, userID uint16) []byte {
	var writer packetWriter
	writer.writeUint16(roomID)
	writer.writeByte(battleMode)
	writer.writeByte(1)
	writer.writeByte(seat)
	writer.writeByte(state)
	writer.writeUint16(userID)
	return writer.bytes()
}

func buildCurrentPVPDestroyedRoomStateBody(roomID uint16) []byte {
	return buildCurrentPVPRoomStateBody(roomID, 0, 0, 0, currentPVPDefaultBattleMode, 0)
}

func buildCurrentPVPRoomStateBody(roomID uint16, roomState, manager byte, mapIndex int16, battleMode byte, matchingType int32) []byte {
	var writer packetWriter
	writer.writeUint16(roomID)
	writer.writeByte(roomState)
	writer.writeByte(manager)
	writer.writeUint16(uint16(mapIndex))
	writer.writeByte(battleMode)
	writer.writeInt32(int(matchingType))
	return writer.bytes()
}
