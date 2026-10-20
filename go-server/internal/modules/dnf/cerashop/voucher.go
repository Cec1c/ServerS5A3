// 代币券道具号经现用PVF核对；邮件领取在账号与邮件共用的事务内转入钱包。
package cerashop

import (
	"errors"
	"math"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// TokenCeraVoucherItemID 是 `代币券`（trade_point.stk）。
	TokenCeraVoucherItemID int64 = 2681762
	// HappyTokenCeraVoucherItemID 是 `欢乐代币券`（trade_point_event.stk）。
	HappyTokenCeraVoucherItemID int64 = 2681917
)

// MaxWalletBalance 是三个钱包字段的上限。
//
// 客户端把这三个值按 **有符号 32 位** 读进字段（反编译 `class0/0x0035` 的 handler
// `FUN_01d83c20` 读的是 i32），所以上限必须是 MaxInt32 而不是 MaxInt64：
// 超过就会在客户端显示成负数。所有入账都必须在这里挡住。
const MaxWalletBalance int64 = math.MaxInt32

// ErrWalletOverflow 表示入账会让钱包超过 MaxWalletBalance。
// 调用方应该把它映射成自己模块的"请求非法"错误，而不是静默截断。
var ErrWalletOverflow = errors.New("cera shop: wallet balance would exceed the client limit")

// VoucherKind 是券对应的钱包字段。
type VoucherKind int

const (
	// VoucherNone 表示这件道具不是券。
	VoucherNone VoucherKind = iota
	// VoucherTokenCera 是代币券，入账到 Wallet.TokenCera。
	VoucherTokenCera
	// VoucherHappyTokenCera 是欢乐代币券，入账到 Wallet.HappyTokenCera。
	VoucherHappyTokenCera
)

// VoucherKindOf 判断一件道具是不是券。不是券返回 VoucherNone。
func VoucherKindOf(itemID int64) VoucherKind {
	switch itemID {
	case TokenCeraVoucherItemID:
		return VoucherTokenCera
	case HappyTokenCeraVoucherItemID:
		return VoucherHappyTokenCera
	default:
		return VoucherNone
	}
}

// IsVoucher 是上面的布尔简写，给"这批奖励里有没有券"这类判断用。
func IsVoucher(itemID int64) bool {
	return VoucherKindOf(itemID) != VoucherNone
}

// VoucherItemID 返回某类券的道具号（VoucherNone 返回 0）。
func VoucherItemID(kind VoucherKind) int64 {
	switch kind {
	case VoucherTokenCera:
		return TokenCeraVoucherItemID
	case VoucherHappyTokenCera:
		return HappyTokenCeraVoucherItemID
	default:
		return 0
	}
}

// VoucherItemIDs 返回全部券道具号，供"发放前先摘出来"的过滤使用。
func VoucherItemIDs() []int64 {
	return []int64{TokenCeraVoucherItemID, HappyTokenCeraVoucherItemID}
}

// VoucherCounts 是一批待入账的券数量。
type VoucherCounts struct {
	TokenCera      int64
	HappyTokenCera int64
}

// IsZero 表示没有任何券需要入账。
func (c VoucherCounts) IsZero() bool {
	return c.TokenCera == 0 && c.HappyTokenCera == 0
}

// Add 累加另一种券的数量。
func (c VoucherCounts) Add(other VoucherCounts) VoucherCounts {
	return VoucherCounts{
		TokenCera:      c.TokenCera + other.TokenCera,
		HappyTokenCera: c.HappyTokenCera + other.HappyTokenCera,
	}
}

// CreditVoucherCounts 把券数量累加到账号钱包。
//
// 调用方负责把账号保存与奖励领取状态放在同一事务中。
//
//   - **全有或全无**：任何一项溢出都不写任何字段，账号记录保持原样，返回 ErrWalletOverflow。
//   - 调用方负责保存 account 与它的 UpdatedAt。
//   - 负数一律当 0：券数量为负只可能来自上游 bug，不该把余额扣下去。
func CreditVoucherCounts(account *dnfrepo.AccountRecord, counts VoucherCounts) (Wallet, error) {
	if account == nil {
		return Wallet{}, errors.New("cera shop: account is required")
	}
	tokenCera := counts.TokenCera
	if tokenCera < 0 {
		tokenCera = 0
	}
	happyTokenCera := counts.HappyTokenCera
	if happyTokenCera < 0 {
		happyTokenCera = 0
	}
	if tokenCera == 0 && happyTokenCera == 0 {
		return WalletBalance(*account), nil
	}
	wallet := WalletBalance(*account)
	if tokenCera > MaxWalletBalance-wallet.TokenCera {
		return wallet, ErrWalletOverflow
	}
	if happyTokenCera > MaxWalletBalance-wallet.HappyTokenCera {
		return wallet, ErrWalletOverflow
	}
	if tokenCera > 0 {
		SetTokenCeraBalance(account, wallet.TokenCera+tokenCera)
	}
	if happyTokenCera > 0 {
		SetHappyTokenCeraBalance(account, wallet.HappyTokenCera+happyTokenCera)
	}
	return WalletBalance(*account), nil
}
