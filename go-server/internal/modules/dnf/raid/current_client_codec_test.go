package raid

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentCreateRaidRequestPreservesThreeUIFlags(t *testing.T) {
	// Actual live op664 shape: route 0, three-byte DSTR, flags 1/0/0.
	body := []byte{0, 3, 0, 0, 0, 'a', 'b', 'c', 1, 0, 0}
	got, err := DecodeCurrentCreateRaidRequest(body)
	if err != nil || got.Route != 0 || string(got.NameBytes) != "abc" || got.UIFlags != [3]byte{1, 0, 0} {
		t.Fatalf("current request = %+v, error %v", got, err)
	}
	body[5] = 'z'
	if string(got.NameBytes) != "abc" {
		t.Fatal("request aliases transport buffer")
	}
	for _, tail := range []int{0, 1, 2, 4} {
		bad := append([]byte{0, 3, 0, 0, 0, 'a', 'b', 'c'}, make([]byte, tail)...)
		if _, err := DecodeCurrentCreateRaidRequest(bad); err == nil {
			t.Fatalf("accepted tail length %d", tail)
		}
	}
	for _, n := range []uint32{4, 0xffffffff} {
		bad := append([]byte(nil), body...)
		binary.LittleEndian.PutUint32(bad[1:5], n)
		if _, err := DecodeCurrentCreateRaidRequest(bad); err == nil {
			t.Fatalf("accepted name length %d", n)
		}
	}
	// Do not relax the previous profile's decoder while the bridge is unlinked.
	if _, err := DecodeCreateRaidRequest(body); err == nil {
		t.Fatal("legacy decoder accepted current layout")
	}
}

func TestCurrentModifyRaidInfoRequestHasIndependentTwoByteTail(t *testing.T) {
	body := []byte{1, 3, 0, 0, 0, 'a', 'b', 'c', 1, 0}
	got, err := DecodeCurrentModifyRaidInfoRequest(body)
	if err != nil || got.Route != 1 || string(got.NameBytes) != "abc" || got.UIFlags != [2]byte{1, 0} {
		t.Fatalf("current modify = %+v, error %v", got, err)
	}
	body[5] = 'z'
	if string(got.NameBytes) != "abc" {
		t.Fatal("modify request aliases transport buffer")
	}
	for _, tail := range []int{0, 1, 3, 4} {
		bad := append([]byte{1, 3, 0, 0, 0, 'a', 'b', 'c'}, make([]byte, tail)...)
		if _, err := DecodeCurrentModifyRaidInfoRequest(bad); err == nil {
			t.Fatalf("modify accepted tail length %d", tail)
		}
	}
	for _, n := range []uint32{4, 0xffffffff} {
		bad := append([]byte(nil), body...)
		binary.LittleEndian.PutUint32(bad[1:5], n)
		if _, err := DecodeCurrentModifyRaidInfoRequest(bad); err == nil {
			t.Fatalf("modify accepted name length %d", n)
		}
	}
	if _, err := DecodeCurrentCreateRaidRequest(body); err == nil {
		t.Fatal("create accepted two-byte modify tail")
	}
	if _, err := DecodeModifyRaidInfoRequest(body); err == nil {
		t.Fatal("legacy modify accepted current layout")
	}
	empty := []byte{0, 0, 0, 0, 0, 0, 1}
	if req, err := DecodeCurrentModifyRaidInfoRequest(empty); err != nil || len(req.NameBytes) != 0 || req.UIFlags != [2]byte{0, 1} {
		t.Fatal("wire codec rejected a bounded empty DSTR")
	}
}

func currentCodecFixtureMembers(n int) []CurrentMemberRecord {
	members := make([]CurrentMemberRecord, n)
	for i := range members {
		members[i] = CurrentMemberRecord{Core: MemberRecord{CharKey: uint16(i + 1), Name: "fixture", Field40: byte(i % 5), Field44: byte(i % 3), GroupIndex: byte(i/4 + 1), SlotOrder: byte(i % 4), Field48: 90, Field66BoolSource: boolFixture(i == 0)}, ExtraByte: byte(i % 2), ExtraName: "fixture-extra"}
	}
	return members
}

func boolFixture(v bool) uint32 {
	if v {
		return 1
	}
	return 0
}

func TestCurrentRaidCodecRejectsInvalidOwnersAndNativeBufferOverflow(t *testing.T) {
	members := currentCodecFixtureMembers(2)
	for _, members := range [][]CurrentMemberRecord{
		{{Core: MemberRecord{CharKey: 0}}},
		{members[0], members[0]},
		currentCodecFixtureMembers(21),
		{{Core: members[0].Core, ExtraName: strings.Repeat("界", 17)}},
		{{Core: members[0].Core, ExtraName: strings.Repeat("😀", 9)}},
		{{Core: members[0].Core, ExtraName: "a\x00b"}},
	} {
		if body, err := BuildCurrentMemberRefreshMode3Body(0x170001, members); err == nil || body != nil {
			t.Fatal("invalid row/owner returned sendable body")
		}
	}
	if body, err := BuildCurrentMemberRefreshMode3Body(0, members); err == nil || body != nil {
		t.Fatal("zero raid owner accepted")
	}
	info := CurrentMetadata{RaidKey: 0x170001, Leader: members[0], Members: members[1:]}
	if body, err := BuildCurrentMetadataBody(info, 0); err == nil || body != nil {
		t.Fatal("absent leader accepted")
	}
	for _, mode := range []uint32{1, 3, 4} {
		if body, err := BuildCurrentMetadataBody(info, mode); err == nil || body != nil {
			t.Fatal("invalid mode accepted")
		}
	}
	// Boundary is wide code units, not GB18030 byte length.
	for _, s := range []string{strings.Repeat("界", 16), strings.Repeat("😀", 8)} {
		row := members[0]
		row.ExtraName = s
		if _, err := BuildCurrentMemberRefreshMode3Body(0x170001, []CurrentMemberRecord{row}); err != nil {
			t.Fatalf("valid 16-unit extra name rejected: %v", err)
		}
	}
}

func TestCurrentRaidCodecNativeFixtures(t *testing.T) {
	type fixture struct {
		Name    string  `json:"name"`
		Mode    uint32  `json:"mode"`
		Members int     `json:"members"`
		Flags   [4]byte `json:"flags"`
	}
	fixtures := []fixture{{"metadata-mode0-one", 0, 1, [4]byte{1, 0, 0, 0}}, {"metadata-mode0-two", 0, 2, [4]byte{0, 1, 2, 3}}, {"metadata-mode0-twenty", 0, 20, [4]byte{3, 2, 1, 0}}, {"metadata-mode2", 2, 1, [4]byte{5, 6, 7, 8}}, {"roster-mode3-empty", 3, 0, [4]byte{}}, {"roster-mode3-two", 3, 2, [4]byte{}}, {"roster-mode3-twenty", 3, 20, [4]byte{}}}
	dir := os.Getenv("DNF90_CURRENT_RAID_FIXTURES")
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			members := currentCodecFixtureMembers(f.Members)
			var body []byte
			var err error
			if f.Mode == 3 {
				body, err = BuildCurrentMemberRefreshMode3Body(0x170001, members)
			} else {
				body, err = BuildCurrentMetadataBody(CurrentMetadata{RaidKey: 0x170001, NameBytes: []byte("abc"), Route: 1, State: 0, Phase: 0, Field34: 0, Flags: f.Flags, Leader: members[0], Members: members}, f.Mode)
			}
			if err != nil {
				t.Fatal(err)
			}
			if binary.LittleEndian.Uint32(body[:4]) != 0x170001 || binary.LittleEndian.Uint32(body[4:8]) != f.Mode {
				t.Fatal("wrong packet owner or mode")
			}
			if dir != "" {
				if err := os.WriteFile(filepath.Join(dir, f.Name+".bin"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if dir != "" {
		data, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fixtures.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
