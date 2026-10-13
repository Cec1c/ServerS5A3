package gmtool

import (
	"context"
	"fmt"
	"net/http"
	"time"

	pvf "longheng.io/server/internal/modules/dnf/pvf"
	rental "longheng.io/server/internal/modules/dnf/rental"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func accountLuckyStars(ctx context.Context, group repo.Group, accountID string) (uint32, error) {
	if group.Account == nil {
		return 0, errRepositoriesUnavailable
	}
	record, found, err := group.Account.Load(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, errAccountMissing
	}
	return rental.Points(record)
}

func (s *Server) handleAccountLuckyStars(w http.ResponseWriter, ctx context.Context, group repo.Group, account webAccount, body accountCurrencyRequest) {
	if !s.requireAccountOffline(ctx, w, "account-lucky-star", group, account.AccountID, int(account.PanelID)) {
		return
	}
	source, err := s.source()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	text, err := source.ReadText(rental.SystemPVFPath)
	if err != nil {
		writeFailure(w, "读取幸运星上限失败: "+err.Error())
		return
	}
	doc, err := pvf.Parse(rental.SystemPVFPath, text)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	limit, err := rental.PointLimit(doc)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	if body.Value != nil && (*body.Value < 0 || *body.Value > int64(limit)) {
		writeFailure(w, fmt.Sprintf("幸运星必须在 0..%d 范围内，未做任何写入。", limit))
		return
	}
	characters, err := s.charactersForAccount(ctx, group, account.AccountID)
	if err != nil || len(characters) == 0 {
		writeFailure(w, "账号没有可验证归属的角色，无法设置幸运星。")
		return
	}
	owner, err := rental.NewOwner(group)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	result, err := owner.UpdatePoints(ctx, rental.UpdatePointsCommand{AccountID: account.AccountID, CharacterID: characters[0].CharacterID, Value: body.Value, Amount: body.Amount, Limit: limit, UpdatedAt: time.Now().UTC()})
	if err != nil {
		writeFailure(w, fmt.Sprintf("设置幸运星失败（范围 0..%d）: %v", limit, err))
		return
	}
	s.logEvent("gm-account-lucky-star", "account_id", account.AccountID, "value", result.Points, "limit", limit)
	writeOK(w, map[string]any{"accountId": account.PanelID, "type": "luckystar", "luckyStar": result.Points, "limit": limit, "hint": "幸运星保存在账号上；请重新登录以刷新游戏内余额。"})
}
