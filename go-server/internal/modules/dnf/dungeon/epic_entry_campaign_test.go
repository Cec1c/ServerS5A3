package dungeon

import (
	"context"
	"errors"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	"testing"
	"time"
)

func TestEpicEntryCampaignCounterSamePhysicalReceiptAndRollback(t *testing.T) {
	o, g, cmd := epicOwnerFixture(t)
	c := onlineevent.EpicCampaign{Version: 1, CreatedAt: cmd.UpdatedAt}
	d := cmd.UpdatedAt.In(onlineevent.ChinaCalendar).Add(-6 * time.Hour)
	c.Starts = time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, onlineevent.ChinaCalendar)
	c.Ends = c.Starts.AddDate(0, 0, 28)
	cmd.Campaign = &c
	underlying := g.CharacterAssets
	fault := errors.New("physical abort")
	g.CharacterAssets = epicRollbackAssets{underlying, fault}
	o, _ = NewOwner(g)
	if _, e := o.ConsumeEpicEntry(context.Background(), cmd); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	ch, _, _ := g.Character.Load(nil, "31")
	state, e := onlineevent.EpicCharacterSnapshot(ch, c, cmd.UpdatedAt)
	if e != nil || state.Invitations != 0 {
		t.Fatal(state, e)
	}
	if seq, fp, tickets := epicBalance(t, g); seq != 0 || fp != 100 || tickets != 90 {
		t.Fatal(seq, fp, tickets)
	}
	g.CharacterAssets = underlying
	o, _ = NewOwner(g)
	for i := 0; i < 2; i++ {
		r, e := o.ConsumeEpicEntry(nil, cmd)
		if e != nil || r.Replay != (i == 1) {
			t.Fatal(r, e)
		}
	}
	ch, _, _ = g.Character.Load(nil, "31")
	state, e = onlineevent.EpicCharacterSnapshot(ch, c, cmd.UpdatedAt)
	if e != nil || state.Invitations != 30 {
		t.Fatal(state, e)
	}
	cmd.ExpectedSequence = 1
	cmd.Nonce = 101
	cmd.DungeonID = 6064
	if _, e = o.ConsumeEpicEntry(nil, cmd); e != nil {
		t.Fatal(e)
	}
	ch, _, _ = g.Character.Load(nil, "31")
	state, e = onlineevent.EpicCharacterSnapshot(ch, c, cmd.UpdatedAt)
	if e != nil || state.Invitations != 75 {
		t.Fatal(state, e)
	}
}
