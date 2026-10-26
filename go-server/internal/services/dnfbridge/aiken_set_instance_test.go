package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func r193AikenSetStack() repo.ItemStack {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint32(raw[2:6], 100120561)
	binary.LittleEndian.PutUint32(raw[6:10], 554884773)
	raw[aikenSetRawStart+0] = 1
	raw[aikenSetRawStart+1] = 2
	raw[aikenSetRawStart+2] = 0x7e
	raw[aikenSetRawStart+3] = 5
	raw[aikenSetRawStart+4] = 3
	return repo.ItemStack{
		ItemID:   100120561,
		Count:    1,
		RawEntry: raw,
		Extra: map[string]string{
			"durability":               "32",
			"item_kind":                "equipment",
			aikenSetOptionsExtra:       "13,21",
			aikenSetRequiredPartsExtra: "3",
			aikenSetGradeExtra:         "2",
			aikenSetPartIndexExtra:     "5",
			aikenSetRecordExtra:        "5",
		},
	}
}

func r193CloneAikenSetStack(s repo.ItemStack) repo.ItemStack {
	return repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"x": s}}).Slots["x"]
}

func TestCurrentAikenReadSetRecoversPersistedRecord(t *testing.T) {
	s := r193AikenSetStack()
	got, found, err := currentAikenReadSet(s)
	if err != nil || !found {
		t.Fatalf("read set: found=%v err=%v", found, err)
	}
	want := aiken.Record{
		Record0:  5,
		Record1:  3,
		Slots:    [5]uint8{13, 21, 0, 0, 0},
		Record18: 2,
	}
	if got != want {
		t.Fatalf("record=%+v want=%+v", got, want)
	}
	// raw +0x76 is the member's individual grade and must not be confused
	// with the set result grade held in custom_ability_set_grade.
	if s.RawEntry[aikenSetRecordOffset+1] != 3 {
		t.Fatalf("read mutated member grade: %x", s.RawEntry[aikenSetRawStart:aikenSetRawEnd])
	}
}

func TestCurrentAikenReadSetOrdinaryItemIsNotFound(t *testing.T) {
	zeroRaw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint32(zeroRaw[2:6], 100120561)
	for _, tc := range []struct {
		name  string
		stack repo.ItemStack
	}{
		{name: "empty", stack: repo.ItemStack{}},
		{name: "zero_state", stack: repo.ItemStack{ItemID: 100120561, Count: 1, RawEntry: zeroRaw, Extra: map[string]string{"item_kind": "equipment"}}},
		{name: "short_row", stack: repo.ItemStack{ItemID: 100120561, Count: 1, RawEntry: []byte{1, 2, 3}}},
		{name: "legacy_zero_record", stack: repo.ItemStack{ItemID: 100120561, Count: 1, RawEntry: append([]byte(nil), zeroRaw...), Extra: map[string]string{aikenSetRecordExtra: "0"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, found, err := currentAikenReadSet(tc.stack); err != nil || found {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
}

func TestCurrentAikenReadSetRejectsBareOrDamagedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(repo.ItemStack) repo.ItemStack
	}{
		{name: "bare_record", change: func(s repo.ItemStack) repo.ItemStack {
			for _, key := range aikenSetExtraKeys {
				delete(s.Extra, key)
			}
			return s
		}},
		{name: "missing_tail", change: func(s repo.ItemStack) repo.ItemStack { s.RawEntry = nil; return s }},
		{name: "missing_required_parts", change: func(s repo.ItemStack) repo.ItemStack { delete(s.Extra, aikenSetRequiredPartsExtra); return s }},
		{name: "missing_record_field", change: func(s repo.ItemStack) repo.ItemStack { delete(s.Extra, aikenSetRecordExtra); return s }},
		{name: "zero_option", change: func(s repo.ItemStack) repo.ItemStack { s.Extra[aikenSetOptionsExtra] = "13,0"; return s }},
		{name: "duplicate_option", change: func(s repo.ItemStack) repo.ItemStack { s.Extra[aikenSetOptionsExtra] = "13,13"; return s }},
		{name: "too_many_options", change: func(s repo.ItemStack) repo.ItemStack { s.Extra[aikenSetOptionsExtra] = "1,2,3,4,5"; return s }},
		{name: "bad_grade", change: func(s repo.ItemStack) repo.ItemStack { s.Extra[aikenSetGradeExtra] = "4"; return s }},
		{name: "too_few_parts", change: func(s repo.ItemStack) repo.ItemStack { s.Extra[aikenSetRequiredPartsExtra] = "2"; return s }},
		{name: "too_many_parts", change: func(s repo.ItemStack) repo.ItemStack { s.Extra[aikenSetRequiredPartsExtra] = "7"; return s }},
		{name: "part_index_mismatch", change: func(s repo.ItemStack) repo.ItemStack { s.Extra[aikenSetPartIndexExtra] = "6"; return s }},
		{name: "record_mismatch", change: func(s repo.ItemStack) repo.ItemStack { s.Extra[aikenSetRecordExtra] = "6"; return s }},
		{name: "zero_record_with_metadata", change: func(s repo.ItemStack) repo.ItemStack { s.RawEntry[aikenSetRecordOffset] = 0; return s }},
		{name: "tail_alias_conflict", change: func(s repo.ItemStack) repo.ItemStack {
			s.Extra["tail_data_72"] = hex.EncodeToString([]byte{1, 2, 0x7e, 6, 3})
			return s
		}},
		{name: "malformed_tail_alias", change: func(s repo.ItemStack) repo.ItemStack { s.Extra["tail72"] = "zz"; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.change(r193AikenSetStack())
			if _, found, err := currentAikenReadSet(s); found || !errors.Is(err, aiken.ErrInvalid) {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
}

func TestCurrentAikenWriteSetPersistsMetadataAndPreservesMemberState(t *testing.T) {
	s := r193AikenSetStack()
	want := aiken.Record{
		Selector: 0xdeadbeef,
		Record0:  7,
		Record1:  4,
		Slots:    [5]uint8{13, 21, 0, 0, 0},
		Record18: 2,
	}
	after, err := currentAikenWriteSet(s, 9, want)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.RawEntry[aikenSetRawStart:aikenSetRawEnd]; !bytes.Equal(got, []byte{1, 2, 0x7e, 7, 3}) {
		t.Fatalf("tail=%x", got)
	}
	for key, value := range map[string]string{
		aikenSetOptionsExtra:       "13,21",
		aikenSetRequiredPartsExtra: "4",
		aikenSetGradeExtra:         "2",
		aikenSetPartIndexExtra:     "7",
		aikenSetRecordExtra:        "7",
	} {
		if got := after.Extra[key]; got != value {
			t.Fatalf("%s=%q want=%q", key, got, value)
		}
	}
	if after.Extra["durability"] != "32" || after.Extra["item_kind"] != "equipment" {
		t.Fatalf("unrelated metadata changed: %v", after.Extra)
	}
	got, found, err := currentAikenReadSet(after)
	if err != nil || !found {
		t.Fatalf("read back: found=%v err=%v", found, err)
	}
	want.Selector = 0
	if got != want {
		t.Fatalf("read back=%+v want=%+v", got, want)
	}
}

func TestCurrentAikenWriteSetSynchronizesAllCarriers(t *testing.T) {
	s := r193AikenSetStack()
	for _, key := range aikenRawAliases {
		s.Extra[key] = hex.EncodeToString(s.RawEntry)
	}
	for _, key := range aikenTailAliases {
		s.Extra[key] = hex.EncodeToString(s.RawEntry[aikenSetRawStart:aikenSetRawEnd])
	}
	after, err := currentAikenWriteSet(s, 9, aiken.Record{
		Record0: 8, Record1: 3, Slots: [5]uint8{7, 0, 0, 0, 0}, Record18: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTail := []byte{1, 2, 0x7e, 8, 3}
	if !bytes.Equal(after.RawEntry[aikenSetRawStart:aikenSetRawEnd], wantTail) {
		t.Fatalf("durable tail=%x", after.RawEntry[aikenSetRawStart:aikenSetRawEnd])
	}
	for _, key := range append(append([]string(nil), aikenTailAliases...), "mode1_raw_e_hex") {
		if after.Extra[key] != hex.EncodeToString(wantTail) {
			t.Fatalf("%s=%q want=%x", key, after.Extra[key], wantTail)
		}
	}
	for _, key := range aikenRawAliases {
		raw, decodeErr := aikenExtraBytes(after.Extra[key])
		if decodeErr != nil || len(raw) != currentItemListEntryWireSize || !bytes.Equal(raw[aikenSetRawStart:aikenSetRawEnd], wantTail) {
			t.Fatalf("%s len=%d err=%v", key, len(raw), decodeErr)
		}
	}
}

func TestCurrentAikenWriteSetZeroClearsOnlySetMetadata(t *testing.T) {
	s := r193AikenSetStack()
	for _, key := range aikenRawAliases {
		s.Extra[key] = hex.EncodeToString(s.RawEntry)
	}
	for _, key := range aikenTailAliases {
		s.Extra[key] = hex.EncodeToString(s.RawEntry[aikenSetRawStart:aikenSetRawEnd])
	}
	after, err := currentAikenWriteSet(s, 9, aiken.Record{Selector: 0x12345678})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range aikenSetExtraKeys {
		if _, exists := after.Extra[key]; exists {
			t.Fatalf("stale set metadata %s=%q", key, after.Extra[key])
		}
	}
	if after.Extra["durability"] != "32" || after.Extra["item_kind"] != "equipment" {
		t.Fatalf("unrelated metadata changed: %v", after.Extra)
	}
	wantTail := []byte{1, 2, 0x7e, 0, 3}
	if !bytes.Equal(after.RawEntry[aikenSetRawStart:aikenSetRawEnd], wantTail) {
		t.Fatalf("durable tail=%x want=%x", after.RawEntry[aikenSetRawStart:aikenSetRawEnd], wantTail)
	}
	for _, key := range aikenTailAliases {
		if after.Extra[key] != hex.EncodeToString(wantTail) {
			t.Fatalf("%s=%q want=%x", key, after.Extra[key], wantTail)
		}
	}
	if _, found, readErr := currentAikenReadSet(after); found || readErr != nil {
		t.Fatalf("cleared read: found=%v err=%v", found, readErr)
	}
}

func TestCurrentAikenWriteSetRejectsInvalidRecordsAtomically(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record aiken.Record
	}{
		{name: "zero_with_parts", record: aiken.Record{Record1: 3}},
		{name: "zero_with_options", record: aiken.Record{Slots: [5]uint8{13}}},
		{name: "too_few_parts", record: aiken.Record{Record0: 5, Record1: 2, Slots: [5]uint8{13}}},
		{name: "too_many_parts", record: aiken.Record{Record0: 5, Record1: 7, Slots: [5]uint8{13}}},
		{name: "zero_option", record: aiken.Record{Record0: 5, Record1: 3}},
		{name: "duplicate_option", record: aiken.Record{Record0: 5, Record1: 3, Slots: [5]uint8{13, 13}}},
		{name: "too_many_options", record: aiken.Record{Record0: 5, Record1: 3, Slots: [5]uint8{1, 2, 3, 4, 5}}},
		{name: "bad_grade", record: aiken.Record{Record0: 5, Record1: 3, Slots: [5]uint8{13}, Record18: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := r193AikenSetStack()
			before := r193CloneAikenSetStack(s)
			if _, err := currentAikenWriteSet(s, 9, tc.record); !errors.Is(err, aiken.ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
			if !reflect.DeepEqual(s, before) {
				t.Fatalf("rejected write mutated input: before=%+v after=%+v", before, s)
			}
		})
	}
}
