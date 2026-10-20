package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Native official-24.9 writer: job, u32 name length, name, nine u8 options.
// Regress the observed early cut that strands the ninth byte ahead of op4.
func TestOfficialCreateKeepsNinthOptionAndFollowingSelection(t *testing.T) {
	for _, name := range []string{"c51", "boundary123", "abcdefghijklmnopqrstuvwxyz1234"} {
		body := append([]byte{0, 0, 0, 0, 0}, []byte(name)...)
		binary.LittleEndian.PutUint32(body[1:5], uint32(len(name)))
		body = append(body, 0, 0, 0, 0, 0, 0, 0, 0, 1)
		create, err := BuildChannelPacket(5, body, 28, DefaultChannelClassification)
		if err != nil {
			t.Fatal(err)
		}
		heartbeat, err := BuildChannelPacket(1276, nil, 29, DefaultChannelClassification)
		if err != nil {
			t.Fatal(err)
		}
		selection, err := BuildChannelPacket(4, []byte{0, 0}, 30, DefaultChannelClassification)
		if err != nil {
			t.Fatal(err)
		}
		stream := append(append(append([]byte(nil), create...), heartbeat...), selection...)
		// Every fragmentation point must preserve the genuine frame boundaries.
		for cut := 0; cut <= len(stream); cut++ {
			first, pending, skipped, err := SplitLatestGameStream(stream[:cut], 65535)
			if err != nil || skipped != 0 {
				t.Fatalf("name=%s cut=%d err=%v skipped=%d", name, cut, err, skipped)
			}
			second, remaining, skipped, err := SplitLatestGameStream(append(pending, stream[cut:]...), 65535)
			packets := append(first, second...)
			if err != nil || skipped != 0 || len(remaining) != 0 || len(packets) != 3 {
				t.Fatalf("name=%s cut=%d err=%v skipped=%d pending=%d packets=%d", name, cut, err, skipped, len(remaining), len(packets))
			}
			for i, want := range [][]byte{create, heartbeat, selection} {
				if packets[i].Kind != LatestGameStreamUpper || !bytes.Equal(packets[i].Data, want) {
					t.Fatalf("name=%s cut=%d packet=%d genuine bytes or framing changed", name, cut, i)
				}
			}
		}
	}
}
