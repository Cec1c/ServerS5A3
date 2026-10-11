package profession

import (
	"fmt"

	skill "longheng.io/server/internal/modules/dnf/skill"
)

// ResetGrants reconstructs the free baseline from the persisted profession,
// never from the currently learned (possibly damaged) skill record.
func (p *Profiles) ResetGrants(catalog *skill.Table, job, packed byte, characterLevel int) ([]Grant, error) {
	if catalog == nil {
		return nil, ErrSourceRequired
	}
	profile, ok := p.profile(job)
	if !ok {
		return nil, ErrProfileMissing
	}
	branch, stage := Decode(packed)
	if !profile.growSupported[branch] || stage > 2 {
		return nil, fmt.Errorf("%w: reset job=%d grow=0x%02x", ErrTransitionInvalid, job, packed)
	}
	for current := byte(1); current <= stage; current++ {
		if !profile.awakeningSupported[branch][current] {
			return nil, fmt.Errorf("%w: reset job=%d branch=%d stage=%d", ErrTransitionInvalid, job, branch, current)
		}
	}
	grants, _ := p.FreeGrants(job, branch, stage)
	for _, grant := range grants {
		if _, exists := catalog.Find(job, grant.SkillID); !exists {
			return nil, fmt.Errorf("%w: job=%d skill=%d", ErrSkillMissing, job, grant.SkillID)
		}
	}
	return grantsAtCharacterLevel(catalog, job, characterLevel, grants), nil
}
