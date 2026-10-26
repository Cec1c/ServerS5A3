package aikenupgrade

import (
	"errors"
	"reflect"
	"testing"
)

func TestDecodeSetRequestCurrentWireShape(t *testing.T) {
	body := []byte{
		3, 1, 13, 252,
		2, 0x09, 0x00, 0x6d, 0x01,
	}

	got, err := DecodeSetRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want := SetRequest{
		Options: []byte{1, 13, 252},
		Targets: []uint16{9, 365},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded request = %#v, want %#v", got, want)
	}
}

func TestDecodeSetRequestPreservesIndependentArrays(t *testing.T) {
	// The values and repetition are intentionally preserved. This decoder does
	// not invent an option-to-target pairing or a uniqueness rule that the
	// current EXE frame does not carry.
	body := []byte{2, 7, 7, 3, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00}

	got, err := DecodeSetRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Options, []byte{7, 7}) {
		t.Fatalf("options = %v", got.Options)
	}
	if !reflect.DeepEqual(got.Targets, []uint16{0, 1, 1}) {
		t.Fatalf("targets = %v", got.Targets)
	}
}

func TestDecodeSetRequestAcceptsEmptyWireArrays(t *testing.T) {
	got, err := DecodeSetRequest([]byte{0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Options) != 0 || len(got.Targets) != 0 {
		t.Fatalf("decoded request = %#v", got)
	}
}

func TestDecodeSetRequestRejectsMalformedLengths(t *testing.T) {
	tests := map[string][]byte{
		"nil":                   nil,
		"missing target count":  {1, 4},
		"target count only":     {1},
		"truncated target":      {1, 4, 1, 9},
		"trailing byte":         {1, 4, 1, 9, 0, 0},
		"missing second target": {2, 1, 2, 2, 1, 0},
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSetRequest(body); !errors.Is(err, ErrSetRequest) {
				t.Fatalf("DecodeSetRequest(%x) error = %v, want ErrSetRequest", body, err)
			}
		})
	}
}

func TestDecodeSetRequestUsesFullCountWidthWithoutOverflow(t *testing.T) {
	options := make([]byte, 257)
	options[0] = 255
	for i := 1; i <= 255; i++ {
		options[i] = byte(i)
	}
	got, err := DecodeSetRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Options) != 255 || got.Options[254] != 255 || len(got.Targets) != 0 {
		t.Fatalf("decoded request lengths = %d/%d", len(got.Options), len(got.Targets))
	}

	targets := make([]byte, 2+2*255)
	targets[0] = 0
	targets[1] = 255
	for i := 0; i < 255; i++ {
		targets[2+i*2] = byte(i)
		targets[3+i*2] = 1
	}
	got, err = DecodeSetRequest(targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 255 || got.Targets[254] != 0x01fe {
		t.Fatalf("decoded target count/final = %d/%#x", len(got.Targets), got.Targets[254])
	}
}
