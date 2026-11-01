package battlepass

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func premiumFixture(t *testing.T, balance int64) (repo.Group, *Catalog, Policy, Command) {
	t.Helper()
	g, c, p, cmd := fixture(t)
	a, _, _ := g.Account.Load(nil, "a")
	cerashop.SetBalance(&a, balance)
	a.Metadata["account_token_cera"] = "0"
	a.Metadata["account_happy_token_cera"] = "0"
	if err := g.Account.Save(nil, a); err != nil {
		t.Fatal(err)
	}
	cmd = ready(cmd, p)
	cmd.Action, cmd.Target = 4, PremiumPrice
	return g, c, p, cmd
}

func TestBattlePassPremiumPurchaseSharedAndRetroactive(t *testing.T) {
	g, c, p, cmd := premiumFixture(t, int64(PremiumPrice))
	v := query(t, g, c, p, cmd)
	if !v.State.Premium || v.CeraBalance != 0 || v.State.PremiumPurchasedAt != cmd.Now.Unix() || v.State.PremiumPricePaid != 100 || v.Missions[3].Flags != 1 || v.Missions[3].GrantedXP != 300 {
		t.Fatal("开通、余额或高级每日任务错误", v)
	}
	a, _, _ := g.Account.Load(nil, "a")
	if a.Metadata["keep"] != "yes" || a.Metadata["account_token_cera"] != "0" || a.Metadata["account_happy_token_cera"] != "0" {
		t.Fatal("影响其他元数据或币种")
	}
	cmd.CharacterID = "2"
	if _, err := Execute(nil, g, c, p, cmd); !errors.Is(err, ErrPremium) {
		t.Fatal("换角重复开通", err)
	}
	cmd.Action = 0
	v = query(t, g, c, p, cmd)
	if !v.State.Premium || v.CeraBalance != 0 {
		t.Fatal("账号权益未共享")
	}
	cmd.CharacterID, cmd.Action, cmd.Category, cmd.Target = "1", 2, 1, 1
	v = query(t, g, c, p, cmd)
	if !Claimed(v.State, 1, 1) || len(v.MailIDs) != 1 {
		t.Fatal("开通前已达等级奖励不能领取")
	}
	cmd.AccountID, cmd.CharacterID, cmd.Action = "b", "3", 0
	if query(t, g, c, p, cmd).State.Premium {
		t.Fatal("其他账号获得权益")
	}
}

func TestBattlePassPremiumRejectsInsufficientStaleAndInvalidPrice(t *testing.T) {
	for _, test := range []struct {
		name    string
		balance int64
		mutate  func(*Command)
		want    error
	}{
		{"三类余额合计不足", 99, func(*Command) {}, ErrFunds},
		{"错误报价", int64(PremiumPrice), func(c *Command) { c.Target-- }, ErrClaim},
		{"错误类别", int64(PremiumPrice), func(c *Command) { c.Category = 1 }, ErrClaim},
		{"错误槽位", int64(PremiumPrice), func(c *Command) { c.Slot = 1 }, ErrClaim},
		{"过期赛季", int64(PremiumPrice), func(c *Command) { c.Season++ }, ErrPeriod},
		{"刷新后旧报价", int64(PremiumPrice), func(c *Command) { c.Now = c.Now.AddDate(0, 0, 1) }, ErrPeriod},
		{"跨账号角色", int64(PremiumPrice), func(c *Command) { c.CharacterID = "3" }, repo.ErrMailboxAssetTransactionUnavailable},
		{"赛季结束", int64(PremiumPrice), func(c *Command) { c.Now = c.Now.AddDate(0, 0, 100) }, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, c, p, cmd := premiumFixture(t, test.balance)
			test.mutate(&cmd)
			if _, err := Execute(nil, g, c, p, cmd); !errors.Is(err, test.want) {
				t.Fatal("错误结果", err)
			}
			a, _, _ := g.Account.Load(nil, "a")
			if cerashop.Balance(a) != test.balance || a.Metadata[MetadataKey] != "" {
				t.Fatal("拒绝后仍扣款或开通")
			}
		})
	}
}

func TestBattlePassPremiumConcurrentPurchaseDebitsOnce(t *testing.T) {
	g, c, p, cmd := premiumFixture(t, 50000)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := cmd
			copy.CharacterID = strconv.Itoa(1 + i%2)
			_, err := Execute(nil, g, c, p, copy)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrPremium) {
				t.Errorf("并发开通失败：%v", err)
			}
		}(i)
	}
	wg.Wait()
	a, _, _ := g.Account.Load(nil, "a")
	if successes.Load() != 1 || cerashop.Balance(a) != 49900 {
		t.Fatal("并发重复扣款", successes.Load(), cerashop.Balance(a))
	}
}

// 注入同一真实内存事务的第二次元数据写失败，检查第一次扣款也回滚。
type premiumFailAccount struct{ repo.AccountRepository }

func (a premiumFailAccount) SaveMetadataEntry(ctx context.Context, id, key, value string, now time.Time) error {
	if key == MetadataKey {
		return errors.New("测试：权益写入失败")
	}
	record, found, err := a.Load(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrUnavailable
	}
	return repo.SaveAccountMetadataEntry(ctx, a.AccountRepository, record, key, value, now)
}

type premiumFailUnit struct {
	repo.MailboxAssetUnitOfWork
	transaction repo.AccountMailboxCharacterUnitOfWork
}

func (u premiumFailUnit) WithinAccountMailboxCharacter(ctx context.Context, a, ch string, f func(repo.AccountRepository, repo.CharacterRecord, repo.MailboxRepository) error) error {
	return u.transaction.WithinAccountMailboxCharacter(ctx, a, ch, func(accounts repo.AccountRepository, character repo.CharacterRecord, mail repo.MailboxRepository) error {
		return f(premiumFailAccount{accounts}, character, mail)
	})
}
func TestBattlePassPremiumPurchaseRollback(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(strconv.FormatBool(failWrite), func(t *testing.T) {
			g, c, p, cmd := premiumFixture(t, 15000)
			if failWrite {
				g.MailboxAssets = premiumFailUnit{g.MailboxAssets, g.MailboxAssets.(repo.AccountMailboxCharacterUnitOfWork)}
			} else {
				calls := 0
				cmd.Validate = func() bool { calls++; return calls == 1 }
			}
			if _, err := Execute(nil, g, c, p, cmd); err == nil {
				t.Fatal("应当失败")
			}
			a, _, _ := g.Account.Load(nil, "a")
			if cerashop.Balance(a) != 15000 || a.Metadata[MetadataKey] != "" {
				t.Fatal("扣款或权益未回滚")
			}
		})
	}
}
