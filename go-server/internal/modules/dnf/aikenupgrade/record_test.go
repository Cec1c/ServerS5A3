package aikenupgrade

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestDecodeRecordExactLayoutAndIdentity(t *testing.T) {
	body := []byte{
		0x44, 0x33, 0x22, 0x11,
		0xa5,
		0xfe,
		0x01, 0x00, 0xff, 0x00, 0x80,
		0x07,
	}
	got, err := DecodeRecord(body)
	if err != nil {
		t.Fatal(err)
	}
	want := Record{
		Selector: 0x11223344,
		Record0:  0xa5,
		Record1:  0xfe,
		Slots:    [5]uint8{0x01, 0x00, 0xff, 0x00, 0x80},
		Record18: 0x07,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded record = %#v, want %#v", got, want)
	}
	if encoded := got.Encode(); !bytes.Equal(encoded, body) {
		t.Fatalf("encoded record = %x, want %x", encoded, body)
	}
	if identity := got.Identity(); identity != (RecordIdentity{CharacterKey: 0x3344, SubKey: 0x00a5}) {
		t.Fatalf("identity = %#v", identity)
	}
	if options := got.Options(); !reflect.DeepEqual(options, []byte{0x01, 0xff, 0x80}) {
		t.Fatalf("options = %x", options)
	}
}

func TestDecodeRecordRejectsEveryNonSingleRecordLength(t *testing.T) {
	for n := 0; n <= 24; n++ {
		if n == recordWireSize {
			continue
		}
		_, err := DecodeRecord(make([]byte, n))
		if !errors.Is(err, ErrRecord) {
			t.Fatalf("length %d: error = %v, want ErrRecord", n, err)
		}
	}
}

func TestRecordWireExtremesRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		identity RecordIdentity
		options  []byte
	}{
		{
			name:     "zero values and empty options",
			body:     make([]byte, recordWireSize),
			identity: RecordIdentity{},
			options:  []byte{},
		},
		{
			name:     "maximum field values",
			body:     bytes.Repeat([]byte{0xff}, recordWireSize),
			identity: RecordIdentity{CharacterKey: 0xffff, SubKey: 0x00ff},
			options:  []byte{0xff, 0xff, 0xff, 0xff, 0xff},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeRecord(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if identity := got.Identity(); identity != tc.identity {
				t.Fatalf("identity = %#v, want %#v", identity, tc.identity)
			}
			if options := got.Options(); !reflect.DeepEqual(options, tc.options) {
				t.Fatalf("options = %x, want %x", options, tc.options)
			}
			if encoded := got.Encode(); !bytes.Equal(encoded, tc.body) {
				t.Fatalf("encoded record = %x, want %x", encoded, tc.body)
			}
		})
	}
}

func TestRecordIdentityUsesCharacterKeyAndOpaqueSubKey(t *testing.T) {
	a := Record{Selector: 0xabcd0007, Record0: 0x09}
	b := Record{Selector: 0x12340007, Record0: 0x09}
	c := Record{Selector: 0xabcd0008, Record0: 0x09}
	d := Record{Selector: 0xabcd0007, Record0: 0x0a}

	if a.Identity() != b.Identity() {
		t.Fatalf("high selector bits changed cache identity: %#v != %#v", a.Identity(), b.Identity())
	}
	if a.Identity() == c.Identity() {
		t.Fatal("different low selector keys share an identity")
	}
	if a.Identity() == d.Identity() {
		t.Fatal("different subkeys share an identity")
	}

	decodedA, err := DecodeRecord(a.Encode())
	if err != nil {
		t.Fatal(err)
	}
	decodedB, err := DecodeRecord(b.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if decodedA.Selector != a.Selector || decodedB.Selector != b.Selector {
		t.Fatal("full selector was not preserved on the wire")
	}
}

func TestOptionsAndSlotsBoundaryProjection(t *testing.T) {
	tests := []struct {
		name    string
		slots   [5]uint8
		options []byte
	}{
		{name: "all zero", slots: [5]uint8{}, options: []byte{}},
		{name: "single final slot", slots: [5]uint8{0, 0, 0, 0, 0xff}, options: []byte{0xff}},
		{name: "zero positions removed", slots: [5]uint8{0, 7, 0, 9, 0}, options: []byte{7, 9}},
		{name: "wire order retained", slots: [5]uint8{5, 4, 3, 2, 1}, options: []byte{5, 4, 3, 2, 1}},
		{name: "duplicate values retained", slots: [5]uint8{5, 0, 5, 0, 0}, options: []byte{5, 5}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := Record{Slots: tc.slots}
			if options := record.Options(); !reflect.DeepEqual(options, tc.options) {
				t.Fatalf("Options() = %x, want %x", options, tc.options)
			}
			projected, err := SlotsFromOptions(tc.options)
			if err != nil {
				t.Fatal(err)
			}
			if options := (Record{Slots: projected}).Options(); !reflect.DeepEqual(options, tc.options) {
				t.Fatalf("projected options = %x, want %x", options, tc.options)
			}
		})
	}
}

func TestSlotsFromOptionsBoundaryAndPadding(t *testing.T) {
	empty, err := SlotsFromOptions(nil)
	if err != nil || empty != ([5]uint8{}) {
		t.Fatalf("nil options = %v, %v", empty, err)
	}
	full := []byte{1, 2, 3, 4, 5}
	got, err := SlotsFromOptions(full)
	if err != nil {
		t.Fatal(err)
	}
	if got != ([5]uint8{1, 2, 3, 4, 5}) {
		t.Fatalf("full options = %v", got)
	}
	short := []byte{1, 2, 3}
	got, err = SlotsFromOptions(short)
	if err != nil {
		t.Fatal(err)
	}
	if got != ([5]uint8{1, 2, 3, 0, 0}) {
		t.Fatalf("short options = %v", got)
	}
}

func TestSlotsFromOptionsRejectsUnrepresentableValues(t *testing.T) {
	tests := map[string][]byte{
		"zero at start":  {0, 1},
		"zero in middle": {1, 0, 2},
		"zero at end":    {1, 2, 0},
		"too many":       {1, 2, 3, 4, 5, 6},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := SlotsFromOptions(options); !errors.Is(err, ErrRecord) {
				t.Fatalf("SlotsFromOptions(%x) error = %v, want ErrRecord", options, err)
			}
		})
	}
}
