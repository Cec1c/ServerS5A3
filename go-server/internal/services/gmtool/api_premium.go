package gmtool

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

var gmPremiumNames = map[int64]string{
	premium.TypeBlackDiamond: "黑钻贵族",
	premium.TypeOverEquip:    "霸王契约",
	premium.TypeOverSkill:    "达人契约",
	premium.TypeBonusExp:     "成长契约",
	premium.TypeCrystal:      "晶之契约",
	premium.TypeLethe:        "遗忘河水契约",
}

func (s *Server) premiumTypes() ([]int64, error) {
	if s.deps.PremiumTypes == nil {
		return nil, fmt.Errorf("契约目录尚未就绪")
	}
	return s.deps.PremiumTypes()
}

func (s *Server) handlePremiumStatus(w http.ResponseWriter, r *http.Request) {
	ctx, group, _, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	types, err := s.premiumTypes()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	account, found, err := group.Account.Load(ctx, character.AccountID)
	if err != nil || !found {
		writeFailure(w, "读取契约账号失败")
		return
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	rows := make([]map[string]any, 0, len(types))
	seen := make(map[int64]bool)
	now := time.Now().UTC()
	for _, typ := range types {
		name, allowed := gmPremiumNames[typ]
		if !allowed || seen[typ] {
			continue
		}
		seen[typ] = true
		rows = append(rows, map[string]any{"type": typ, "name": name, "expireAt": premium.ExpireAt(account, typ), "active": premium.Active(account, typ, now)})
	}
	writeOK(w, map[string]any{"contracts": rows, "accountShared": true, "reloginRequired": true})
}

func (s *Server) handlePremiumGrant(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody[struct {
		Type      int64  `json:"type"`
		Days      int64  `json:"days"`
		RequestID string `json:"requestId"`
	}](w, r)
	if !ok {
		return
	}
	ctx, group, _, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	types, err := s.premiumTypes()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	allowed := false
	for _, typ := range types {
		if typ == body.Type {
			allowed = true
			break
		}
	}
	name, known := gmPremiumNames[body.Type]
	if !allowed || !known {
		writeFailure(w, "当前游戏资源未注册此契约")
		return
	}
	if group.RentalAssets == nil {
		writeFailure(w, "契约事务尚未就绪")
		return
	}
	var result premium.AdminGrantResult
	now := time.Now().UTC()
	err = group.RentalAssets.WithinRentalAssets(ctx, character.AccountID, character.CharacterID, func(accounts repo.AccountRepository, characters repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
		current, found, err := characters.Load(ctx, character.CharacterID)
		if err != nil {
			return err
		}
		if !found || current.AccountID != character.AccountID {
			return fmt.Errorf("角色所属账号已变化，请刷新后重试")
		}
		account, found, err := accounts.Load(ctx, character.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("契约账号不存在")
		}
		account = repo.CloneAccount(account)
		result, err = premium.GrantAdminContract(&account, character.CharacterID, body.Type, body.Days, strings.TrimSpace(body.RequestID), now)
		if err != nil {
			return err
		}
		if result.Replayed {
			return nil
		}
		account.UpdatedAt = now
		return accounts.Save(ctx, account)
	})
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	s.cfg.Logger("dnf90 gm premium grant", "account_id", character.AccountID, "character_id", character.CharacterID, "premium_type", body.Type, "days", body.Days, "expire_at", result.ExpireAt, "replayed", result.Replayed)
	writeOK(w, map[string]any{"name": name, "expireAt": result.ExpireAt, "replayed": result.Replayed, "accountShared": true, "reloginRequired": true})
}
