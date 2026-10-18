// Package signupbenefits owns registration and character initialization grants.
// GM campaigns, scanners and bulk mail live in the independent DNF90GM repository.
package signupbenefits

import (
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
)

const MaxAmount int64 = math.MaxInt32
const WelcomeReceipt = "test_benefits_welcome_cera_v1"

type Policy struct {
	Revision      uint64 `json:"revision"`
	AccountCera   int64  `json:"accountCera"`
	CharacterGold int64  `json:"characterGold"`
}

func (p Policy) Validate() error {
	if p.AccountCera < 0 || p.AccountCera > MaxAmount || p.CharacterGold < 0 || p.CharacterGold > MaxAmount {
		return errors.New("金额必须为 0 至 2147483647 的整数；0 表示关闭")
	}
	return nil
}

// WelcomeCera is called only inside character creation's locked account
// transaction. The amount is captured at registration, never at later login.
func WelcomeCera(account *repository.AccountRecord, amount int64) error {
	if amount < 0 || amount > MaxAmount {
		return errors.New("invalid welcome cera")
	}
	if amount == 0 || account.Metadata[WelcomeReceipt] != "" {
		return nil
	}
	current := int64(0)
	if raw := account.Metadata["account_cera"]; raw != "" {
		var err error
		current, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || current < 0 {
			return errors.New("invalid account cera balance")
		}
	}
	if current > MaxAmount-amount {
		return errors.New("welcome cera exceeds wallet limit")
	}
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	account.Metadata["account_cera"] = strconv.FormatInt(current+amount, 10)
	account.Metadata[WelcomeReceipt] = strconv.FormatInt(amount, 10)
	return nil
}
