package pet

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func encodeSyntheticCreatureName(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(value))
	if err != nil {
		t.Fatalf("encode synthetic creature name: %v", err)
	}
	return encoded
}

func TestCreatureNameForWireKeepsLongestCompleteUTF16Prefix(t *testing.T) {
	chinese := strings.Repeat("界", CurrentCreatureNameUTF16Limit+1)
	supplementary := strings.Repeat("🙂", CurrentCreatureNameUTF16Limit/2) + "尾"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "12 ascii", in: "abcdefghijkl", want: "abcdefghijkl"},
		{name: "13 ascii", in: "abcdefghijklm", want: "abcdefghijkl"},
		{name: "12 chinese", in: strings.Repeat("界", 12), want: strings.Repeat("界", 12)},
		{name: "13 chinese", in: chinese, want: strings.Repeat("界", 12)},
		{name: "supplementary rune boundary", in: supplementary, want: strings.Repeat("🙂", 6)},
		{name: "NUL terminator", in: "abc\x00ignored", want: "abc"},
		{name: "invalid UTF-8", in: "ab\xffcd", want: "ab"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := CreatureNameForWire(test.in)
			if got != test.want {
				t.Fatalf("CreatureNameForWire()=%q want=%q", got, test.want)
			}
			if units := len(utf16.Encode([]rune(got))); units > CurrentCreatureNameUTF16Limit {
				t.Fatalf("wire name has %d UTF-16 units", units)
			}
		})
	}
}

func TestDefaultCreatureNameForWireOnlyRemovesSpacesWhenOverLimit(t *testing.T) {
	if got, want := DefaultCreatureNameForWire("合成 名"), "合成 名"; got != want {
		t.Fatalf("short default name=%q want=%q", got, want)
	}
	if got, want := DefaultCreatureNameForWire("合成宠物名称 (测 1)-尾"), "合成宠物名称(测1)-尾"; got != want {
		t.Fatalf("long default name=%q want=%q", got, want)
	}
	if got, want := DefaultCreatureNameForWire(strings.Repeat("a", 10)+" bc"), strings.Repeat("a", 10)+"bc"; got != want {
		t.Fatalf("space projection=%q want=%q", got, want)
	}
}

func TestCreatureNameRawForWirePreservesShortCanonicalBytes(t *testing.T) {
	raw := encodeSyntheticCreatureName(t, "合成")
	before := append([]byte(nil), raw...)
	got := CreatureNameRawForWire(raw)
	if !bytes.Equal(got, raw) {
		t.Fatalf("short raw=% X want=% X", got, raw)
	}
	got[0] ^= 0xff
	if !bytes.Equal(raw, before) {
		t.Fatalf("projected result aliases input: got=% X want=% X", raw, before)
	}

	empty := CreatureNameRawForWire([]byte{})
	if empty == nil || len(empty) != 0 {
		t.Fatalf("canonical empty raw=% X nil=%t", empty, empty == nil)
	}
	nulTerminated := CreatureNameRawForWire(append(encodeSyntheticCreatureName(t, "合成"), 0, 'x'))
	if want := encodeSyntheticCreatureName(t, "合成"); !bytes.Equal(nulTerminated, want) {
		t.Fatalf("NUL-terminated raw=% X want=% X", nulTerminated, want)
	}
}

func TestCreatureNameRawForWireProjectsLongChineseAndSupplementaryRunes(t *testing.T) {
	chinese := strings.Repeat("界", 13)
	if got, want := CreatureNameRawForWire(encodeSyntheticCreatureName(t, chinese)), encodeSyntheticCreatureName(t, strings.Repeat("界", 12)); !bytes.Equal(got, want) {
		t.Fatalf("long Chinese raw=% X want=% X", got, want)
	}
	supplementary := strings.Repeat("🙂", 7)
	rawSupplementary := encodeSyntheticCreatureName(t, supplementary)
	if len(rawSupplementary) != 28 {
		t.Fatalf("synthetic four-byte GB18030 name length=%d", len(rawSupplementary))
	}
	if got, want := CreatureNameRawForWire(rawSupplementary), encodeSyntheticCreatureName(t, strings.Repeat("🙂", 6)); !bytes.Equal(got, want) {
		t.Fatalf("long supplementary raw=% X want=% X", got, want)
	}
}

func TestCreatureNameRawForWireRejectsInvalidAndTruncatedGB18030(t *testing.T) {
	fourByte := encodeSyntheticCreatureName(t, "🙂")
	invalid := [][]byte{
		{0xff},
		{0x81},
		fourByte[:len(fourByte)-1],
	}
	for _, raw := range invalid {
		if got := CreatureNameRawForWire(raw); got != nil {
			t.Fatalf("invalid raw=% X projected=% X, want nil", raw, got)
		}
	}
}

func TestCreatureNameForWireBoundsFourByteBMPEncoding(t *testing.T) {
	// U+0080 uses one UTF-16 unit but four GB18030 bytes.
	name := strings.Repeat("\u0080", 12)
	want := strings.Repeat("\u0080", 6)
	if got := CreatureNameForWire(name); got != want {
		t.Fatalf("four-byte BMP prefix=%q want=%q", got, want)
	}
	raw := encodeSyntheticCreatureName(t, name)
	if len(raw) != 48 {
		t.Fatalf("synthetic BMP fixture bytes=%d", len(raw))
	}
	if got := CreatureNameRawForWire(raw); !bytes.Equal(got, encodeSyntheticCreatureName(t, want)) {
		t.Fatalf("four-byte BMP wire=% X", got)
	}
}

func TestValidatedCreatureEntriesProjectsLegacyNamesBeforeLogin(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "fallback", raw: []byte(strings.Repeat("a", 31)), want: strings.Repeat("a", 12)},
		{name: strings.Repeat("界", 13), want: strings.Repeat("界", 12)},
		{name: "fallback", raw: []byte{0x81}, want: ""},
	} {
		record := repository.PetRecord{Entries: map[string]repository.PetEntry{
			"7": {PetKey: "7", CreatureKey: 7, ItemID: 63000, Level: 1, Name: test.name, NameRaw: test.raw},
		}}
		entries, err := validatedCreatureEntries(record)
		if err != nil || len(entries) != 1 {
			t.Fatalf("legacy login projection entries=%+v err=%v", entries, err)
		}
		if !bytes.Equal(entries[0].NameRaw, encodeSyntheticCreatureName(t, test.want)) {
			t.Fatalf("login name=% X want=%q", entries[0].NameRaw, test.want)
		}
		if _, err := BuildCreatureListBody(entries); err != nil {
			t.Fatalf("projected login body: %v", err)
		}
		if saved := record.Entries["7"]; saved.Name != test.name || !bytes.Equal(saved.NameRaw, test.raw) {
			t.Fatal("login projection changed durable record")
		}
	}
}

func TestValidateCreatureRenameNameWireEnforcesCanonicalUTF16Bound(t *testing.T) {
	if err := validateCreatureRenameNameWire([]byte("abcdefghijkl")); err != nil {
		t.Fatalf("12 ASCII units rejected: %v", err)
	}
	if err := validateCreatureRenameNameWire([]byte("abcdefghijklm")); err == nil {
		t.Fatal("13 ASCII units accepted")
	}
	if err := validateCreatureRenameNameWire([]byte{0x81}); err == nil {
		t.Fatal("invalid GB18030 accepted")
	}
}

func TestBuildCreatureListBodyProjectsPersistedNamesWithoutMutation(t *testing.T) {
	longRaw := []byte(strings.Repeat("a", 31))
	before := append([]byte(nil), longRaw...)
	body, err := buildCreatureListBody([]repository.PetEntry{{
		PetKey:      "9",
		CreatureKey: 9,
		ItemID:      63000,
		Satiety:     7,
		Level:       3,
		NameRaw:     longRaw,
		TailFlag:    1,
	}})
	if err != nil {
		t.Fatalf("project long persisted name: %v", err)
	}
	if got := binary.LittleEndian.Uint32(body[12:16]); got != 12 {
		t.Fatalf("projected name length=%d body=% X", got, body)
	}
	if !bytes.Equal(body[16:28], []byte(strings.Repeat("a", 12))) || body[28] != 1 {
		t.Fatalf("projected long body=% X", body)
	}
	if !bytes.Equal(longRaw, before) {
		t.Fatalf("durable input mutated: got=% X want=% X", longRaw, before)
	}

	invalidRaw := []byte{0xff}
	body, err = buildCreatureListBody([]repository.PetEntry{{
		PetKey:      "10",
		CreatureKey: 10,
		ItemID:      63000,
		Satiety:     8,
		Level:       4,
		NameRaw:     invalidRaw,
		TailFlag:    1,
	}})
	if err != nil {
		t.Fatalf("malformed persisted name rejected whole list: %v", err)
	}
	if got := binary.LittleEndian.Uint32(body[12:16]); got != 0 || body[16] != 1 {
		t.Fatalf("malformed name did not degrade to empty: body=% X", body)
	}
	if !bytes.Equal(invalidRaw, []byte{0xff}) {
		t.Fatalf("malformed durable input mutated: % X", invalidRaw)
	}
}

func TestOwnerRenameRejectsInvalidWireNameBeforeCardConsumption(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "too many ASCII units", raw: []byte("abcdefghijklm")},
		{name: "invalid GB18030", raw: []byte{0x81}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			seedRenameCreature(t, ctx, repos, "77")
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatalf("NewOwner: %v", err)
			}
			_, err = owner.Rename(ctx, RenameCommand{
				SelectedCharacterID: 77,
				ListType:            listTypePet,
				SlotIndex:           5,
				NameRaw:             test.raw,
			})
			if !errors.Is(err, ErrPetRenameNameInvalid) {
				t.Fatalf("Rename error=%v want=%v", err, ErrPetRenameNameInvalid)
			}
			inventory, found, loadErr := repos.Inventory.Load(ctx, "77")
			if loadErr != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, loadErr)
			}
			if card := inventory.Slots["0:5"]; card.Count != 2 {
				t.Fatalf("failed rename consumed card: %+v", card)
			}
			record, found, loadErr := repos.Pet.Load(ctx, "77")
			if loadErr != nil || !found {
				t.Fatalf("load pet found=%t err=%v", found, loadErr)
			}
			entry := record.Entries["37"]
			if entry.Name != "old" || !bytes.Equal(entry.NameRaw, []byte("old")) {
				t.Fatalf("failed rename mutated durable entry: %+v", entry)
			}
		})
	}
}
