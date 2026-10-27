package guild

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const EssenceCatalogPath = "etc/agitwar.etc"

var ErrEssenceState = errors.New("invalid guild essence state or catalog")

// The first four AP sources are supported by the matched PVF and June 2017
// rules. AP[4] and all GP events remain unassigned. No client-supplied index.
type EssenceEvent byte

const (
	EssenceLogin EssenceEvent = iota
	EssenceRecommendedClear
	EssenceGuildClear
	EssencePartyGuildClear
)

type EssenceCatalog struct {
	dailyMax uint32
	ap       [4]uint32
}

func LoadEssenceCatalog(source pvf.Source) (*EssenceCatalog, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText(EssenceCatalogPath)
	if err != nil {
		return nil, err
	}
	return ParseEssenceCatalog(text)
}

func ParseEssenceCatalog(text string) (*EssenceCatalog, error) {
	doc, err := pvf.Parse(EssenceCatalogPath, text)
	if err != nil {
		return nil, err
	}
	c := &EssenceCatalog{}
	seen := map[string]bool{}
	for _, s := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(s.Name))
		if name != "daily max ap" && name != "ap reward" {
			continue
		}
		if seen[name] {
			return nil, ErrEssenceState
		}
		seen[name] = true
		tokens := doc.Tokens[s.Start:s.End]
		count := 1
		if name == "ap reward" {
			count = 5
		}
		if len(tokens) != count {
			return nil, ErrEssenceState
		}
		for i, token := range tokens {
			if token.Kind != pvf.TokenInt || token.Int < 0 || token.Int > math.MaxInt32 {
				return nil, ErrEssenceState
			}
			if name == "daily max ap" {
				c.dailyMax = uint32(token.Int)
			} else if i < 4 {
				c.ap[i] = uint32(token.Int)
			}
		}
	}
	if !seen["daily max ap"] || !seen["ap reward"] || c.dailyMax == 0 {
		return nil, ErrEssenceState
	}
	return c, nil
}

type EssenceBalance struct {
	AP uint32 `json:"ap"`
	GP uint32 `json:"gp"`
}

type EssenceIncome struct {
	GuildID uint32
	AP      uint32
}

const essenceDayEnd = "guild_essence_day_end"
const essenceDayAP = "guild_essence_day_ap"
const essenceDayLogin = "guild_essence_day_login"

// ApplyEssenceIncome is called inside a guild/character transaction, after the
// permanent dungeon completion receipt check. The caller saves the character
// in the SAME transaction. Daily accounting belongs to the character and thus
// survives leaving, disbanding and rejoining a guild.
func ApplyEssenceIncome(ctx context.Context, settings r.SettingsRepository, actor *r.CharacterRecord, c *EssenceCatalog, event EssenceEvent, now time.Time) (EssenceIncome, error) {
	if actor == nil || c == nil || c.dailyMax == 0 || event > EssencePartyGuildClear || now.IsZero() {
		return EssenceIncome{}, ErrEssenceState
	}
	reg, err := Load(ctx, settings)
	if err != nil {
		return EssenceIncome{}, err
	}
	g, _, found := reg.GuildForOwner(r.GuildOwner{AccountID: actor.AccountID, CharacterID: actor.CharacterID})
	if !found {
		return EssenceIncome{}, nil
	}
	end := itemexpiration.NextDailyDeleteAt(now).Unix()
	oldEnd, earned, login := actor.Stats[essenceDayEnd], actor.Stats[essenceDayAP], actor.Stats[essenceDayLogin]
	if oldEnd < 0 || oldEnd > end || earned < 0 || earned > math.MaxInt32 || login < 0 || login > 1 || (oldEnd == 0 && (earned != 0 || login != 0)) {
		return EssenceIncome{}, ErrEssenceState
	}
	if oldEnd != end {
		earned, login = 0, 0
	}
	if event == EssenceLogin && login == 1 {
		return EssenceIncome{GuildID: g.ID}, nil
	}
	amount := c.ap[event]
	if earned >= int64(c.dailyMax) {
		amount = 0
	} else {
		amount = min(amount, c.dailyMax-uint32(earned))
	}
	if g.Essence.AP > math.MaxUint32-amount {
		return EssenceIncome{}, ErrEssenceState
	}
	if actor.Stats == nil {
		actor.Stats = map[string]int64{}
	}
	actor.Stats[essenceDayEnd], actor.Stats[essenceDayAP] = end, earned+int64(amount)
	if event == EssenceLogin {
		login = 1
	}
	actor.Stats[essenceDayLogin] = login
	if amount != 0 {
		g.Essence.AP += amount
		g.UpdatedAt = now
		if err := Save(ctx, settings, reg, now); err != nil {
			return EssenceIncome{}, err
		}
	}
	return EssenceIncome{GuildID: g.ID, AP: amount}, nil
}

// Login uses the character's day marker for durable idempotency. Revalidate
// checks the canonical session before reads and after both durable writes.
func GrantEssenceLogin(ctx context.Context, uow r.GuildUnitOfWork, owner r.GuildOwner, c *EssenceCatalog, now time.Time, revalidate func() bool) (EssenceIncome, error) {
	if uow == nil || revalidate == nil {
		return EssenceIncome{}, ErrRepositoryUnavailable
	}
	var result EssenceIncome
	err := uow.WithinGuild(ctx, []r.GuildOwner{owner}, func(tx r.Group) error {
		if !revalidate() {
			return ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.AccountID != owner.AccountID || actor.CharacterID != owner.CharacterID {
			return ErrCharacterNotFound
		}
		actor = r.CloneCharacter(actor)
		result, err = ApplyEssenceIncome(ctx, tx.Settings, &actor, c, EssenceLogin, now)
		if err != nil {
			return err
		}
		if result.GuildID != 0 {
			actor.UpdatedAt = now
			if err = r.SaveCharacterFields(ctx, tx.Character, actor, r.CharacterFieldStats); err != nil {
				return err
			}
		}
		if !revalidate() {
			return ErrCharacterNotFound
		}
		return nil
	})
	if err != nil {
		return EssenceIncome{}, err
	}
	return result, nil
}
