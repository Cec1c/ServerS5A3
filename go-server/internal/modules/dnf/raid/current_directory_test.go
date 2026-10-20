package raid

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentRaidDirectory(t *testing.T) {
	entry := func(key uint32, count int, route byte) CurrentMetadata {
		members := currentCodecFixtureMembers(count)
		return CurrentMetadata{RaidKey: key, NameBytes: []byte("directory"), Route: route, Flags: [4]byte{1}, Leader: members[0], Members: members}
	}
	cases := []struct {
		name string
		rows []CurrentMetadata
	}{
		{"empty", nil},
		{"single", []CurrentMetadata{entry(0x170001, 1, 0)}},
		{"two", []CurrentMetadata{entry(0x170001, 2, 0), entry(0xf10002, 8, 1)}},
		{"twenty", []CurrentMetadata{entry(0x170001, 20, 0)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, err := BuildCurrentDirectoryBody(c.rows)
			if err != nil || binary.LittleEndian.Uint32(body) != uint32(len(c.rows)) {
				t.Fatalf("directory count: %x %v", body, err)
			}
			if len(c.rows) == 0 && len(body) != 4 {
				t.Fatal("empty directory has payload")
			}
			if dir := os.Getenv("DNF90_CURRENT_RAID_DIRECTORY_NATIVE_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, "directory-"+c.name+".bin"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for _, kind := range []string{"key", "empty", "too-many", "leader", "duplicate-member", "duplicate-raid"} {
		t.Run("invalid-"+kind, func(t *testing.T) {
			row := entry(0x170001, 2, 0)
			rows := []CurrentMetadata{row}
			switch kind {
			case "key":
				rows[0].RaidKey = 0
			case "empty":
				rows[0].Members = nil
			case "too-many":
				rows[0].Members = currentCodecFixtureMembers(21)
			case "leader":
				rows[0].Leader.Core.CharKey = 99
			case "duplicate-member":
				rows[0].Members[1] = rows[0].Members[0]
			case "duplicate-raid":
				rows = append(rows, row)
			}
			if body, err := BuildCurrentDirectoryBody(rows); err == nil || body != nil {
				t.Fatalf("invalid directory accepted: %x", body)
			}
		})
	}
}
