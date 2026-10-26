package dnfbridge

import (
	"bytes"
	"testing"
)

func TestAikenInstanceWireR138ProjectionBoundaries(t *testing.T) {
	var entry currentItemListEntry
	if got := currentMode1EquipmentCustomStateFromItem(nil, entry); len(got) != 0 {
		t.Fatal("zero state changed existing wire")
	}
	copy(entry.data[0x72:], []byte{255, 128, 127, 254, 255})
	for _, key := range []string{"mode1_raw_e_hex", "mode1_state_188_192_raw_hex"} {
		want := []byte{7, 6, 5, 4, 3}
		if got := currentMode1EquipmentCustomStateFromItem(map[string]string{key: "0706050403"}, entry); !bytes.Equal(got, want) {
			t.Fatalf("explicit override %s lost", key)
		}
	}
	got := currentMode1EquipmentCustomStateFromItem(nil, entry)
	if !bytes.Equal(got, entry.data[0x72:]) {
		t.Fatal("unsigned byte boundary lost")
	}
	got[0] = 0
	if entry.data[0x72] != 255 {
		t.Fatal("projection aliases raw input")
	}
}
