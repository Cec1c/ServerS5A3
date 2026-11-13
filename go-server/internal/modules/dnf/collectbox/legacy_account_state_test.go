package collectbox

import (
	"reflect"
	"testing"
)

func TestParseLegacyAccountState(t *testing.T) {
	raw := `{"boxes":{"5":{"8":490007143,"0":490007148},"1":{"4":10007328}}}`
	got, err := ParseLegacyAccountState(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []LegacyRegistration{
		{BoxIndex: 1, SlotIndex: 4, ItemID: 10007328, EntryKey: RegisteredSlotKey(1, 4)},
		{BoxIndex: 5, SlotIndex: 0, ItemID: 490007148, EntryKey: RegisteredSlotKey(5, 0)},
		{BoxIndex: 5, SlotIndex: 8, ItemID: 490007143, EntryKey: RegisteredSlotKey(5, 8)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %+v want %+v", got, want)
	}
}

func TestParseLegacyAccountStateRejectsCorruptRows(t *testing.T) {
	cases := []string{
		`{"boxes":{"0":{"0":1}}}`,
		`{"boxes":{"5":{"256":1}}}`,
		`{"boxes":{"5":{"0":0}}}`,
		`{"boxes":{"5":{"bad":1}}}`,
		`{`,
	}
	for _, raw := range cases {
		if _, err := ParseLegacyAccountState(raw); err == nil {
			t.Fatalf("ParseLegacyAccountState(%q) succeeded, want error", raw)
		}
	}
}
