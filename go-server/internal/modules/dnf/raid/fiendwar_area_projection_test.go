package raid

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestRealPVFFiendWarAreaProjectionNativeFixtureAndInvalidSnapshot(t *testing.T) {
	c, err := LoadFiendWarConfiguration(realFiendWarSource(t))
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[uint32]string, len(c.Areas))
	for _, a := range c.Areas {
		states[a.ID] = a.InitialState
	}
	for _, state := range []string{"empty", "enemy", "monster", "user_enemy", "user_monster", "enemy_monster"} {
		states[c.Areas[0].ID] = state
		projection, err := c.AreaStateSymbols(states)
		if err != nil {
			t.Fatal(state, err)
		}
		body, err := BuildSymbolBody(projection)
		if err != nil || int(body[0]) != len(c.Areas) || len(body) != 1+8*len(c.Areas) {
			t.Fatal("actual full area table not encoded", state, err)
		}
		seen := map[uint32]bool{}
		for i := 0; i < int(body[0]); i++ {
			id := binary.LittleEndian.Uint32(body[1+8*i:])
			if id == 0 || seen[id] {
				t.Fatal("reset/repeated area identifier")
			}
			seen[id] = true
		}
		if out := os.Getenv("DNFBRIDGE_FIEND_AREA_FIXTURE_OUT"); out != "" {
			if err := os.MkdirAll(out, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "area-"+state+".bin"), body, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, mutation := range []string{"unknown", "missing", "foreign", "missing-symbol", "duplicate-symbol"} {
		t.Run(mutation, func(t *testing.T) {
			local := c
			local.Symbols = copyAntonMap(c.Symbols)
			input := copyAntonMap(states)
			key := c.Areas[0].ID
			switch mutation {
			case "unknown":
				input[key] = "client_clear"
			case "missing":
				delete(input, key)
			case "foreign":
				delete(input, key)
				input[999] = "empty"
			case "missing-symbol":
				delete(local.Symbols, "2 AREA STATE")
			case "duplicate-symbol":
				local.Symbols["2 AREA STATE"] = local.Symbols["3 AREA STATE"]
			}
			if result, err := local.AreaStateSymbols(input); err == nil || result != nil {
				t.Fatal("invalid snapshot published", result, err)
			}
		})
	}
}
