package cerashop

import (
	"errors"
	"fmt"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 商城的钱包扣费策略。
//
// 现场背景：客户端**只上报"支付方式 0 = 点券"**，没有任何"用代币券买"的选项
// （见 dnfbridge 的 currentCeraShopPaymentMode*）。也就是说代币券 / 欢乐代币券
// 能不能花掉，完全由服务端决定 —— 服务端不主动从这两个钱包扣，它们就是一组
// 只能看不能用的数字（这正是现场 2026-09-19 抱怨的另一半）。
//
// 所以这里实现官方的"瀑布"语义：欢乐代币券 → 代币券 → 点券，依次扣到够为止。
type PayPolicy int

const (
	// PayPolicyCeraOnly 只扣点券，**这是零值**。
	//
	// 零值必须等于改动前的行为：既有调用方一行都不用改，也不会因为漏传字段而
	// 悄悄换一种扣费方式（Go 里"新增 bool 字段默认 false"就是这种陷阱）。
	PayPolicyCeraOnly PayPolicy = iota
	// PayPolicyWaterfall 欢乐代币券 → 代币券 → 点券，依次扣。
	PayPolicyWaterfall
	// PayPolicyTokenCeraOnly 只扣欢乐代币券 + 代币券，不动点券。
	// 对应客户端支持、而本服 PVF 尚未配置的 `[buy only cera point]` 商品；
	// 余额不够就整笔失败。
	PayPolicyTokenCeraOnly
)

// ErrWalletInsufficient 表示按给定策略扣不动这笔钱。
//
// 与 ErrCeraInsufficient（只针对点券）分开：调用方需要区分"点券不够"和
// "代币券+点券都不够"，日志里才看得清玩家到底缺哪种钱。
var ErrWalletInsufficient = errors.New("cera shop: wallet balance is insufficient for the payment policy")

// WalletPayment 是一次结账从各钱包实际扣掉的数量。
type WalletPayment struct {
	FromHappyTokenCera int64
	FromTokenCera      int64
	FromCera           int64
}

// Total 是这次结账各钱包扣费之和。
func (p WalletPayment) Total() int64 {
	return p.FromHappyTokenCera + p.FromTokenCera + p.FromCera
}

// IsZero 表示没有从任何钱包扣钱。
func (p WalletPayment) IsZero() bool {
	return p.FromHappyTokenCera == 0 && p.FromTokenCera == 0 && p.FromCera == 0
}

// String 便于日志与断言（例如 "happy=100 token=50 cera=0"）。
func (p WalletPayment) String() string {
	return fmt.Sprintf("happy=%d token=%d cera=%d", p.FromHappyTokenCera, p.FromTokenCera, p.FromCera)
}

// PlanWalletPayment 按策略算出各钱包要扣多少，**不改动传入的钱包**。
//
// 返回的 WalletPayment 保证 Total() == cost（成功时）；失败时返回零值与错误。
// 这是一个纯函数，方便把"扣费顺序"这类最容易出错的逻辑单独钉死。
func PlanWalletPayment(wallet Wallet, cost int64, policy PayPolicy) (WalletPayment, error) {
	if cost < 0 {
		return WalletPayment{}, fmt.Errorf("%w: negative cost %d", ErrWalletInsufficient, cost)
	}
	if cost == 0 {
		return WalletPayment{}, nil
	}
	// 负数余额只可能来自脏数据；当成 0，绝不让它把扣费算成"多扣"。
	happy := wallet.HappyTokenCera
	if happy < 0 {
		happy = 0
	}
	token := wallet.TokenCera
	if token < 0 {
		token = 0
	}
	cera := wallet.Cera
	if cera < 0 {
		cera = 0
	}

	var payment WalletPayment
	remaining := cost

	// 两种非"纯点券"的策略都先花欢乐代币券：它是活动货币，优先级最高。
	if policy == PayPolicyWaterfall || policy == PayPolicyTokenCeraOnly {
		payment.FromHappyTokenCera = minInt64(remaining, happy)
		remaining -= payment.FromHappyTokenCera
		if remaining > 0 {
			payment.FromTokenCera = minInt64(remaining, token)
			remaining -= payment.FromTokenCera
		}
	}
	if policy == PayPolicyWaterfall && remaining > 0 {
		payment.FromCera = minInt64(remaining, cera)
		remaining -= payment.FromCera
	}
	// 未知策略一律按"只扣点券"处理，绝不猜一种更宽松的扣法。
	if policy != PayPolicyWaterfall && policy != PayPolicyTokenCeraOnly {
		payment = WalletPayment{}
		remaining = cost
		payment.FromCera = minInt64(remaining, cera)
		remaining -= payment.FromCera
	}
	if remaining > 0 {
		return WalletPayment{}, fmt.Errorf("%w: policy=%d cost=%d wallet=%+v", ErrWalletInsufficient, policy, cost, wallet)
	}
	return payment, nil
}

// ApplyWalletPayment 把扣费方案写回账号钱包，并返回扣费后的钱包。
//
// 调用方负责保存账号记录（本函数只改内存里的记录，与仓库无关，便于在事务回调里用）。
// 每个字段都夹到 >= 0：方案本身已经保证不会扣超，这里只是最后一道防线。
func ApplyWalletPayment(account *dnfrepo.AccountRecord, payment WalletPayment) Wallet {
	if account == nil {
		return Wallet{}
	}
	wallet := WalletBalance(*account)
	SetHappyTokenCeraBalance(account, wallet.HappyTokenCera-payment.FromHappyTokenCera)
	SetTokenCeraBalance(account, wallet.TokenCera-payment.FromTokenCera)
	SetBalance(account, wallet.Cera-payment.FromCera)
	return WalletBalance(*account)
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
