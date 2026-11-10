package dnfbridge

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingTreeWaterNativeFailureReplies(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int32
		text int
	}{
		{"missing", wedding.ErrTreeMissing, -4, 72852},
		{"expired", wedding.ErrTreeExpired, -11, 72981},
		{"mature", wedding.ErrTreeMature, -9, 72973},
		{"quota", wedding.ErrTreeWaterQuota, -6, 72851},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("transaction: %w", errors.Join(wedding.ErrState, tc.err))
			code := currentWeddingExtensionFailureCode(1049, err)
			if code != tc.code {
				t.Fatalf("got %d want %d", code, tc.code)
			}
			for _, op := range []uint16{1047, 1048, 1052, 1053, 1054} {
				if currentWeddingExtensionFailureCode(op, err) != -1 {
					t.Fatal("water-only native error applied to other opcode", op)
				}
			}
			row, err := json.Marshal(map[string]any{"name": tc.name, "opcode": 1049, "payload_hex": hex.EncodeToString(currentWeddingExtensionACK(1049, code, wedding.CapsuleState{})), "result": code, "string_id": tc.text})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("TREE_WATER_ACK_CASE %s", row)
		})
	}
	if currentWeddingExtensionFailureCode(1049, errors.New("unknown")) != -1 {
		t.Fatal("unknown error disguised as a specific tree condition")
	}
}
