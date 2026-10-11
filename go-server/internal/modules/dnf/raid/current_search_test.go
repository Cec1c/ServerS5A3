package raid

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentSearchRequestNativeFieldsAndStrictFraming(t *testing.T) {
	// Real UI default sort is SortMemberNum=5, not raid route or channel.
	body := []byte{0, 33, 0, 0, 0, 5, 1, 1, 1, 2, 0, 0, 0, 0xb9, 0xa5}
	r, err := DecodeCurrentSearchRequest(body)
	if err != nil || r.Offset != 33 || r.SortColumn != 5 || !r.Reverse || !r.OnlyMyChannel || !r.ExcludeProceeding || !bytes.Equal(r.NameBytes, []byte{0xb9, 0xa5}) {
		t.Fatal("native search fields lost", r, err)
	}
	body[13] = 0
	if r.NameBytes[0] != 0xb9 {
		t.Fatal("search name retains caller buffer")
	}
	if r, err := DecodeCurrentSearchRequest([]byte{1}); err != nil || r.Mode != 1 {
		t.Fatal("native catalog selector rejected")
	}
	for _, bad := range [][]byte{nil, {0}, {1, 0}, {2}, {255}, body[:12], append(append([]byte(nil), body...), 0)} {
		if _, err := DecodeCurrentSearchRequest(bad); err == nil {
			t.Fatalf("malformed search accepted: %x", bad)
		}
	}
	for _, at := range []int{5, 6, 7, 8, 9} {
		bad := append([]byte(nil), body...)
		bad[at] = 255
		if _, err := DecodeCurrentSearchRequest(bad); err == nil {
			t.Fatalf("invalid field accepted: %d", at)
		}
	}
}

func TestCurrentSearchPagesForActualNative753(t *testing.T) {
	row := func(key uint32, count int, route byte) CurrentMetadata {
		members := currentCodecFixtureMembers(count)
		return CurrentMetadata{RaidKey: key, NameBytes: []byte("directory"), Route: route, State: 1, Flags: [4]byte{1}, Leader: members[0], Members: members}
	}
	cases := []struct {
		name          string
		total, offset uint32
		rows          []CurrentMetadata
	}{
		{"empty", 0, 0, nil},
		{"single", 1, 0, []CurrentMetadata{row(0x17010001, 1, 0)}},
		{"two", 35, 33, []CurrentMetadata{row(0x17010001, 2, 0), row(0x20010002, 8, 1)}},
		{"twenty", 1, 0, []CurrentMetadata{row(0x17010001, 20, 0)}},
		{"last-empty", 35, 35, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, err := BuildCurrentSearchPageBody(c.total, c.offset, c.rows)
			if err != nil || len(body) < 12 || binary.LittleEndian.Uint32(body) != c.total || binary.LittleEndian.Uint32(body[4:]) != c.offset || binary.LittleEndian.Uint32(body[8:]) != uint32(len(c.rows)) {
				t.Fatal("search page framing", body, err)
			}
			if dir := os.Getenv("DNF90_CURRENT_RAID_SEARCH_NATIVE_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, "search-"+c.name+".bin"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for _, pair := range [][2]uint32{{0, 1}, {1, 1}, {0xffffffff, 0xffffffff}} {
		if _, err := BuildCurrentSearchPageBody(pair[0], pair[1], []CurrentMetadata{row(0x17010001, 1, 0)}); err == nil {
			t.Fatal("page exceeds total or overflows", pair)
		}
	}
	rows := make([]CurrentMetadata, 34)
	for i := range rows {
		rows[i] = row(0x17010001+uint32(i), 1, 0)
	}
	if _, err := BuildCurrentSearchPageBody(34, 0, rows); err == nil {
		t.Fatal("native UI page size exceeded")
	}
	if _, err := BuildCurrentSearchPageBody(2, 0, []CurrentMetadata{rows[0], rows[0]}); err == nil {
		t.Fatal("duplicate directory owner accepted")
	}
}
