package dnfbridge

import "testing"

func TestWeddingGameplayRegistersAllCurrentNativeCommands(t *testing.T) {
	m := weddingGameplayModule()
	if len(m.LegacyHandlers) != 23 || len(m.UpperHandlers) != 23 {
		t.Fatalf("incomplete wedding routing legacy=%d upper=%d", len(m.LegacyHandlers), len(m.UpperHandlers))
	}
	for op := uint16(1032); op <= 1054; op++ {
		if m.LegacyHandlers[op] == nil || m.UpperHandlers[op] == nil {
			t.Fatalf("native wedding command %d has no real route", op)
		}
	}
}
