package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"
)

func TestAccountFriendProjectionPreservesRealUint16Channel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		channel int
		want    uint16
	}{
		{"ordinary", 11, 11},
		{"byte_maximum", 255, 255},
		{"above_byte", 256, 256},
		{"large_channel", 300, 300},
		{"uint16_maximum", 65535, 65535},
		{"zero", 0, 0},
		{"negative", -1, 0},
		{"above_uint16", 65536, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, b, _, _ := prepareAccountFriendTest(t)
			callAccountFriend(t, s, a, 1436, "Beta", "申请", 0)
			callAccountFriend(t, s, b, 1437, "Alpha", "", 0)
			b.channel.ID = tc.channel
			b.residentChannel.ID = 11
			rows, _, err := s.currentAccountFriendProjection(context.Background(), "dnf:1")
			if err != nil || len(rows) != 2 || rows[0].Channel != tc.want || rows[1].Channel != 0 {
				t.Fatalf("projection rows=%+v err=%v, want real channel=%d and offline=0", rows, err, tc.want)
			}
			body, err := buildCurrentAccountFriendInfoBody(rows)
			if err != nil {
				t.Fatal(err)
			}
			// Skip count, head and state, then the real adventure-name DSTR.
			_, next, ok := readCurrentRawDstr(body, 4)
			if !ok || next+5 > len(body) {
				t.Fatalf("invalid native row body=%x", body)
			}
			if got := binary.LittleEndian.Uint16(body[next+3 : next+5]); got != tc.want {
				t.Fatalf("NOTI1371 wire channel=%d, want=%d", got, tc.want)
			}
			s.unbindGameSession(b)
			rows, _, err = s.currentAccountFriendProjection(context.Background(), "dnf:1")
			if err != nil || len(rows) != 2 || rows[0].Channel != 0 {
				t.Fatalf("offline friendship disappeared or kept channel: rows=%+v err=%v", rows, err)
			}
		})
	}
}
