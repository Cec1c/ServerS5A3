package tagtournament

import (
	"context"
	"reflect"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestLobbyReadDoesNotChargeResetOrExposeMutableState(t *testing.T) {
	o, g, _ := ownerFixture(t)
	ctx := context.Background()
	r := admitRun(t, o, Normal)
	before, _, _ := g.Account.Load(ctx, "a")
	chBefore, _, _ := g.Character.Load(ctx, "1")
	view, err := o.Lobby(ctx, "a")
	if err != nil || !view.Registered || view.Characters[0].CharacterID != "1" || view.Characters[1].CharacterID != "2" || !view.Day.Paid || view.Day.ActiveRun == "" {
		t.Fatal(view, err)
	}
	view.Characters[0].Stats["gold"] = 0
	view.Day.NormalClaims[12345] = true
	view.Day.ActiveRun = "被调用者改写"
	viewAgain, err := o.Lobby(ctx, "a")
	if err != nil || viewAgain.Characters[0].Stats["gold"] != chBefore.Stats["gold"] || viewAgain.Day.ActiveRun != r.admission.token || viewAgain.Day.NormalClaims[12345] {
		t.Fatal("休息室投影共享了可变状态", viewAgain, err)
	}
	after, _, _ := g.Account.Load(ctx, "a")
	chAfter, _, _ := g.Character.Load(ctx, "1")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(chBefore, chAfter) || len(mailTotals(t, g)) != 0 {
		t.Fatal("读取休息室改变了账本、金币或邮件")
	}
}

func TestLobbyAndAdmissionRejectMalformedRosterWithoutRepairingIt(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `["1"]`, `["1","2","3"]`, `["1","1"]`, `["1","2"] {}`} {
		t.Run(raw, func(t *testing.T) {
			o, g, _ := ownerFixture(t)
			ctx := context.Background()
			ac, _, _ := g.Account.Load(ctx, "a")
			ac.Metadata[rosterKey] = raw
			if err := g.Account.Save(ctx, ac); err != nil {
				t.Fatal(err)
			}
			if _, err := o.Lobby(ctx, "a"); err == nil {
				t.Fatal("损坏名单被显示为有效队伍")
			}
			if _, err := o.Admit(ctx, "a", "1", [2]string{"1", "2"}, Normal); err == nil {
				t.Fatal("损坏名单仍能扣费入场")
			}
			after, _, _ := g.Account.Load(ctx, "a")
			ch, _, _ := g.Character.Load(ctx, "1")
			if !reflect.DeepEqual(ac, after) || ch.Stats["gold"] != 1000000 {
				t.Fatal("拒绝时改变了名单或资产")
			}
		})
	}
	o, g, _ := ownerFixture(t)
	ctx := context.Background()
	ac, _, _ := g.Account.Load(ctx, "a")
	delete(ac.Metadata, rosterKey)
	if err := g.Account.Save(ctx, ac); err != nil {
		t.Fatal(err)
	}
	view, err := o.Lobby(ctx, "a")
	if err != nil || view.Registered || !reflect.DeepEqual(view.Characters, [2]repo.CharacterRecord{}) {
		t.Fatal("未注册账号被自动组队", view, err)
	}
}

type lobbyBeforeTransactionTx struct {
	base   repo.AccountTradeMailboxUnitOfWork
	before func()
}

func (f lobbyBeforeTransactionTx) WithinAccountTradeMailbox(ctx context.Context, account string, ids []string, recipient string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	f.before()
	return f.base.WithinAccountTradeMailbox(ctx, account, ids, recipient, apply)
}

func TestRegisterRosterChecksSentIdentityInsideTransaction(t *testing.T) {
	for _, field := range []string{"槽位", "姓名", "负槽位"} {
		t.Run(field, func(t *testing.T) {
			o, g, _ := ownerFixture(t)
			ctx := context.Background()
			old, err := o.Lobby(ctx, "a")
			if err != nil {
				t.Fatal(err)
			}
			before, _, _ := g.Account.Load(ctx, "a")
			o.tx = lobbyBeforeTransactionTx{base: o.tx, before: func() {
				ch, _, _ := g.Character.Load(ctx, "2")
				switch field {
				case "槽位":
					ch.Slot = 9
				case "姓名":
					ch.Name = "另一个名字"
				case "负槽位":
					ch.Slot = -1
				}
				if err := g.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
			}}
			if err := o.RegisterRosterFromSnapshot(ctx, "a", "1", old.Characters); err == nil {
				t.Fatal("旧角色列表通过了事务内身份检查")
			}
			after, _, _ := g.Account.Load(ctx, "a")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("拒绝旧名单仍写入了账号")
			}
		})
	}
}
