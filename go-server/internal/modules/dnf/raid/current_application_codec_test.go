package raid

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentRaidApplicationNativePackedOwner(t *testing.T) {
	row := CurrentApplicationRecord{CharKey: 32, Channel: 23, Level: 90, Job: 0, Grow: 33, Name: "7331", ClientValue: 0x12345678, NativeValue: 10}
	body, err := BuildCurrentRaidApplicationBody(row)
	if err != nil || len(body) != 51 || binary.LittleEndian.Uint16(body) != 32 || body[4] != 23 || body[5] != 0 || binary.LittleEndian.Uint32(body[6:]) != row.ClientValue || binary.LittleEndian.Uint16(body[10:]) != 10 || binary.LittleEndian.Uint16(body[16:]) != 90 || body[19] != 33 || string(body[20:24]) != "7331" || body[24] != 0 {
		t.Fatalf("body=%x err=%v", body, err)
	}
	if dir := os.Getenv("DNF90_CURRENT_RAID_APPLICATION_NATIVE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "raid-application-755.bin"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"id", "channel", "level", "name", "nul", "long"} {
		bad := row
		switch kind {
		case "id":
			bad.CharKey = 0
		case "channel":
			bad.Channel = 0
		case "level":
			bad.Level = 0
		case "name":
			bad.Name = ""
		case "nul":
			bad.Name = "a\x00b"
		case "long":
			bad.Name = "123456789012345678901234567890"
		}
		if _, err := BuildCurrentRaidApplicationBody(bad); err == nil {
			t.Fatalf("accepted %s", kind)
		}
	}
}
