package dnfbridge

import (
	"encoding/binary"
	"fmt"

	"longheng.io/server/internal/modules/dnf/onlineevent"
)

const attendanceMarbleBoardSize = onlineevent.MarbleBoardSize
const attendanceMarbleCalendarDayKey = onlineevent.MarbleDayKey
const (
	attendanceMarbleBuffUniqueDrop = 29
	attendanceMarbleBuffMonsterExp = 30
	attendanceMarbleBuffClearExp   = 31
	attendanceMarbleBuffClearHunt  = 32
)

type attendanceMarbleCatalog = onlineevent.MarbleCatalog
type attendanceMarbleState = onlineevent.MarbleState

func attendanceMarbleBuffKey(index int) string {
	return fmt.Sprintf("attendance_marble_buff_%d", index)
}

func parseAttendanceMarbleRequestInfo(body []byte) error {
	if len(body) != 0 {
		return fmt.Errorf("attendance marble info body length=%d want=0", len(body))
	}
	return nil
}
func parseAttendanceMarbleRequestDice(body []byte) (byte, error) {
	if len(body) != 1 {
		return 0, fmt.Errorf("attendance marble dice body length=%d want=1", len(body))
	}
	return body[0], nil
}

// class1/629成功标志后的正文：保留值、总次数、剩余次数、位置、保留值、圈数。
func buildAttendanceMarbleInfoBody(state attendanceMarbleState) []byte {
	var body [16]byte
	binary.LittleEndian.PutUint16(body[2:], uint16(state.Turns))
	binary.LittleEndian.PutUint16(body[4:], uint16(state.Remaining()))
	binary.LittleEndian.PutUint16(body[6:], uint16(state.Position))
	binary.LittleEndian.PutUint32(body[12:], uint32(state.Round))
	return body[:]
}

// class1/630成功标志后的正文：两个骰子、剩余次数和圈数。
func buildAttendanceMarbleDiceBody(dice [2]byte, remaining, round int64) []byte {
	var body [8]byte
	body[0], body[1] = dice[0], dice[1]
	binary.LittleEndian.PutUint16(body[2:], uint16(remaining))
	binary.LittleEndian.PutUint32(body[4:], uint32(round))
	return body[:]
}
