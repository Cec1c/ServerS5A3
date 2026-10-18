package cerashop

import (
	"errors"
	"math"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 代币券 / 欢乐代币券必须能落成钱包余额，而不是背包里的一件"材料"。
// 这里守住券的道具号识别与入账语义（溢出、全有或全无）。

func TestVoucherKindOfRecognizesBothVouchers(t *testing.T) {
	cases := []struct {
		itemID int64
		want   VoucherKind
	}{
		{2681762, VoucherTokenCera},
		{2681917, VoucherHappyTokenCera},
		{0, VoucherNone},
		{2681762 - 1, VoucherNone},
		{2681762 + 1, VoucherNone},
		{2681917 + 1, VoucherNone},
		{100050, VoucherNone},
	}
	for _, testCase := range cases {
		if got := VoucherKindOf(testCase.itemID); got != testCase.want {
			t.Fatalf("VoucherKindOf(%d)=%v want=%v", testCase.itemID, got, testCase.want)
		}
		if IsVoucher(testCase.itemID) != (testCase.want != VoucherNone) {
			t.Fatalf("IsVoucher(%d) 与 VoucherKindOf 不一致", testCase.itemID)
		}
	}
	if VoucherItemID(VoucherTokenCera) != 2681762 || VoucherItemID(VoucherHappyTokenCera) != 2681917 ||
		VoucherItemID(VoucherNone) != 0 {
		t.Fatal("VoucherItemID 与 VoucherKindOf 不是同一套映射")
	}
	if ids := VoucherItemIDs(); len(ids) != 2 || !IsVoucher(ids[0]) || !IsVoucher(ids[1]) {
		t.Fatalf("VoucherItemIDs=%v", ids)
	}
}

func TestCreditVoucherCountsAddsBothWallets(t *testing.T) {
	account := &dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{"account_cera": "700", "account_token_cera": "5"},
	}
	wallet, err := CreditVoucherCounts(account, VoucherCounts{TokenCera: 100, HappyTokenCera: 250})
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Cera != 700 || wallet.TokenCera != 105 || wallet.HappyTokenCera != 250 {
		t.Fatalf("钱包=%+v want cera=700 token=105 happy=250", wallet)
	}
	// 点券字段绝不能被券的入账碰到。
	if got := Balance(*account); got != 700 {
		t.Fatalf("点券被改动了: %d", got)
	}
}

// 数量为 0 时是空操作，不该写任何键（否则会给账号留下无意义的新元数据）。
func TestCreditVoucherCountsIgnoresZeroAndNegative(t *testing.T) {
	account := &dnfrepo.AccountRecord{AccountID: "account-1"}
	wallet, err := CreditVoucherCounts(account, VoucherCounts{})
	if err != nil || wallet.TokenCera != 0 || wallet.HappyTokenCera != 0 {
		t.Fatalf("空入账 err=%v wallet=%+v", err, wallet)
	}
	if len(account.Metadata) != 0 {
		t.Fatalf("空入账不该写元数据: %v", account.Metadata)
	}
	// 负数只可能来自上游 bug，按 0 处理而不是把余额扣下去。
	if _, err := CreditVoucherCounts(account, VoucherCounts{TokenCera: -50, HappyTokenCera: -1}); err != nil {
		t.Fatal(err)
	}
	if wallet := WalletBalance(*account); wallet.TokenCera != 0 || wallet.HappyTokenCera != 0 {
		t.Fatalf("负数不该扣余额: %+v", wallet)
	}
}

// 溢出必须整笔拒绝，且**一个字段都不写**（全有或全无）——否则会出现
// "代币券溢出失败、欢乐代币券却已经进账"的半截状态。
func TestCreditVoucherCountsRefusesOverflowAtomically(t *testing.T) {
	account := &dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{"account_token_cera": "2147483647"},
	}
	_, err := CreditVoucherCounts(account, VoucherCounts{TokenCera: 1, HappyTokenCera: 100})
	if !errors.Is(err, ErrWalletOverflow) {
		t.Fatalf("代币券溢出应返回 ErrWalletOverflow，实际=%v", err)
	}
	if wallet := WalletBalance(*account); wallet.TokenCera != math.MaxInt32 || wallet.HappyTokenCera != 0 {
		t.Fatalf("溢出时不该写任何字段: %+v", wallet)
	}

	// 边界：刚好填满到上限是允许的（不是溢出）。
	account = &dnfrepo.AccountRecord{AccountID: "account-1"}
	wallet, err := CreditVoucherCounts(account, VoucherCounts{TokenCera: math.MaxInt32})
	if err != nil || wallet.TokenCera != math.MaxInt32 {
		t.Fatalf("刚好到上限应当允许: err=%v wallet=%+v", err, wallet)
	}
	// 再 +1 就溢出。
	if _, err := CreditVoucherCounts(account, VoucherCounts{TokenCera: 1}); !errors.Is(err, ErrWalletOverflow) {
		t.Fatalf("超过上限应溢出，实际=%v", err)
	}
}

// nil 账号必须报错而不是 panic：调用方多为事务回调，panic 会连累整条连接。
func TestCreditVoucherCountsRejectsNilAccount(t *testing.T) {
	if _, err := CreditVoucherCounts(nil, VoucherCounts{TokenCera: 1}); err == nil {
		t.Fatal("nil 账号应报错")
	}
}

func TestVoucherCountsAddAndIsZero(t *testing.T) {
	if !(VoucherCounts{}).IsZero() {
		t.Fatal("空 counts 应当是 zero")
	}
	if (VoucherCounts{TokenCera: 1}).IsZero() {
		t.Fatal("非空 counts 不该是 zero")
	}
	sum := VoucherCounts{TokenCera: 3}.Add(VoucherCounts{TokenCera: 4, HappyTokenCera: 5})
	if sum.TokenCera != 7 || sum.HappyTokenCera != 5 {
		t.Fatalf("Add=%+v", sum)
	}
}
