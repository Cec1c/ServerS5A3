package guild

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrInvalidAppearance = errors.New("invalid guild appearance")
var ErrInsufficientMileage = errors.New("insufficient guild mileage")

const NoGuildAura = uint32(math.MaxUint32)

type AppearanceCatalog struct {
	Marks      uint32
	FreeMarks  uint32
	MarkPrice  uint32
	AuraMarks  uint32
	AuraFrames uint32
}

type AppearanceChange struct {
	Owner               repo.GuildOwner
	Mark                byte
	Aura                bool
	AuraMark, AuraFrame uint32
	Revalidate          func() bool
}

// ChangeAppearance keeps the guild charge and appearance in one transaction.
// Revalidate after the durable write as well, before the UoW commits.
func ChangeAppearance(ctx context.Context, uow repo.GuildUnitOfWork, change AppearanceChange, catalog AppearanceCatalog, now time.Time) (uint32, error) {
	if uow == nil || change.Revalidate == nil {
		return 0, ErrRepositoryUnavailable
	}
	var id uint32
	err := uow.WithinGuild(ctx, []repo.GuildOwner{change.Owner}, func(tx repo.Group) error {
		if !change.Revalidate() {
			return ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, change.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.CharacterID != change.Owner.CharacterID || actor.AccountID != change.Owner.AccountID {
			return ErrCharacterNotFound
		}
		r, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		var g *Guild
		if change.Aura {
			g, err = r.SetAura(change.Owner, change.AuraMark, change.AuraFrame, catalog, now)
		} else {
			g, err = r.SetMark(change.Owner, change.Mark, catalog, now)
		}
		if err != nil {
			return err
		}
		if !change.Revalidate() {
			return ErrCharacterNotFound
		}
		if err := Save(ctx, tx.Settings, r, now); err != nil {
			return err
		}
		if !change.Revalidate() {
			return ErrCharacterNotFound
		}
		id = g.ID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// LoadAppearanceCatalog uses the active PVF, including the selectable icon UI.
// The paired client sends zero-based indices, not the XML's one-based IDs.
func LoadAppearanceCatalog(source pvf.Source) (AppearanceCatalog, error) {
	var c AppearanceCatalog
	if source == nil {
		return c, ErrRepositoryUnavailable
	}
	text, err := source.ReadText("etc/(r)guild.etc")
	if err != nil {
		return c, err
	}
	d, err := pvf.Parse("etc/(r)guild.etc", text)
	if err != nil {
		return c, err
	}
	fee, ok := d.Section("guild mark")
	if !ok || len(fee) != 2 || fee[0].Kind != pvf.TokenInt || fee[1].Kind != pvf.TokenInt || fee[0].Int != 5 || fee[1].Int <= 0 || fee[1].Int > math.MaxUint32 {
		return c, ErrInvalidAppearance
	}
	// The matched native sender treats indices 0..4 as free.
	c.FreeMarks, c.MarkPrice = uint32(fee[0].Int), uint32(fee[1].Int)
	text, err = source.ReadText("ui/guild/guildmark_cn.xui")
	if err != nil {
		return c, err
	}
	dec := xml.NewDecoder(strings.NewReader(strings.TrimRight(text, "\x00")))
	ids := map[int]bool{}
	for {
		token, e := dec.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return c, ErrInvalidAppearance
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "IControl" {
			continue
		}
		for _, a := range start.Attr {
			if a.Name.Local != "ID" {
				continue
			}
			n, e := strconv.Atoi(strings.TrimPrefix(a.Value, "STR:"))
			if e != nil || n < 1 || n > 256 || ids[n] {
				return c, ErrInvalidAppearance
			}
			ids[n] = true
		}
	}
	c.Marks = uint32(len(ids))
	if c.Marks < c.FreeMarks {
		return c, ErrInvalidAppearance
	}
	for n := 1; n <= len(ids); n++ {
		if !ids[n] {
			return c, ErrInvalidAppearance
		}
	}
	text, err = source.ReadText("etc/guild/insigniashapelist.etc")
	if err != nil {
		return c, err
	}
	d, err = pvf.Parse("etc/guild/insigniashapelist.etc", text)
	if err != nil {
		return c, err
	}
	for _, s := range []struct {
		name  string
		count *uint32
	}{{"insignia mark", &c.AuraMarks}, {"insignia frame", &c.AuraFrames}} {
		values, ok := d.Section(s.name)
		if !ok || len(values) == 0 {
			return c, ErrInvalidAppearance
		}
		for _, v := range values {
			if v.Kind != pvf.TokenString || !strings.HasSuffix(strings.ToLower(v.Value), ".ani") {
				return c, ErrInvalidAppearance
			}
		}
		*s.count = uint32(len(values))
	}
	return c, nil
}

// Appearance is guild-wide. Preserve the master-only editing rule; ordinary
// grade masks must not confer ownership of a guild-wide insignia.
func (r *Registry) appearanceMaster(owner repo.GuildOwner) (*Guild, error) {
	g, m, ok := r.GuildForOwner(owner)
	if !ok {
		return nil, ErrNotMember
	}
	if m.Grade != DefaultMasterGrade || g.MasterCharacterID != owner.CharacterID {
		return nil, ErrPermissionDenied
	}
	return g, nil
}

// SetMark must run inside the Guild UoW. Same-value retry does not charge again.
// Neither member contribution nor character gold pays the guild mark fee.
func (r *Registry) SetMark(owner repo.GuildOwner, mark byte, c AppearanceCatalog, now time.Time) (*Guild, error) {
	g, err := r.appearanceMaster(owner)
	if err != nil {
		return nil, err
	}
	if c.Marks == 0 || c.FreeMarks > c.Marks || c.MarkPrice == 0 || uint32(mark) >= c.Marks {
		return nil, ErrInvalidAppearance
	}
	if g.Mark == mark {
		return g, nil
	}
	price := uint32(0)
	if uint32(mark) >= c.FreeMarks {
		price = c.MarkPrice
	}
	if g.Mileage < price {
		return nil, ErrInsufficientMileage
	}
	g.Mileage -= price
	g.Mark, g.UpdatedAt = mark, now
	return g, nil
}

// The native clear button sends two UINT32_MAX values. A partly selected pair
// cannot be submitted by its save button and must not enter durable state.
func (r *Registry) SetAura(owner repo.GuildOwner, mark, frame uint32, c AppearanceCatalog, now time.Time) (*Guild, error) {
	g, err := r.appearanceMaster(owner)
	if err != nil {
		return nil, err
	}
	clear := mark == NoGuildAura && frame == NoGuildAura
	if !clear && (mark >= c.AuraMarks || frame >= c.AuraFrames) {
		return nil, ErrInvalidAppearance
	}
	if g.AuraMark != mark || g.AuraFrame != frame {
		g.AuraMark, g.AuraFrame, g.UpdatedAt = mark, frame, now
	}
	return g, nil
}
