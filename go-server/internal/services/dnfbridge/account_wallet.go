package dnfbridge

import (
	"context"

	dnfcerashop "longheng.io/server/internal/modules/dnf/cerashop"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// currentAccountCera reads the account-shared Cera balance from account
// metadata. The balance lives in dnf_accounts.metadata_json["account_cera"]
// so every character on the account sees and spends the same pool; the
// per-character dnf_characters.cera column is legacy read-only data.
func currentAccountCera(account dnfrepo.AccountRecord) int64 {
	return dnfcerashop.Balance(account)
}

// setCurrentAccountCera writes the account-shared Cera balance back through
// the same metadata key. Negative inputs clamp to zero so a malformed caller
// can never produce a negative shared pool.
func setCurrentAccountCera(account *dnfrepo.AccountRecord, balance int64) {
	dnfcerashop.SetBalance(account, balance)
}

// loadCurrentAccountCera is the read-only display path for places that only
// need the current shared balance without joining a rental-assets mutation.
func (s *Service) loadCurrentAccountCera(ctx context.Context, repositories dnfrepo.Group, sessions ...*gameSession) int64 {
	if repositories.Account == nil {
		return 0
	}
	account, found, err := repositories.Account.Load(ctx, s.accountIDForSession(sessions...))
	if err != nil || !found {
		return 0
	}
	return currentAccountCera(account)
}

// 购买、领取及选角后均从同一账号钱包发送权威余额。
func (s *Service) sendCurrentAccountWallet(session *gameSession, onlyWithTokens ...bool) error {
	if s == nil || session == nil {
		return nil
	}
	r, ok := s.repositoryGroup()
	if !ok || r.Account == nil {
		return nil
	}
	account, found, err := r.Account.Load(context.Background(), s.accountIDForSession(session))
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	w := dnfcerashop.WalletBalance(account)
	if len(onlyWithTokens) > 0 && onlyWithTokens[0] && w.TokenCera == 0 && w.HappyTokenCera == 0 {
		return nil
	}
	return s.sendGameUpperRawClass(session, currentCeraShopUpdateMsgID, buildCurrentCeraShopWalletBody(w.Cera, w.TokenCera, w.HappyTokenCera), 0)
}
