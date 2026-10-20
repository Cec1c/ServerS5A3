package raid

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentInfoReplyNativeFixtures(t *testing.T) {
	for _, selector := range []byte{1, 2} {
		for _, count := range []int{1, 2, 20} {
			members := currentCodecFixtureMembers(count)
			info := CurrentMetadata{RaidKey: 0x00170001, NameBytes: []byte("view"), Leader: members[0], Members: members}
			body, err := BuildCurrentInfoReplyBody(info, selector)
			if err != nil || !bytes.Equal(body[:6], []byte{1, selector, 1, 0, 23, 0}) {
				t.Fatalf("ACK1359 header: %x %v", body, err)
			}
			// Independent native boundaries: selector1 starts with count; selector2
			// starts with the repeated object key. Neither has NOTI591's mode u32.
			if selector == 1 && body[6] != byte(count) || selector == 2 && binary.LittleEndian.Uint32(body[6:10]) != info.RaidKey {
				t.Fatal("wrong native payload branch")
			}
			if dir := os.Getenv("DNF90_CURRENT_RAID_INFO_NATIVE_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("info-%d-%d.bin", selector, count)), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestCurrentInfoReplyRejectsInvalidNativeOwners(t *testing.T) {
	members := currentCodecFixtureMembers(1)
	for _, kind := range []string{"key", "selector", "leader", "absent", "duplicate", "capacity", "member"} {
		t.Run(kind, func(t *testing.T) {
			info := CurrentMetadata{RaidKey: 0x00170001, Leader: members[0], Members: members}
			selector := byte(2)
			switch kind {
			case "key":
				info.RaidKey = 0
			case "selector":
				selector = 3
			case "leader":
				info.Leader.Core.CharKey = 0
			case "absent":
				info.Leader.Core.CharKey = 99
			case "duplicate":
				info.Members = []CurrentMemberRecord{members[0], members[0]}
			case "capacity":
				info.Members = currentCodecFixtureMembers(21)
			case "member":
				selector = 1
				info.Members = []CurrentMemberRecord{{}}
			}
			if body, err := BuildCurrentInfoReplyBody(info, selector); err == nil || body != nil {
				t.Fatal("invalid owner returned sendable ACK")
			}
		})
	}
}
