package dnfbridge

import (
	"encoding/binary"
	"fmt"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	"testing"
)

func TestEpicBenefitsProjectionPreservesNativeUnsignedProgressBoundary(t *testing.T) {
	for _, n := range []uint32{0, 9000, 18000, 18001, 0x7fffffff, 0x80000000, 0xffffffff} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			state := onlineevent.EpicSnapshot{Invitations: n, Claimed: 7}
			b := buildCurrentEpicStateBody(state)
			want := n
			if want > 18000 {
				want = 18000
			}
			if len(b) != 8 || binary.LittleEndian.Uint32(b) != want || binary.LittleEndian.Uint32(b[4:]) != 7 {
				t.Fatalf("native progress projection counter=%d wire=%x want bounded=%d", n, b, want)
			}
			if state.Invitations != n {
				t.Fatal("durable count truncated")
			}
		})
	}
}
