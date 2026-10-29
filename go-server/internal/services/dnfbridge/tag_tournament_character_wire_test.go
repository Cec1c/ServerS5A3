package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTagCharacterWireWrapsOneExistingActorAndOneNamedBackup(t *testing.T) {
	var fixtures []map[string]any
	for _, name := range []string{"备用队员", "role2", strings.Repeat("测", 15)} {
		actor := tagActorWireFixture()
		inner, err := buildCurrentTagTournamentActorWire(actor)
		if err != nil {
			t.Fatal(err)
		}
		body, err := buildCurrentTagTournamentCharacterWire(29, 2, name, actor)
		if err != nil {
			t.Fatal(err)
		}
		n := int(binary.LittleEndian.Uint32(body[5:9]))
		rawName := rosterNameBytes(name)
		if name == strings.Repeat("测", 15) {
			rawName = bytes.Repeat([]byte{0xb2, 0xe2}, 15)
		}
		if !bytes.Equal(body[:5], []byte{1, 29, 0, 1, 2}) || n != len(rawName) || !bytes.Equal(body[9:9+n], rawName) || !bytes.Equal(body[9+n:], inner) {
			t.Fatal("1389主备层次或姓名错位")
		}
		fixtures = append(fixtures, map[string]any{"owner": 29, "selector": 2, "name": rawName, "body": body, "actor": inner})
	}
	if target := os.Getenv("DNFBRIDGE_TAG_CHARACTER_WIRE_FIXTURES"); target != "" {
		raw, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTagCharacterWireRejectsInvalidHeaderBeforeEncoding(t *testing.T) {
	for _, test := range []struct {
		key      uint16
		selector byte
		name     string
	}{{0, 1, "角色"}, {0xffff, 1, "角色"}, {29, 0xff, "角色"}, {29, 1, ""}, {29, 1, strings.Repeat("测", 16)}} {
		body, err := buildCurrentTagTournamentCharacterWire(test.key, test.selector, test.name, tagActorWireFixture())
		if err == nil || len(body) != 0 {
			t.Fatal("接受无效1389头", test)
		}
	}
}
