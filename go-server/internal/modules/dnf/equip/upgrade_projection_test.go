package equip

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestUpgradeProjectionRejectsOutOfRangeAndPreservesFlags(t *testing.T) {
	raw := make([]byte, 119)
	raw[10], raw[19] = 0xe3, 4
	binary.LittleEndian.PutUint16(raw[20:22], 123)
	before := append([]byte(nil), raw...)
	ProjectUpgradeFields(raw, map[string]string{"ext_data0": "256", "reinforce": "32", "byte_13": "-1", "marker_16": "65536"})
	if !bytes.Equal(raw, before) {
		t.Fatal("invalid metadata changed the cached instance")
	}
	ProjectUpgradeFields(raw, map[string]string{"reinforce": "10"})
	if raw[10] != 0xea || raw[19] != 4 || binary.LittleEndian.Uint16(raw[20:22]) != 123 {
		t.Fatal("reinforcement fallback changed flags or amplification")
	}
	ProjectUpgradeFields(raw, map[string]string{"reinforce": "0"})
	if raw[10] != 0xe0 {
		t.Fatal("explicit zero did not clear only the reinforcement bits")
	}
}

func TestUpgradeProjectionRejectsOtherWireLayouts(t *testing.T) {
	for _, size := range []int{0, 47, 83, 118, 120} {
		raw := bytes.Repeat([]byte{0x5a}, size)
		before := append([]byte(nil), raw...)
		ProjectUpgradeFields(raw, map[string]string{"ext_data0": "10", "byte_13": "4", "marker_16": "123"})
		if !bytes.Equal(raw, before) {
			t.Fatalf("changed a %d-byte layout", size)
		}
	}
}
