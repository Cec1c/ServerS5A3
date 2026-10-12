package protocol

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func TestSplitV56LoginBeforeLegacyAndAcrossReads(t *testing.T) {
	for _, size := range []int{606, 610} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := make([]byte, size)
			binary.LittleEndian.PutUint32(body, 1)
			body[4] = 0x30
			binary.LittleEndian.PutUint32(body[5:9], 32)
			copy(body[9:], "de509f65000000000000000000000000") // Synthetic token, never a live credential.
			packet, err := BuildChannelPacket(1, body, 0, DefaultChannelClassification)
			if err != nil {
				t.Fatal(err)
			}
			for _, cut := range []int{1, 12, 13, 100, len(packet) - 1} {
				first, rest, skipped, err := SplitLatestGameStream(packet[:cut], 65535)
				if err != nil || skipped != 0 || len(first) != 0 {
					t.Fatalf("partial %d: %v %d %v", cut, first, skipped, err)
				}
				complete, rest, skipped, err := SplitLatestGameStream(append(rest, packet[cut:]...), 65535)
				if err != nil || skipped != 0 || len(rest) != 0 || len(complete) != 1 || complete[0].Kind != LatestGameStreamUpper {
					t.Fatalf("v56 login did not reach upper decoder: %v %d %v", complete, skipped, err)
				}
			}
		})
	}
	for _, size := range []int{605, 607, 609, 611} {
		if isKnownClientUpperMsg(1, make([]byte, size)) {
			t.Fatalf("accepted adjacent length %d", size)
		}
	}
}
