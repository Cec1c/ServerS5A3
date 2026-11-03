package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixture() ([]byte, func(int) string) {
	names := []string{"[image]", "[/image]", "[town]", "[index]", "[area]", "[type]", "[real rect]", "[project rect]", "[/area]", "[/town]"}
	var raw []byte
	tag := func(i int) {
		b := make([]byte, 5)
		b[0] = 3
		binary.LittleEndian.PutUint32(b[1:], uint32(i))
		raw = append(raw, b...)
	}
	integer := func(v int) {
		b := make([]byte, 5)
		b[0] = 0
		binary.LittleEndian.PutUint32(b[1:], uint32(v))
		raw = append(raw, b...)
	}
	tag(0)
	tag(1)
	tag(2)
	tag(3)
	integer(11)
	tag(4)
	tag(3)
	integer(0)
	tag(5)
	integer(0)
	tag(6)
	for _, v := range []int{34, 138, 2620, 356} {
		integer(v)
	}
	tag(7)
	for _, v := range []int{277, 289, 424, 317} {
		integer(v)
	}
	tag(8)
	tag(9)
	return raw, func(i int) string {
		if i < 0 || i >= len(names) {
			return ""
		}
		return names[i]
	}
}
func TestMissingTownRegistrationAndPreservation(t *testing.T) {
	raw, resolve := fixture()
	before, _ := tokens(raw, resolve)
	ids, _ := townIDs(before)
	if !reflect.DeepEqual(ids, []int32{11}) {
		t.Fatal(ids)
	}
	out, err := patch(raw, resolve)
	if err != nil {
		t.Fatal(err)
	}
	after, err := tokens(out, resolve)
	if err != nil {
		t.Fatal(err)
	}
	ids, err = townIDs(after)
	if err != nil || !reflect.DeepEqual(ids, []int32{13, 11}) {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	// The existing image and every old town byte survive unchanged around one insertion.
	at := 10
	inserted := len(out) - len(raw)
	if !bytes.Equal(raw[:at], out[:at]) || !bytes.Equal(raw[at:], out[at+inserted:]) {
		t.Fatal("existing minimap content changed")
	}
	if _, err = patch(out, resolve); err == nil {
		t.Fatal("repeated application must be rejected")
	}
	if _, err = patch(raw[:len(raw)-1], resolve); err == nil {
		t.Fatal("truncated stream accepted")
	}
}
func TestOutputGuards(t *testing.T) {
	d := t.TempDir()
	in := filepath.Join(d, "in.pvf")
	out := filepath.Join(d, "out.pvf")
	report := filepath.Join(d, "report.json")
	os.WriteFile(in, []byte("unreviewed"), 0600)
	if err := run(in, in, report); err == nil {
		t.Fatal("in-place edit allowed")
	}
	if err := run(in, out, report); err == nil {
		t.Fatal("unknown PVF accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("rejected input left an output")
	}
	os.WriteFile(out, []byte("existing"), 0600)
	if err := run(in, out, report); err == nil {
		t.Fatal("overwriting output allowed")
	}
	b, _ := os.ReadFile(out)
	if string(b) != "existing" {
		t.Fatal("existing output changed")
	}
}
