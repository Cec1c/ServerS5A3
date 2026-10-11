package accountfriend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) repo.Group {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	for _, a := range []repo.AccountRecord{{AccountID: "a", RepresentAccountName: "Alpha", Metadata: map[string]string{"other": "keep"}}, {AccountID: "b", RepresentAccountName: "Beta", Metadata: map[string]string{"other": "keep2"}}} {
		if err := g.Account.Save(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []repo.CharacterRecord{{CharacterID: "1", AccountID: "a", Name: "one"}, {CharacterID: "2", AccountID: "a", Name: "alt"}, {CharacterID: "3", AccountID: "b", Name: "three"}} {
		if err := g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return g
}
func commands() (Command, Command) {
	return Command{AccountID: "a", CharacterID: "1", TargetAccountID: "b", TargetName: "Beta", Text: "invitation"}, Command{AccountID: "b", CharacterID: "3", TargetAccountID: "a", TargetName: "Alpha", Text: "reverse invitation"}
}
func mustList(t *testing.T, g repo.Group, id, state string) Entry {
	t.Helper()
	rows, err := NewOwner(g).List(nil, id)
	if err != nil || len(rows) != 1 || rows[0].Relation.State != state {
		t.Fatalf("list %s: %+v %v", id, rows, err)
	}
	return rows[0]
}

func TestInvitationAcceptReloadAndAccountScope(t *testing.T) {
	g := fixture(t)
	a, b := commands()
	o := NewOwner(g)
	if err := o.Accept(nil, b); !errors.Is(err, ErrInvitation) {
		t.Fatal("unsolicited accept", err)
	}
	if err := o.Request(nil, a); err != nil {
		t.Fatal(err)
	}
	ar := mustList(t, g, "a", Outgoing)
	br := mustList(t, g, "b", Incoming)
	if ar.Relation.OwnCharacterID != "1" || br.Relation.PeerCharacterID != "1" || br.Relation.Message != "invitation" {
		t.Fatal("inviter identity/message lost")
	}
	if err := NewOwner(g).Accept(nil, b); err != nil {
		t.Fatal(err)
	}
	ar = mustList(t, g, "a", Friend)
	br = mustList(t, g, "b", Friend)
	if ar.Relation.PeerCharacterID != "3" || br.Relation.OwnCharacterID != "3" {
		t.Fatal("acceptor identity lost")
	}
	a.CharacterID = "2"
	a.Text = "private remark"
	if err := NewOwner(g).UpdateNote(nil, a); err != nil {
		t.Fatal(err)
	}
	ar = mustList(t, g, "a", Friend)
	br = mustList(t, g, "b", Friend)
	if ar.Relation.Note != "private remark" || br.Relation.Note != "" {
		t.Fatal("alt cannot access account friend or note leaked to peer")
	}
	// Changing the peer name changes projections and invalidates a stale request.
	row, _, _ := g.Account.Load(context.Background(), "b")
	row.RepresentAccountName = "Renamed"
	if err := g.Account.Save(nil, row); err != nil {
		t.Fatal(err)
	}
	if mustList(t, g, "a", Friend).Account.RepresentAccountName != "Renamed" {
		t.Fatal("stale nickname projection")
	}
	if err := o.Delete(nil, a); !errors.Is(err, ErrMissing) {
		t.Fatal("stale target name accepted", err)
	}
	a.TargetName = "Renamed"
	if err := NewOwner(g).Delete(nil, a); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		rows, err := NewOwner(g).List(nil, id)
		if err != nil || len(rows) != 0 {
			t.Fatal("delete not reciprocal", rows, err)
		}
	}
	aa, _, _ := g.Account.Load(nil, "a")
	bb, _, _ := g.Account.Load(nil, "b")
	if aa.Metadata["other"] != "keep" || bb.Metadata["other"] != "keep2" {
		t.Fatal("unrelated metadata changed")
	}
}

func TestCancelRefuseRolesAndNoSideEffects(t *testing.T) {
	for _, action := range []byte{4, 5} {
		t.Run(fmt.Sprint(action), func(t *testing.T) {
			g := fixture(t)
			a, b := commands()
			o := NewOwner(g)
			if err := o.Request(nil, a); err != nil {
				t.Fatal(err)
			}
			wrong, right := a, b
			if action == 5 {
				wrong, right = b, a
			}
			aa, _, _ := g.Account.Load(nil, "a")
			bb, _, _ := g.Account.Load(nil, "b")
			beforeA, _ := json.Marshal(aa)
			beforeB, _ := json.Marshal(bb)
			if err := o.RefuseCancel(nil, wrong, action); !errors.Is(err, ErrInvitation) {
				t.Fatal("wrong direction", err)
			}
			afterA, _, _ := g.Account.Load(nil, "a")
			afterB, _, _ := g.Account.Load(nil, "b")
			va, _ := json.Marshal(afterA)
			vb, _ := json.Marshal(afterB)
			if string(beforeA) != string(va) || string(beforeB) != string(vb) {
				t.Fatal("rejection mutated pair")
			}
			if err := o.RefuseCancel(nil, right, action); err != nil {
				t.Fatal(err)
			}
			if err := o.Accept(nil, b); !errors.Is(err, ErrInvitation) {
				t.Fatal("cancelled invitation accepted", err)
			}
			if err := o.Request(nil, a); err != nil {
				t.Fatal("reinvite", err)
			}
		})
	}
}

func TestConcurrentDuplicateAndOppositeDirectionRequests(t *testing.T) {
	g := fixture(t)
	a, b := commands()
	var wg sync.WaitGroup
	fail := make(chan error, 80)
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := a
			if i%2 == 1 {
				cmd = b
			}
			err := NewOwner(g).Request(nil, cmd)
			if err != nil && !errors.Is(err, ErrAlready) {
				fail <- err
			}
		}(i)
	}
	wg.Wait()
	close(fail)
	for err := range fail {
		t.Fatal(err)
	}
	x, err := NewOwner(g).List(nil, "a")
	if err != nil || len(x) != 1 {
		t.Fatal(x, err)
	}
	acceptor := b
	if x[0].Relation.State == Incoming {
		acceptor = a
	}
	if err := NewOwner(g).Accept(nil, acceptor); err != nil {
		t.Fatal(err)
	}
	mustList(t, g, "a", Friend)
	mustList(t, g, "b", Friend)
}

type unscopedAccount struct{ repo.AccountRepository }

func TestMutationsRejectMissingTransactionSpoofAndCorruptPair(t *testing.T) {
	g := fixture(t)
	a, b := commands()
	g.Account = unscopedAccount{g.Account}
	if err := NewOwner(g).Request(nil, a); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unsafe fallback", err)
	}
	g = fixture(t)
	a.CharacterID = "3"
	if err := NewOwner(g).Request(nil, a); !errors.Is(err, ErrIdentity) {
		t.Fatal("spoof", err)
	}
	a, b = commands()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewOwner(g).Request(ctx, a); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel", err)
	}
	aa, _, _ := g.Account.Load(nil, "a")
	aa.Metadata[prefix+"b"] = "corrupt"
	if err := g.Account.Save(nil, aa); err != nil {
		t.Fatal(err)
	}
	if err := NewOwner(g).Request(nil, a); !errors.Is(err, ErrInvitation) {
		t.Fatal("corrupt row overwritten", err)
	}
	if err := NewOwner(g).Accept(nil, b); !errors.Is(err, ErrInvitation) {
		t.Fatal("one-sided state accepted", err)
	}
}

func TestPendingCapacitySerializesAcrossOwners(t *testing.T) {
	g := fixture(t)
	aa, _, _ := g.Account.Load(nil, "a")
	value, _ := json.Marshal(Relation{State: Outgoing, ExpiresAt: time.Now().Add(time.Hour)})
	for i := 0; i < MaximumPending; i++ {
		aa.Metadata[fmt.Sprintf("%sp%d", prefix, i)] = string(value)
	}
	if err := g.Account.Save(nil, aa); err != nil {
		t.Fatal(err)
	}
	a, _ := commands()
	if err := NewOwner(g).Request(nil, a); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity", err)
	}
	bb, _, _ := g.Account.Load(nil, "b")
	if len(bb.Metadata) != 1 {
		t.Fatal("capacity failure partially created invitation")
	}
}

func TestExpiredInvitationCannotBeAcceptedAndCanBeReplaced(t *testing.T) {
	g := fixture(t)
	a, b := commands()
	o := NewOwner(g)
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	o.now = func() time.Time { return at }
	if err := o.Request(nil, a); err != nil {
		t.Fatal(err)
	}
	rows, err := o.List(nil, "a")
	if err != nil || len(rows) != 1 || !rows[0].Relation.ExpiresAt.Equal(at.Add(InvitationLifetime)) {
		t.Fatal("expiry policy not persisted", rows, err)
	}
	at = at.Add(InvitationLifetime)
	if err := o.Accept(nil, b); !errors.Is(err, ErrInvitation) {
		t.Fatal("expired accept", err)
	}
	rows, err = o.List(nil, "a")
	if err != nil || len(rows) != 0 {
		t.Fatal("expired list", rows, err)
	}
	if err := o.Request(nil, a); err != nil {
		t.Fatal("expired replacement", err)
	}
	if err := o.Accept(nil, b); err != nil {
		t.Fatal("fresh accept", err)
	}
}
