package raid

import (
	"bytes"
	"testing"
)

func TestCurrentPeerCheckStrictActualContract(t *testing.T) {
	req, err := DecodeCurrentPeerCheckRequest([]byte{23, 31, 0, 7})
	if err != nil || req.Server != 7 || req.TargetCharKey != 31 || req.Channel != 23 {
		t.Fatalf("actual field order %+v %v", req, err)
	}
	actual, err := DecodeCurrentPeerCheckRequest([]byte{0x17, 8, 0, 0})
	if err != nil || actual.Server != 0 || actual.Channel != 23 || actual.TargetCharKey != 8 {
		t.Fatalf("fresh test1 to 2B capture decoded incorrectly: %+v %v", actual, err)
	}
	for _, bad := range [][]byte{nil, {7, 31}, {7, 31, 0}, {7, 31, 0, 23, 0}} {
		if _, err := DecodeCurrentPeerCheckRequest(bad); err == nil {
			t.Fatalf("non-four-byte request accepted: %x", bad)
		}
	}
	old, err := DecodeCheckRaidUserRequest([]byte{7, 31, 0})
	if err != nil || old.Mode != 7 || old.TargetKey != 31 {
		t.Fatal("legacy contract changed")
	}
	ack := BuildCurrentUnassignedPeerCheckACK()
	if !bytes.Equal(ack, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("actual ACK inactive cache: %x", ack)
	}
}
