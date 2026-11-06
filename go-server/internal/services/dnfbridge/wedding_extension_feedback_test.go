package dnfbridge

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingExtensionFeedbackNativeReplyMatrix(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   uint16
		err  error
		code int32
		text int
	}{
		{"plant-occupied", 1048, wedding.ErrTreeOccupied, -4, 72849},
		{"harvest-missing", 1050, wedding.ErrTreeMissing, -4, 72852},
		{"harvest-immature", 1050, wedding.ErrTreeNotMature, -6, 72907},
		{"harvest-expired", 1050, wedding.ErrTreeExpired, -7, 72981},
		{"clear-missing", 1051, wedding.ErrTreeMissing, -4, 72852},
		{"capsule-energy", 1052, wedding.ErrCapsuleEnergy, -4, 72962},
		{"capsule-own-limit", 1052, wedding.ErrCapsuleOwnLimit, -5, 72963},
		{"capsule-empty", 1053, wedding.ErrCapsuleEmpty, -4, 73176},
		{"capsule-partner-limit", 1053, wedding.ErrCapsulePartnerLimit, -6, 72963},
		{"capsule-bag-full", 1054, errDungeonPickupInventoryFull, -6, 72632},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("transaction: %w", tc.err)
			code := currentWeddingExtensionFailureCode(tc.op, wrapped)
			if code != tc.code {
				t.Fatalf("got %d want %d", code, tc.code)
			}
			b := currentWeddingExtensionACK(tc.op, code, wedding.CapsuleState{})
			if len(b) != 5 {
				t.Fatalf("failed ACK length=%d", len(b))
			}
			row, err := json.Marshal(map[string]any{"name": tc.name, "opcode": tc.op, "payload_hex": hex.EncodeToString(b), "result": code, "string_id": tc.text})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("EXTENSION_FAILURE_CASE %s", row)
		})
	}
	for op := uint16(1047); op <= 1054; op++ {
		for _, err := range []error{errors.New("unknown"), wedding.ErrState, wedding.ErrItem, wedding.ErrUnavailable} {
			if currentWeddingExtensionFailureCode(op, err) != -1 {
				t.Fatal("unknown error assigned specific business reason", op, err)
			}
		}
	}
	// Current C1054/-4 opens a church-entry question with this dstr. Do not
	// copy the 86 receive-empty code into this protocol version.
	if currentWeddingExtensionFailureCode(1054, wedding.ErrCapsuleEmpty) != -1 {
		t.Fatal("send-empty reason reused as receive-empty")
	}
}
