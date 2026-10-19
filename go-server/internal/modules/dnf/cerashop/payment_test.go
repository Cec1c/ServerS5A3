package cerashop

import (
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 支付瀑布：欢乐代币券 → 代币券 → 点券。
//
// 客户端只上报"支付方式 0 = 点券"，没有"用代币券买"的选项，所以代币券能不能花掉
// 完全由服务端决定。这一组测试把"扣费顺序"钉死——它是最容易写错、也最容易
// 悄悄扣错钱包的地方。

func TestPlanWalletPaymentCeraOnlyIsTheZeroValue(t *testing.T) {
	wallet := Wallet{Cera: 100, TokenCera: 500, HappyTokenCera: 500}
	// 零值必须是"只扣点券"：既有调用方一行不改也不该换扣费方式。
	var policy PayPolicy
	if policy != PayPolicyCeraOnly {
		t.Fatalf("PayPolicy 零值=%v want PayPolicyCeraOnly", policy)
	}
	payment, err := PlanWalletPayment(wallet, 80, policy)
	if err != nil {
		t.Fatal(err)
	}
	if payment.FromCera != 80 || payment.FromTokenCera != 0 || payment.FromHappyTokenCera != 0 {
		t.Fatalf("只扣点券模式动了别的钱包: %s", payment)
	}
	// 点券不够就该失败，而不是把代币券也花了。
	if _, err := PlanWalletPayment(wallet, 101, PayPolicyCeraOnly); !errors.Is(err, ErrWalletInsufficient) {
		t.Fatalf("点券不够应失败: %v", err)
	}
}

func TestPlanWalletPaymentWaterfallSpendsInOrder(t *testing.T) {
	cases := []struct {
		name    string
		wallet  Wallet
		cost    int64
		want    WalletPayment
		wantErr bool
	}{
		{
			name:   "happy_covers_all",
			wallet: Wallet{Cera: 999, TokenCera: 999, HappyTokenCera: 100},
			cost:   40,
			want:   WalletPayment{FromHappyTokenCera: 40},
		},
		{
			name:   "happy_exhausted_then_token",
			wallet: Wallet{Cera: 999, TokenCera: 100, HappyTokenCera: 30},
			cost:   50,
			want:   WalletPayment{FromHappyTokenCera: 30, FromTokenCera: 20},
		},
		{
			name:   "happy_and_token_exhausted_then_cera",
			wallet: Wallet{Cera: 999, TokenCera: 20, HappyTokenCera: 10},
			cost:   100,
			want:   WalletPayment{FromHappyTokenCera: 10, FromTokenCera: 20, FromCera: 70},
		},
		{
			name:   "exactly_enough_across_all_three",
			wallet: Wallet{Cera: 70, TokenCera: 20, HappyTokenCera: 10},
			cost:   100,
			want:   WalletPayment{FromHappyTokenCera: 10, FromTokenCera: 20, FromCera: 70},
		},
		{
			name:    "one_short",
			wallet:  Wallet{Cera: 69, TokenCera: 20, HappyTokenCera: 10},
			cost:    100,
			wantErr: true,
		},
		{
			name:   "zero_cera_still_payable_with_token",
			wallet: Wallet{Cera: 0, TokenCera: 300, HappyTokenCera: 0},
			cost:   250,
			want:   WalletPayment{FromTokenCera: 250},
		},
		{
			name:   "cost_zero_is_a_no_op",
			wallet: Wallet{Cera: 5, TokenCera: 5, HappyTokenCera: 5},
			cost:   0,
			want:   WalletPayment{},
		},
		{
			// 负数余额只可能来自脏数据，当成 0，绝不能让扣费算出"多扣"。
			name:   "negative_balances_treated_as_zero",
			wallet: Wallet{Cera: 100, TokenCera: -50, HappyTokenCera: -50},
			cost:   100,
			want:   WalletPayment{FromCera: 100},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payment, err := PlanWalletPayment(testCase.wallet, testCase.cost, PayPolicyWaterfall)
			if testCase.wantErr {
				if !errors.Is(err, ErrWalletInsufficient) {
					t.Fatalf("期望余额不足，实际 err=%v payment=%s", err, payment)
				}
				if !payment.IsZero() {
					t.Fatalf("失败时必须返回零方案，绝不部分扣费: %s", payment)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if payment != testCase.want {
				t.Fatalf("payment=%s want=%s", payment, testCase.want)
			}
			if payment.Total() != testCase.cost {
				t.Fatalf("扣费总额=%d want=%d", payment.Total(), testCase.cost)
			}
		})
	}
}

// `[buy only cera point]` 商品：不能动点券。
func TestPlanWalletPaymentTokenCeraOnlyNeverTouchesCera(t *testing.T) {
	wallet := Wallet{Cera: 100000, TokenCera: 30, HappyTokenCera: 10}
	payment, err := PlanWalletPayment(wallet, 40, PayPolicyTokenCeraOnly)
	if err != nil {
		t.Fatal(err)
	}
	if payment.FromCera != 0 || payment.FromHappyTokenCera != 10 || payment.FromTokenCera != 30 {
		t.Fatalf("不该动点券: %s", payment)
	}
	// 券不够时失败，即使点券充足。
	if _, err := PlanWalletPayment(wallet, 41, PayPolicyTokenCeraOnly); !errors.Is(err, ErrWalletInsufficient) {
		t.Fatalf("券不够应失败（点券再多也不能用）: %v", err)
	}
}

// 未知策略一律按"只扣点券"处理：绝不猜一种更宽松的扣法。
func TestPlanWalletPaymentUnknownPolicyFallsBackToCeraOnly(t *testing.T) {
	wallet := Wallet{Cera: 100, TokenCera: 500, HappyTokenCera: 500}
	payment, err := PlanWalletPayment(wallet, 60, PayPolicy(99))
	if err != nil {
		t.Fatal(err)
	}
	if payment.FromCera != 60 || payment.FromTokenCera != 0 || payment.FromHappyTokenCera != 0 {
		t.Fatalf("未知策略不该动代币券钱包: %s", payment)
	}
}

func TestPlanWalletPaymentRejectsNegativeCost(t *testing.T) {
	if _, err := PlanWalletPayment(Wallet{Cera: 100}, -1, PayPolicyWaterfall); !errors.Is(err, ErrWalletInsufficient) {
		t.Fatalf("负成本应失败: %v", err)
	}
}

func TestApplyWalletPaymentWritesOnlyThePlannedFields(t *testing.T) {
	account := &dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{}}
	SetBalance(account, 700)
	SetTokenCeraBalance(account, 100)
	SetHappyTokenCeraBalance(account, 50)
	after := ApplyWalletPayment(account, WalletPayment{FromHappyTokenCera: 50, FromTokenCera: 20, FromCera: 30})
	want := Wallet{Cera: 670, TokenCera: 80, HappyTokenCera: 0}
	if after != want {
		t.Fatalf("after=%+v want=%+v", after, want)
	}
	if wallet := WalletBalance(*account); wallet != want {
		t.Fatalf("账号记录=%+v want=%+v", wallet, want)
	}
	// nil 账号不该 panic（调用方多在事务回调里，panic 会连累整条连接）。
	if got := ApplyWalletPayment(nil, WalletPayment{FromCera: 1}); got != (Wallet{}) {
		t.Fatalf("nil 账号应返回零钱包: %+v", got)
	}
}
