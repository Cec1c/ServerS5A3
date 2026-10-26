package party

import (
	"errors"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"testing"
)

func TestRuntimePartySnapshotCommitGuard(t *testing.T) {
	for _, mode := range []string{"same", "leave", "leader", "slot", "generation", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			m := NewRuntimePartyManager()
			created := m.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
			m.Join(created.Party.ID, testRuntimePartyMember(202, 2))
			expected, _ := m.SnapshotByID(created.Party.ID)
			switch mode {
			case "leave":
				m.Leave(202, 2)
			case "leader":
				m.TransferLeader(101, 1, 202, 2)
			case "slot":
				expected.Members[0].Slot = 7
			case "generation":
				expected.Members[1].SessionGeneration++
			case "duplicate":
				expected.Members[1] = expected.Members[0]
			}
			called := false
			sentinel := errors.New("commit result")
			err := m.WithSnapshot(expected, func() error { called = true; return sentinel })
			if mode == "same" {
				if !called || err != sentinel {
					t.Fatal("valid callback", err)
				}
			} else if called || !errors.Is(err, ErrRuntimePartyChanged) {
				t.Fatal("changed membership admitted", err)
			}
		})
	}
}

func TestRuntimePartySoloCommitGuard(t *testing.T) {
	m := NewRuntimePartyManager()
	called := false
	if err := m.WithSoloMember(101, func() error { called = true; return nil }); err != nil || !called {
		t.Fatal(err)
	}
	m.Create(testRuntimePartyMember(101, 1), alignedcmd.PartyState{MaxMembers: 4})
	called = false
	if err := m.WithSoloMember(101, func() error { called = true; return nil }); !errors.Is(err, ErrRuntimePartyChanged) || called {
		t.Fatal("joined actor retained solo eligibility", err)
	}
}
