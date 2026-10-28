package party

import (
	"testing"
	"time"
)

func TestTradeInvitationCancelEnvelope(t *testing.T) {
	body := []byte{5, 0, 1, 0, 0, 0, 0, 133, 0}
	request, err := DecodeResponsePeerRequest(body)
	if err != nil || request.TargetID != 5 || request.Mode != 1 || request.RejectReason != 133 {
		t.Fatalf("cancel=%+v/%v", request, err)
	}
	for _, bad := range [][]byte{body[:8], append(append([]byte{}, body...), 0), {5, 0, 4, 0, 0, 0, 0, 133, 0}, {5, 0, 1, 0, 0, 0, 0, 0, 0}, {5, 0, 1, 0, 0, 0, 0, 133, 1}} {
		if _, err := DecodeResponsePeerRequest(bad); err == nil {
			t.Fatalf("invalid tail admitted: %x", bad)
		}
	}
}

func TestTradeInvitationLeaseAndOverlappingPrompts(t *testing.T) {
	m := NewRuntimePartyManager()
	if _, ok := m.RecordOnlinePeerRequest(5, 1, 1, 1, 1); !ok {
		t.Fatal("initial invite")
	}
	for _, pair := range [][2]uint16{{5, 9}, {9, 1}, {1, 5}, {1, 9}} {
		if _, ok := m.RecordOnlinePeerRequest(pair[0], 1, pair[1], 1, 1); ok {
			t.Fatalf("overlap admitted %v", pair)
		}
	}
	if _, ok := m.ConsumePeerRequest(5, 2, 1, 1, 1); ok {
		t.Fatal("stale generation consumed invitation")
	}
	if _, ok := m.ConsumePeerRequest(5, 1, 1, 1, 1); !ok {
		t.Fatal("valid invitation lost")
	}
	m.RecordOnlinePeerRequest(5, 1, 1, 1, 1)
	invite := m.invites[5]
	invite.expiresAt = time.Now().Add(-time.Second)
	m.invites[5] = invite
	if _, ok := m.ConsumePeerRequest(5, 1, 1, 1, 1); ok {
		t.Fatal("expired invitation accepted")
	}
	if len(m.invites) != 0 {
		t.Fatal("expired invitation retained")
	}
	m.RecordOnlinePeerRequest(5, 1, 1, 1, 1)
	invite = m.invites[5]
	invite.expiresAt = time.Now().Add(-time.Second)
	m.invites[5] = invite
	if _, ok := m.RecordOnlinePeerRequest(9, 1, 1, 1, 1); !ok {
		t.Fatal("expired invite blocked fresh request")
	}
	if _, ok := m.invites[5]; ok {
		t.Fatal("expired peer key retained")
	}
}
