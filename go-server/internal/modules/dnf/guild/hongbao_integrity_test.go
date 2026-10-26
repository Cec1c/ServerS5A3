package guild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestHongbaoRandomFailurePreservesInventoryAndReceipt(t *testing.T) {
	for _, mode := range []string{"error", "out_of_range"} {
		t.Run(mode, func(t *testing.T) {
			repos, o, now := hongbaoGroup(t)
			ctx := context.Background()
			p, err := o.Purchase(ctx, HongbaoPurchaseRequest{hongbaoRequest(1, "buy"), 103941}, now)
			if err != nil {
				t.Fatal(err)
			}
			before := hongbaoSnapshot(t, repos)
			o.contract.RandIntn = func(n uint32) (uint32, error) {
				if mode == "error" {
					return 0, errors.New("entropy unavailable")
				}
				return n, nil
			}
			q := HongbaoIssueRequest{hongbaoRequest(1, "issue"), p.Product.ItemID, 30}
			if _, err := o.Issue(ctx, q, now); err == nil {
				t.Fatal("invalid random result accepted")
			}
			if !reflect.DeepEqual(before, hongbaoSnapshot(t, repos)) {
				t.Fatal("failed draw changed inventory or persisted shares")
			}
			o.contract.RandIntn = func(n uint32) (uint32, error) { return n - 1, nil }
			out, err := o.Issue(ctx, q, now)
			if err != nil || out.Replayed || out.RecordID != 1 {
				t.Fatal(out, err)
			}
			reg, err := Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			var sum uint32
			for _, share := range reg.Hongbao.Records[out.RecordID].Shares {
				if share == 0 {
					t.Fatal("zero share")
				}
				sum += share
			}
			if sum != 5000 {
				t.Fatal(sum)
			}
		})
	}
}

func TestHongbaoProductionDisbandPreservesOfflineSettlement(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	ctx := context.Background()
	id := hongbaoIssue(t, o, now, "disband", 103938, 5).RecordID
	claim := hongbaoClaim(t, o, now, id, 2)
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	sender := hongbaoRequest(1, "").Owner
	for i := 2; i <= 32; i++ {
		if _, err := reg.DepartMember(sender, hongbaoRequest(i, "").Owner, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reg.DepartMember(sender, sender, now); err != nil {
		t.Fatal(err)
	}
	if len(reg.Guilds) != 0 || len(reg.Hongbao.Records) != 1 {
		t.Fatal("disband discarded global transaction ledger")
	}
	actor, _, _ := repos.Character.Load(ctx, sender.CharacterID)
	if _, err := reg.Create(actor, "另一公会", itemexpiration.NextDailyDeleteAt(now)); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewHongbaoOwner(repos, hongbaoFixtureContract(t))
	if err != nil {
		t.Fatal(err)
	}
	at := now.Add(24 * time.Hour)
	ids, err := restarted.Pending(ctx, at, 10)
	if err != nil || !reflect.DeepEqual(ids, []uint32{id}) {
		t.Fatal(ids, err)
	}
	before := hongbaoSnapshot(t, repos)
	view, err := restarted.View(ctx, sender, at)
	if err != nil || view.GuildID == 1 || len(view.Records) != 0 {
		t.Fatal(view, err)
	}
	if _, err := restarted.History(ctx, sender, id, at); !errors.Is(err, ErrHongbaoUnavailable) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, hongbaoSnapshot(t, repos)) {
		t.Fatal("query settled old guild record")
	}
	s, err := restarted.Settle(ctx, id, at)
	if err != nil || s.Sender != sender || s.Coins != claim.Amount/100 || s.Points != 0 || s.RefundTokenCera != 0 || len(s.MailIDs) != 1 {
		t.Fatal(s, err)
	}
	again, err := restarted.Settle(ctx, id, at.Add(time.Hour))
	if err != nil || !again.Replayed {
		t.Fatal(again, err)
	}
	again.Replayed = false
	if !reflect.DeepEqual(again, s) {
		t.Fatal("sender or settlement changed on replay", again, s)
	}
	reg, _ = Load(ctx, repos.Settings)
	_, m, ok := reg.GuildForOwner(sender)
	if !ok || m.ContributionPoints != 0 {
		t.Fatal("contribution transferred to new membership", m)
	}
}

func TestHongbaoCappedContributionStillRefundsAtSettlementTime(t *testing.T) {
	repos, o, now := hongbaoGroup(t)
	ctx := context.Background()
	id := hongbaoIssue(t, o, now, "refund", 103941, 5).RecordID
	claim := hongbaoClaim(t, o, now, id, 2)
	at := now.Add(24 * time.Hour)
	actor, _, _ := repos.Character.Load(ctx, "1")
	actor.Stats[hongbaoContributionDay] = itemexpiration.NextDailyDeleteAt(at).Unix()
	actor.Stats[hongbaoContributionCount] = 500
	if err := repos.Character.Save(ctx, actor); err != nil {
		t.Fatal(err)
	}
	deliver := o.contract.DeliverMail
	called := 0
	o.contract.DeliverMail = func(ctx context.Context, tx r.Group, actor r.CharacterRecord, a HongbaoMailAward, received time.Time) ([]string, error) {
		called++
		if !received.Equal(at) || a.Coins != 0 || a.RefundTokenCera != 5000-claim.Amount {
			t.Fatal(a, received)
		}
		return deliver(ctx, tx, actor, a, received)
	}
	s, err := o.Settle(ctx, id, at)
	if err != nil || s.Points != 0 || s.Coins != 0 || s.RefundTokenCera != 5000-claim.Amount || called != 1 {
		t.Fatal(s, err, called)
	}
	if _, err := o.Settle(ctx, id, at); err != nil || called != 1 {
		t.Fatal("replay redelivered mail", err, called)
	}
}

func TestHongbaoMailProjectionRejectsInvalidResultAtomically(t *testing.T) {
	for _, mode := range []string{"empty", "missing", "duplicate", "no_attachments", "wrong_recipient", "reused"} {
		t.Run(mode, func(t *testing.T) {
			repos, o, now := hongbaoGroup(t)
			ctx := context.Background()
			id := hongbaoIssue(t, o, now, "mail", 103941, 5).RecordID
			actor, _, _ := repos.Character.Load(ctx, "1")
			old, err := hongbaoFixtureMail(ctx, repos, actor, HongbaoMailAward{RecordID: 99, Coins: 1}, now)
			if err != nil {
				t.Fatal(err)
			}
			before := hongbaoSnapshot(t, repos)
			o.contract.DeliverMail = func(ctx context.Context, tx r.Group, actor r.CharacterRecord, a HongbaoMailAward, at time.Time) ([]string, error) {
				switch mode {
				case "empty":
					return nil, nil
				case "missing":
					return []string{"missing"}, nil
				case "reused":
					return old, nil
				}
				ids, err := hongbaoFixtureMail(ctx, tx, actor, a, at)
				if err != nil {
					return nil, err
				}
				if mode == "duplicate" {
					return append(ids, ids[0]), nil
				}
				box, _, err := tx.Mailbox.Load(ctx, actor.CharacterID)
				if err != nil {
					return nil, err
				}
				mail := box.Mails[ids[0]]
				if mode == "no_attachments" {
					mail.Attachments = nil
				} else {
					mail.RecipientCharacterID = "2"
				}
				box.Mails[ids[0]] = mail
				return ids, tx.Mailbox.Save(ctx, box)
			}
			if _, err := o.Settle(ctx, id, now.Add(24*time.Hour)); !errors.Is(err, ErrHongbaoState) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, hongbaoSnapshot(t, repos)) {
				t.Fatal("bad mail projection partially committed")
			}
		})
	}
}

func TestHongbaoCorruptLedgerIsRejectedOnRead(t *testing.T) {
	for _, mode := range []string{"sum", "zero_share", "reused_id", "claim_amount", "repeat_account", "sender"} {
		t.Run(mode, func(t *testing.T) {
			repos, o, now := hongbaoGroup(t)
			ctx := context.Background()
			id := hongbaoIssue(t, o, now, "corrupt", 103941, 5).RecordID
			hongbaoClaim(t, o, now, id, 2)
			hongbaoClaim(t, o, now, id, 3)
			if mode == "sender" {
				if _, err := o.Settle(ctx, id, now.Add(24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			reg, err := Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			h := reg.Hongbao.Records[id]
			switch mode {
			case "sum":
				h.Shares[4]++
			case "zero_share":
				h.Shares[3] += h.Shares[4]
				h.Shares[4] = 0
			case "reused_id":
				reg.Hongbao.NextID = id
			case "claim_amount":
				h.Claims[0].Amount++
			case "repeat_account":
				h.Claims[1].Owner.AccountID = h.Claims[0].Owner.AccountID
			case "sender":
				h.Settlement.Sender = hongbaoRequest(2, "").Owner
			}
			reg.Hongbao.Records[id] = h
			encoded, err := json.Marshal(reg)
			if err != nil {
				t.Fatal(err)
			}
			if err := repos.Settings.Save(ctx, r.SettingsRecord{Scope: RegistryScope, Values: map[string]string{registryStateKey: string(encoded)}}); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(ctx, repos.Settings); !errors.Is(err, ErrHongbaoState) {
				t.Fatal(fmt.Sprint("corrupt ledger accepted: ", err))
			}
		})
	}
}
