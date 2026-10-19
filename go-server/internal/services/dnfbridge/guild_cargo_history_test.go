package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func cargoHistoryFixture() guild.CargoHistoryEntry {
	return guild.CargoHistoryEntry{At: time.Unix(1700000000, 0), CharacterID: "1", CharacterName: "会员", Kind: guild.CargoHistoryDeposit, Item: r.ItemStack{ItemID: 123, Count: 3}}
}

func TestGuildCargoHistoryWireFieldsAndMagicSealTranspose(t *testing.T) {
	e := cargoHistoryFixture()
	e.Item.RawEntry = make([]byte, 119)
	for i := 0x48; i <= 0x56; i++ {
		e.Item.RawEntry[i] = byte(i)
	}
	before := append([]byte(nil), e.Item.RawEntry...)
	body, err := buildCurrentGuildCargoHistoryBody([]guild.CargoHistoryEntry{e})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 61 || binary.LittleEndian.Uint32(body) != 1 {
		t.Fatalf("length/count: %x", body)
	}
	row := body[4:]
	if binary.LittleEndian.Uint32(row) != 1700000000 || row[4] != 1 || !bytes.Equal(row[5:9], []byte{0xbb, 0xe1, 0xd4, 0xb1}) || !bytes.Equal(row[9:35], make([]byte, 26)) || binary.LittleEndian.Uint32(row[35:]) != 123 || binary.LittleEndian.Uint32(row[39:]) != 3 {
		t.Fatalf("fields: %x", row)
	}
	want := []byte{0x48, 0x4b, 0x4e, 0x49, 0x4c, 0x4f, 0x4a, 0x4d, 0x50, 0x51, 0x54, 0x55, 0x56, 0x53}
	if !bytes.Equal(row[43:], want) || !bytes.Equal(before, e.Item.RawEntry) {
		t.Fatalf("seal/alias: %x", row[43:])
	}
	e.Item.RawEntry[0x52] = 0xff
	e.Kind = guild.CargoHistoryWithdraw
	body, err = buildCurrentGuildCargoHistoryBody([]guild.CargoHistoryEntry{e})
	want[10], want[11], want[12] = 0, 0, 0
	if err != nil || body[8] != 2 || !bytes.Equal(body[47:], want) {
		t.Fatalf("extra option sentinel: %x %v", body, err)
	}
}

func TestGuildCargoHistoryWireRecentOrderAndMultibyteBoundary(t *testing.T) {
	var entries []guild.CargoHistoryEntry
	for i := 0; i < 53; i++ {
		e := cargoHistoryFixture()
		e.Item.ItemID = int64(i + 1)
		entries = append(entries, e)
	}
	entries[0].At = entries[0].At.Add(time.Second)
	entries[0].CharacterName = strings.Repeat("会", 14) + "A员"
	before := append([]guild.CargoHistoryEntry(nil), entries...)
	body, err := buildCurrentGuildCargoHistoryBody(entries)
	if err != nil || len(body) != 4+50*57 || binary.LittleEndian.Uint32(body) != 50 {
		t.Fatalf("%d %v", len(body), err)
	}
	if binary.LittleEndian.Uint32(body[4+35:]) != 1 || binary.LittleEndian.Uint32(body[4+57+35:]) != 53 || binary.LittleEndian.Uint32(body[4+49*57+35:]) != 5 {
		t.Fatal("descending timestamp/newest insertion ordering")
	}
	if !bytes.Equal(body[9:37], bytes.Repeat([]byte{0xbb, 0xe1}, 14)) || body[37] != 'A' || body[38] != 0 {
		t.Fatal("multibyte/NUL boundary")
	}
	if !reflect.DeepEqual(entries, before) {
		t.Fatal("input reordered")
	}
}

func TestGuildCargoHistoryWireEmptyAndInvalidHiddenRows(t *testing.T) {
	body, err := buildCurrentGuildCargoHistoryBody(nil)
	if err != nil || !bytes.Equal(body, []byte{0, 0, 0, 0}) {
		t.Fatal(body, err)
	}
	for _, name := range []string{"time", "kind", "name", "count", "item"} {
		t.Run(name, func(t *testing.T) {
			bad := cargoHistoryFixture()
			switch name {
			case "time":
				bad.At = time.Unix(math.MaxInt32+1, 0)
			case "kind":
				bad.Kind = 3
			case "name":
				bad.CharacterName = strings.Repeat("a", 40) + "\x00"
			case "count":
				bad.Item.Count = 0
			case "item":
				bad.Item.ItemID = math.MaxUint32 + 1
			}
			entries := []guild.CargoHistoryEntry{bad}
			for i := 0; i < 50; i++ {
				entries = append(entries, cargoHistoryFixture())
			}
			if got, err := buildCurrentGuildCargoHistoryBody(entries); err == nil || got != nil {
				t.Fatal("invalid row masked by truncation")
			}
		})
	}
}

func TestGuildCargoHistoryWireUsesOnlyCompleteInstanceRows(t *testing.T) {
	e := cargoHistoryFixture()
	e.Item.RawEntry = bytes.Repeat([]byte{0xff}, 43)
	body, err := buildCurrentGuildCargoHistoryBody([]guild.CargoHistoryEntry{e})
	if err != nil || !bytes.Equal(body[47:], make([]byte, 14)) {
		t.Fatal("legacy row interpreted as 119-byte row")
	}
	raw := make([]byte, 119)
	raw[0x48] = 7
	e.Item.Extra = map[string]string{"current_raw_entry_77": hex.EncodeToString(raw)}
	body, err = buildCurrentGuildCargoHistoryBody([]guild.CargoHistoryEntry{e})
	if err != nil || body[47] != 7 {
		t.Fatal("full persisted instance projection not reused")
	}
}
