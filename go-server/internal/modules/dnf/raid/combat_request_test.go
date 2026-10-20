package raid

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Execute bodies captured from the complete actual client writers, including
// the native header constructor and all three scalar writers, through Go.
func TestCurrentRaidCombatRequestsNativeWriterBodies(t *testing.T) {
	dir := os.Getenv("DNF90_ANTON_NATIVE_DIR")
	if dir == "" {
		t.Skip("native writer evidence directory not set")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "anton-command-writers-native-proof-v92.json"))
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		Passed bool   `json:"passed"`
		SHA    string `json:"client_sha256"`
		Cases  []struct {
			Packets []struct {
				Opcode uint16 `json:"opcode"`
				Body   string `json:"body_hex"`
			} `json:"packets"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &proof); err != nil {
		t.Fatal(err)
	}
	if !proof.Passed || len(proof.Cases) != 22 || proof.SHA != "ceebd829c1bbab3378d20236de92118cfa94c2bd46ba7979ca43c31f7d7b742b" {
		t.Fatal("native writer evidence mismatch")
	}
	counts := map[uint16]int{}
	for _, c := range proof.Cases {
		for _, packet := range c.Packets {
			body, err := hex.DecodeString(packet.Body)
			if err != nil {
				t.Fatal(err)
			}
			switch packet.Opcode {
			case 659:
				_, err = DecodeMovieSkipRequest(body)
			case 660:
				_, err = DecodeRewardCardRequest(body)
			case 661:
				var req DoBehaviorRequest
				req, err = DecodeDoBehaviorRequest(body)
				if req.Scene != 106 || req.Behavior != 1 {
					t.Fatal("typed native behavior changed", req)
				}
			case 662:
				var req SetSymbolRequest
				req, err = DecodeSetSymbolRequest(body)
				if req.SourceValue != 110 || req.TargetValue != 1700 {
					t.Fatal("native symbol values changed", req)
				}
			default:
				t.Fatal("unexpected native opcode", packet.Opcode)
			}
			if err != nil {
				t.Fatal(packet, err)
			}
			counts[packet.Opcode]++
		}
	}
	if counts[659] != 2 || counts[660] != 20 || counts[661] != 1 || counts[662] != 3 {
		t.Fatal("native packet coverage changed", counts)
	}
}

func TestCurrentRaidCombatRequestsRejectOtherWidthsAndRanges(t *testing.T) {
	for _, body := range [][]byte{nil, {0, 0}, {2}, {255}} {
		if _, err := DecodeMovieSkipRequest(body); err == nil {
			t.Fatal("invalid movie request accepted", body)
		}
	}
	for _, body := range [][]byte{nil, {1}, {1, 0, 0}, {3, 0}, {1, 4}, {255, 255}} {
		if _, err := DecodeRewardCardRequest(body); err == nil {
			t.Fatal("invalid reward request accepted", body)
		}
	}
	for n := 0; n <= 12; n++ {
		if n != 6 {
			if _, err := DecodeDoBehaviorRequest(make([]byte, n)); err == nil {
				t.Fatal("incorrect behavior width accepted", n)
			}
		}
	}
	req, err := DecodeDoBehaviorRequest([]byte{0x78, 0x56, 0x34, 0x12, 0xfe, 0xdc})
	if err != nil || req.Scene != 0x12345678 || req.Behavior != 0xdcfe {
		t.Fatal("behavior values truncated", req, err)
	}
}
