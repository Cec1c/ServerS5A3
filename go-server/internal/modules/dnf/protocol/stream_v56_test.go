package protocol

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func TestSplitV56LoginBeforeLegacyAndAcrossReads(t *testing.T) {
	for _, size := range []int{586, 590, 594, 598, 602, 606, 610, 614, 630, 838} {
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
	for _, size := range []int{582, 585, 587, 593, 595, 601, 603, 605, 607, 609, 611, 613, 615, 858} {
		if isKnownClientUpperMsg(1, make([]byte, size)) {
			t.Fatalf("accepted invalid/adjacent length %d", size)
		}
	}
}

func TestClientLoginStructuredIPv4CountValidation(t *testing.T) {
	buildStructured := func(ipv4Count int, optional16 bool) []byte {
		size := clientLoginBaseBodySize + clientLoginIPv4EntrySize*ipv4Count
		countOffset := clientLoginIPv4CountOffset
		if optional16 {
			size += clientLoginOptionalBlockSize
			countOffset += clientLoginOptionalBlockSize
		}
		body := make([]byte, size)
		binary.LittleEndian.PutUint32(body[0:4], 1)
		body[4] = 0x30
		binary.LittleEndian.PutUint32(body[5:9], 32)
		copy(body[9:], "de509f65000000000000000000000000")
		binary.LittleEndian.PutUint32(body[clientLoginFixedBlockOffset:clientLoginFixedBlockOffset+4], clientLoginFixedBlockSize)
		body[countOffset] = byte(ipv4Count)
		return body
	}

	for _, n := range []int{1, 4, 5, 6, 7, 8, 12, 64} {
		if !isKnownClientUpperMsg(1, buildStructured(n, false)) {
			t.Fatalf("structured login with N=%d (%d bytes) rejected", n, 582+4*n)
		}
	}
	if !isKnownClientUpperMsg(1, buildStructured(5, true)) {
		t.Fatal("structured login with optional 16-byte arg1 and N=5 rejected")
	}

	mismatch := buildStructured(5, false)
	mismatch[clientLoginIPv4CountOffset] = 4
	if isKnownClientUpperMsg(1, mismatch) {
		t.Fatal("structured login with mismatched IPv4 count accepted")
	}
	badHeader := buildStructured(5, false)
	binary.LittleEndian.PutUint32(badHeader[5:9], 16)
	if isKnownClientUpperMsg(1, badHeader) {
		t.Fatal("structured login with bad token length header accepted")
	}
}
