package battlepass

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func battleWallet(t *testing.T, g repo.Group, w cerashop.Wallet) {
	t.Helper()
	a, _, _ := g.Account.Load(nil, "a")
	cerashop.SetBalance(&a, w.Cera)
	cerashop.SetTokenCeraBalance(&a, w.TokenCera)
	cerashop.SetHappyTokenCeraBalance(&a, w.HappyTokenCera)
	if e := g.Account.Save(nil, a); e != nil {
		t.Fatal(e)
	}
}
func walletQuote(cmd Command, w cerashop.Wallet) Command {
	p, _ := cerashop.PlanWalletPayment(w, int64(PremiumPrice), cerashop.PayPolicyWaterfall)
	cmd.QuoteHappy, cmd.QuoteToken = uint32(p.FromHappyTokenCera), uint32(p.FromTokenCera)
	return cmd
}
func TestBattlePassPremiumMultiWalletPriority(t *testing.T) {
	for _, tc := range []struct {
		name               string
		before, after      cerashop.Wallet
		happy, token, cera uint32
	}{
		{"欢乐代币券优先", cerashop.Wallet{10000, 20000, 100}, cerashop.Wallet{10000, 20000, 0}, 100, 0, 0},
		{"两种代币券够用不扣点券", cerashop.Wallet{30000, 60, 40}, cerashop.Wallet{30000, 0, 0}, 40, 60, 0},
		{"仅普通代币券100可购买", cerashop.Wallet{0, 100, 0}, cerashop.Wallet{}, 0, 100, 0},
		{"三钱包合付", cerashop.Wallet{80, 10, 10}, cerashop.Wallet{}, 10, 10, 80},
		{"仅点券100可购买", cerashop.Wallet{100, 0, 0}, cerashop.Wallet{}, 0, 0, 100},
		{"极大余额不溢出", cerashop.Wallet{math.MaxInt64, math.MaxInt64, math.MaxInt64}, cerashop.Wallet{math.MaxInt64, math.MaxInt64, math.MaxInt64 - 100}, 100, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, c, p, cmd := premiumFixture(t, 0)
			battleWallet(t, g, tc.before)
			cmd = walletQuote(cmd, tc.before)
			v := query(t, g, c, p, cmd)
			a, _, _ := g.Account.Load(nil, "a")
			if cerashop.WalletBalance(a) != tc.after || !v.State.Premium || v.State.PremiumFromCera != tc.cera || v.State.PremiumFromTokenCera != tc.token || v.State.PremiumFromHappyTokenCera != tc.happy {
				t.Fatal("扣款顺序或回执错误", cerashop.WalletBalance(a), v.State)
			}
			if v.CeraBalance != tc.after.Cera || v.TokenCeraBalance != tc.after.TokenCera || v.HappyTokenCeraBalance != tc.after.HappyTokenCera || a.Metadata["keep"] != "yes" {
				t.Fatal("快照或其他元数据错误")
			}
		})
	}
}
func TestBattlePassPremiumWalletChangeRequiresNewConfirmation(t *testing.T) {
	g, c, p, cmd := premiumFixture(t, 0)
	shown := cerashop.Wallet{0, 100, 0}
	battleWallet(t, g, shown)
	cmd = walletQuote(cmd, shown)
	changed := cerashop.Wallet{100, 90, 0}
	battleWallet(t, g, changed)
	if _, e := Execute(nil, g, c, p, cmd); !errors.Is(e, ErrQuote) {
		t.Fatal("报价后代币券减少，应重新确认而不多扣点券", e)
	}
	a, _, _ := g.Account.Load(nil, "a")
	if cerashop.WalletBalance(a) != changed || a.Metadata[MetadataKey] != "" {
		t.Fatal("过期扣款分配仍执行")
	}
	for _, quotes := range [][2]uint32{{101, 0}, {0, 101}, {100, 1}} {
		bad := cmd
		bad.QuoteHappy, bad.QuoteToken = quotes[0], quotes[1]
		if _, e := Execute(nil, g, c, p, bad); !errors.Is(e, ErrClaim) {
			t.Fatal("异常扣款分配", e)
		}
	}
	battleWallet(t, g, cerashop.Wallet{1, 97, 1})
	cmd.QuoteHappy, cmd.QuoteToken = 1, 97
	if _, e := Execute(nil, g, c, p, cmd); !errors.Is(e, ErrFunds) {
		t.Fatal("合计不足仍购买", e)
	}
}
func TestBattlePassPremiumMultiWalletRollbackAndConcurrency(t *testing.T) {
	initial := cerashop.Wallet{100, 10, 20}
	for _, failWrite := range []bool{false, true} {
		g, c, p, cmd := premiumFixture(t, 0)
		battleWallet(t, g, initial)
		cmd = walletQuote(cmd, initial)
		if failWrite {
			g.MailboxAssets = premiumFailUnit{g.MailboxAssets, g.MailboxAssets.(repo.AccountMailboxCharacterUnitOfWork)}
		} else {
			n := 0
			cmd.Validate = func() bool { n++; return n == 1 }
		}
		if _, e := Execute(nil, g, c, p, cmd); e == nil {
			t.Fatal("应当失败")
		}
		a, _, _ := g.Account.Load(nil, "a")
		if cerashop.WalletBalance(a) != initial || a.Metadata[MetadataKey] != "" {
			t.Fatal("三钱包及权益未一起回滚")
		}
	}
	g, c, p, cmd := premiumFixture(t, 0)
	battleWallet(t, g, initial)
	cmd = walletQuote(cmd, initial)
	var wg sync.WaitGroup
	var count atomic.Int32
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := Execute(nil, g, c, p, cmd)
			if e == nil {
				count.Add(1)
			} else if !errors.Is(e, ErrPremium) {
				t.Errorf("并发购买：%v", e)
			}
		}()
	}
	wg.Wait()
	a, _, _ := g.Account.Load(nil, "a")
	if count.Load() != 1 || cerashop.WalletBalance(a) != (cerashop.Wallet{Cera: 30}) {
		t.Fatal("并发重复扣款", count.Load(), cerashop.WalletBalance(a))
	}
}
