package guild

import (
	"context"
	"sort"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func (o *HongbaoOwner) View(ctx context.Context, owner r.GuildOwner, now time.Time) (HongbaoView, error) {
	var view HongbaoView
	if o == nil || !validHongbaoOwner(owner) || now.IsZero() {
		return view, ErrHongbaoRequest
	}
	actor, found, err := o.repos.Character.Load(ctx, owner.CharacterID)
	if err != nil {
		return view, err
	}
	if !found || actor.CharacterID != owner.CharacterID || actor.AccountID != owner.AccountID || actor.Stats["delete_flag"] != 0 {
		return view, ErrCharacterNotFound
	}
	account, found, err := o.repos.Account.Load(ctx, owner.AccountID)
	if err != nil {
		return view, err
	}
	if !found || account.AccountID != owner.AccountID {
		return view, ErrCharacterNotFound
	}
	reg, err := Load(ctx, o.repos.Settings)
	if err != nil {
		return view, err
	}
	g, _, found := reg.GuildForOwner(owner)
	if !found {
		return view, ErrNotMember
	}
	view.GuildID, view.MemberCount = g.ID, uint32(len(g.Members))
	view.PurchasedToday, err = hongbaoDayValue(actor.Stats[hongbaoPurchaseDay], actor.Stats[hongbaoPurchaseCount], now, o.contract.Catalog.purchaseLimit)
	if err != nil {
		return HongbaoView{}, err
	}
	view.TakenToday, err = hongbaoAccountCount(account, now, o.contract.Catalog.claimLimit)
	if err != nil {
		return HongbaoView{}, err
	}
	if reg.Hongbao != nil {
		for _, h := range reg.Hongbao.Records {
			if h.GuildID == g.ID && h.visible(now) && !now.Before(h.CreatedAt) {
				view.Records = append(view.Records, h.copy())
			}
		}
	}
	sort.Slice(view.Records, func(i, j int) bool { return view.Records[i].ID > view.Records[j].ID })
	if len(view.Records) > HongbaoVisibleLimit {
		view.Records = view.Records[:HongbaoVisibleLimit]
	}
	return view, nil
}
func (o *HongbaoOwner) History(ctx context.Context, owner r.GuildOwner, id uint32, now time.Time) ([]HongbaoClaimEntry, error) {
	view, err := o.View(ctx, owner, now)
	if err != nil {
		return nil, err
	}
	for _, h := range view.Records {
		if h.ID == id {
			return append([]HongbaoClaimEntry(nil), h.Claims...), nil
		}
	}
	return nil, ErrHongbaoUnavailable
}
