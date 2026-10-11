package dnfbridge

import (
	"encoding/binary"
	"testing"
)

func TestV56LoginTokenLayoutAndLifecycle(t *testing.T) {
	body := make([]byte, 606)
	binary.LittleEndian.PutUint32(body, 1)
	body[4] = 0x30
	binary.LittleEndian.PutUint32(body[5:9], 32)
	const token = "de509f65000000000000000000000000"
	copy(body[9:], token)
	if got := clientLoginTokenFromBody(body); got != token {
		t.Fatalf("token = %q", got)
	}
	if !isCurrentChannelReconnectLifecycleBody(1, 1, len(body)) {
		t.Fatal("v56 login is not recognized as lifecycle")
	}
	for _, size := range []int{605, 607} {
		if isCurrentChannelReconnectDisplayProbeBodyLen(size) {
			t.Fatalf("accepted adjacent length %d", size)
		}
	}
	if isCurrentChannelReconnectLifecycleBody(1, 0, len(body)) {
		t.Fatal("accepted wrong classification")
	}
}
