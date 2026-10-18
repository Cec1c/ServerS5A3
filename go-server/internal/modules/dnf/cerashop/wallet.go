package cerashop

import (
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"strings"
)

const (
	accountTokenCeraMetadataKey      = "account_token_cera"
	accountHappyTokenCeraMetadataKey = "account_happy_token_cera"
)

type Wallet struct{ Cera, TokenCera, HappyTokenCera int64 }

func TokenCeraBalance(account dnfrepo.AccountRecord) int64 {
	return metadataBalance(account, accountTokenCeraMetadataKey)
}

func HappyTokenCeraBalance(account dnfrepo.AccountRecord) int64 {
	return metadataBalance(account, accountHappyTokenCeraMetadataKey)
}

func WalletBalance(account dnfrepo.AccountRecord) Wallet {
	return Wallet{
		Cera:           Balance(account),
		TokenCera:      TokenCeraBalance(account),
		HappyTokenCera: HappyTokenCeraBalance(account),
	}
}

func metadataBalance(account dnfrepo.AccountRecord, key string) int64 {
	raw := strings.TrimSpace(account.Metadata[key])
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func SetTokenCeraBalance(account *dnfrepo.AccountRecord, balance int64) {
	setMetadataBalance(account, accountTokenCeraMetadataKey, balance)
}

func SetHappyTokenCeraBalance(account *dnfrepo.AccountRecord, balance int64) {
	setMetadataBalance(account, accountHappyTokenCeraMetadataKey, balance)
}

func setMetadataBalance(account *dnfrepo.AccountRecord, key string, balance int64) {
	if account == nil {
		return
	}
	if account.Metadata == nil {
		account.Metadata = make(map[string]string, 4)
	}
	if balance < 0 {
		balance = 0
	}
	account.Metadata[key] = strconv.FormatInt(balance, 10)
}
