package raid

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCurrentRaidProfilesNativePackedRecords(t *testing.T) {
	rows := []CurrentProfileRecord{{CharKey: 31, Channel: 23, Server: 0, Level: 90, Job: 0, Grow: 0x21, Name: "q7a"}, {CharKey: 32, Channel: 23, Server: 0, Level: 90, Job: 0, Grow: 0x21, Name: "7331"}}
	body, err := BuildCurrentRaidProfilesBody(rows)
	if err != nil || len(body) != 118 || binary.LittleEndian.Uint32(body) != 2 {
		t.Fatalf("profile body: len=%d err=%v", len(body), err)
	}
	for i, row := range rows {
		packed := body[4+57*i : 4+57*(i+1)]
		if binary.LittleEndian.Uint16(packed) != row.CharKey || packed[3] != row.Channel || binary.LittleEndian.Uint16(packed[4:]) != row.Level || !reflect.DeepEqual(packed[6:9], []byte{row.Grow, row.Job, row.Server}) {
			t.Fatalf("wrong native field positions at row %d", i)
		}
		if string(packed[10:10+len(row.Name)]) != row.Name || packed[10+len(row.Name)] != 0 || packed[2] != 0 || packed[9] != 0 || !reflect.DeepEqual(packed[40:], make([]byte, 17)) {
			t.Fatalf("wrong terminated name/reserved projection at row %d", i)
		}
	}
	if dir := os.Getenv("DNF90_CURRENT_RAID_PROFILE_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "raid-profiles-752.bin"), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCurrentRaidProfilesRejectInvalidRecords(t *testing.T) {
	valid := CurrentProfileRecord{CharKey: 31, Channel: 23, Level: 90, Name: "角色"}
	for _, mutate := range []func(*CurrentProfileRecord){func(r *CurrentProfileRecord) { r.CharKey = 0 }, func(r *CurrentProfileRecord) { r.Channel = 0 }, func(r *CurrentProfileRecord) { r.Level = 0 }, func(r *CurrentProfileRecord) { r.Name = "" }, func(r *CurrentProfileRecord) { r.Name = "a\x00b" }, func(r *CurrentProfileRecord) { r.Name = strings.Repeat("角", 15) }} {
		bad := valid
		mutate(&bad)
		if _, err := BuildCurrentRaidProfilesBody([]CurrentProfileRecord{bad}); err == nil {
			t.Fatal("accepted invalid native profile")
		}
	}
	if _, err := BuildCurrentRaidProfilesBody([]CurrentProfileRecord{valid, valid}); err == nil {
		t.Fatal("accepted duplicate identity")
	}
	if _, err := BuildCurrentRaidProfilesBody(make([]CurrentProfileRecord, MaxAttackPartyMembers+1)); err == nil {
		t.Fatal("accepted excess count")
	}
}
