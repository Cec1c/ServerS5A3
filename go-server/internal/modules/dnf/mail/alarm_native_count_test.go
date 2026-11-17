package mail

import (
	"encoding/binary"
	"testing"
)

func TestAlarmCountRemainsPositiveForNativeSignedWord(t *testing.T) {
	for _, c := range []struct{ input, want int }{{-1, 0}, {0, 0}, {1, 1}, {513, 513}, {32767, 32767}, {32768, 32767}, {65535, 32767}, {100000, 32767}} {
		b := BuildAlarmNotification(c.input)
		if len(b) != 2 || int(int16(binary.LittleEndian.Uint16(b))) != c.want {
			t.Fatalf("count=%d wire=%x want=%d", c.input, b, c.want)
		}
	}
}
