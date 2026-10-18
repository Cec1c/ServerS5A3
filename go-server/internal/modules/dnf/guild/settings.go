package guild

import (
	"errors"
	"math/bits"
	"strings"
	"time"
	"unicode/utf16"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	SettingsJoinMode  uint32 = 1 << 1
	SettingsPromotion uint32 = 1 << 5
	SettingsTags      uint32 = 1 << 17
	SettingsMask             = SettingsJoinMode | SettingsPromotion | SettingsTags
	PromotionMaxBytes        = 255
	PromotionMaxUTF16        = 80
)

var ErrInvalidSettings = errors.New("guild settings selection is invalid")

type SettingsChange struct {
	Mask         uint32
	Promotion    string
	WebPromotion bool
	Tags         uint32
	JoinMode     byte
}

// SetSettings runs inside the Guild UoW. The native op740 always carries every
// field, including stale values for fields the actor did not select or cannot
// edit. Validate every selected permission before applying any of the changes.
func (r *Registry) SetSettings(owner dnfrepo.GuildOwner, change SettingsChange, now time.Time) (*Guild, error) {
	g, member, found := r.GuildForOwner(owner)
	if !found {
		return nil, ErrNotMember
	}
	if change.Mask == 0 || change.Mask & ^SettingsMask != 0 {
		return nil, ErrInvalidSettings
	}
	permissions := GradePermissionsFor(g, member.Grade)
	// The matched tag editor calls the permission helper with fallback=true;
	// its fallback table grants bit17 to the master even with a zero saved mask.
	if member.Grade == DefaultMasterGrade {
		permissions |= SettingsTags
	}
	if permissions&change.Mask != change.Mask {
		return nil, ErrPermissionDenied
	}
	if change.Mask&SettingsJoinMode != 0 && change.JoinMode > 1 {
		// The native selector exposes 0=approval, 1=immediate; 2 is its fallback
		// for a missing control/invalid selection, not a third admission policy.
		return nil, ErrInvalidSettings
	}
	if change.Mask&SettingsTags != 0 && (change.Tags & ^uint32(0x7f) != 0 || bits.OnesCount32(change.Tags) > 3) {
		return nil, ErrInvalidSettings
	}
	if change.Mask&SettingsPromotion != 0 {
		if err := validateText(change.Promotion, PromotionMaxBytes, true); err != nil {
			return nil, err
		}
		if len(utf16.Encode([]rune(change.Promotion))) > PromotionMaxUTF16 || (g.Promotion != "" && strings.TrimSpace(change.Promotion) == "") {
			return nil, ErrInvalidText
		}
	}
	changed := false
	if change.Mask&SettingsJoinMode != 0 && g.JoinMode != change.JoinMode {
		g.JoinMode = change.JoinMode
		changed = true
	}
	if change.Mask&SettingsPromotion != 0 && (g.Promotion != change.Promotion || g.WebPromotion != change.WebPromotion) {
		g.Promotion, g.WebPromotion = change.Promotion, change.WebPromotion
		changed = true
	}
	if change.Mask&SettingsTags != 0 {
		current := uint32(0)
		for _, tag := range g.Tags {
			if tag < 7 {
				current |= 1 << tag
			}
		}
		if current != change.Tags {
			g.Tags = nil
			for tag := byte(0); tag < 7; tag++ {
				if change.Tags&(1<<tag) != 0 {
					g.Tags = append(g.Tags, tag)
				}
			}
			changed = true
		}
	}
	if changed {
		g.UpdatedAt = now
	}
	return g, nil
}
