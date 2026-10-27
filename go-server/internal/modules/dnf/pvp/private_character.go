package pvp

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const PrivateCharacterKey = "pvp_private_character"

type PrivateCreateError byte

func (e PrivateCreateError) Error() string {
	return fmt.Sprintf("PVP private character creation rejected: %d", e)
}

type PrivateEquipment struct {
	Slot     string
	ItemID   int64
	Equipped bool
}

type PrivateCreation struct {
	Job, Grow                       byte
	Level, CheckLevel, RecreateDays int
	CharacterPath                   string
	Equipment                       []PrivateEquipment
}

func IsPrivateCharacter(character repo.CharacterRecord) bool {
	return character.Stats[PrivateCharacterKey] == 1
}

func LoadPrivateCreation(source pvf.Source, job, grow byte) (*PrivateCreation, error) {
	if source == nil || !jobmap.Valid(int(job)) || grow > 5 {
		return nil, fmt.Errorf("invalid PVP private character job/grow: %d/%d", job, grow)
	}
	text, err := source.ReadText("etc/pvpparameter.etc")
	if err != nil {
		return nil, err
	}
	doc, err := pvf.Parse("etc/pvpparameter.etc", text)
	if err != nil {
		return nil, err
	}
	pairs := doc.Ints("pvp matching possible character")
	allowed := false
	for i := 0; i+1 < len(pairs); i += 2 {
		allowed = allowed || pairs[i] == int64(job) && pairs[i+1] == int64(grow)
	}
	if !allowed || len(pairs)%2 != 0 {
		return nil, fmt.Errorf("PVP private profession is not enabled: %d/%d", job, grow)
	}
	level, levelOK := doc.Int("pvp revision level")
	check, checkOK := doc.Int("check level create private charac")
	if !levelOK || !checkOK || level < 1 || level > 255 || check < 1 || check > 255 {
		return nil, fmt.Errorf("invalid PVP private character levels")
	}
	plan := &PrivateCreation{Job: job, Grow: grow, Level: int(level), CheckLevel: int(check)}
	text, err = source.ReadText("character/character.lst")
	if err != nil {
		return nil, err
	}
	doc, err = pvf.Parse("character/character.lst", text)
	if err != nil {
		return nil, err
	}
	for _, row := range pvf.ParseList(doc) {
		if row.ID == int64(job) {
			plan.CharacterPath = path.Join("character", strings.ReplaceAll(row.Path, "\\", "/"))
			break
		}
	}
	if plan.CharacterPath == "" {
		return nil, fmt.Errorf("PVP private character profile missing")
	}
	text, err = source.ReadText(plan.CharacterPath)
	if err != nil {
		return nil, err
	}
	doc, err = pvf.Parse(plan.CharacterPath, text)
	if err != nil {
		return nil, err
	}
	inGrow := false
	for _, section := range doc.Sections {
		if strings.HasPrefix(section.Name, "growtype ") {
			inGrow = section.Name == fmt.Sprintf("growtype %d", int(grow)+1)
		}
		if !inGrow {
			continue
		}
		values := doc.Tokens[section.Start:section.End]
		switch section.Name {
		case "pvp private recreate penaly period":
			if len(values) != 1 || values[0].Kind != pvf.TokenInt || values[0].Int < 0 || values[0].Int > 365 {
				return nil, fmt.Errorf("invalid PVP private recreate period")
			}
			plan.RecreateDays = int(values[0].Int)
		case "pvp private create equipment list":
			if len(values)%3 != 0 {
				return nil, fmt.Errorf("invalid PVP private equipment triples")
			}
			for i := 0; i < len(values); i += 3 {
				if values[i].Kind != pvf.TokenString || values[i+1].Kind != pvf.TokenInt || values[i+1].Int <= 0 ||
					values[i+2].Kind != pvf.TokenInt || values[i+2].Int < 0 || values[i+2].Int > 1 {
					return nil, fmt.Errorf("invalid PVP private equipment entry")
				}
				plan.Equipment = append(plan.Equipment, PrivateEquipment{Slot: strings.Trim(values[i].Value, "[]"), ItemID: values[i+1].Int, Equipped: values[i+2].Int == 1})
			}
		}
	}
	if len(plan.Equipment) == 0 {
		return nil, fmt.Errorf("PVP private equipment missing for %d/%d", job, grow)
	}
	return plan, nil
}

// Check is used before the native confirmation and again in the creation transaction.
func (p *PrivateCreation) Check(ctx context.Context, characters repo.CharacterRepository, accountID string, now time.Time) (int64, error) {
	active, err := characters.ListByAccount(ctx, accountID, repo.DefaultCharacterSlotLimit)
	if err != nil {
		return 0, err
	}
	rating := int64(0)
	for _, character := range active {
		if character.AccountID != accountID || character.Job != strconv.Itoa(int(p.Job)) || byte(character.Stats["grow_type"])&15 != p.Grow {
			continue
		}
		if IsPrivateCharacter(character) || character.Level >= p.CheckLevel {
			return 0, PrivateCreateError(150)
		}
		rating = max(rating, character.Stats[RankPointKey])
	}
	history, ok := characters.(repo.CharacterDeletedProfessionReader)
	if !ok {
		return 0, fmt.Errorf("PVP private character deletion history unavailable")
	}
	deleted, found, err := history.LastDeletedProfession(ctx, accountID, p.Job, p.Grow)
	if err != nil {
		return 0, err
	}
	if found {
		if deleted.AccountID != accountID || deleted.Job != strconv.Itoa(int(p.Job)) || byte(deleted.Stats["grow_type"])&15 != p.Grow || deleted.Stats["delete_flag"] == 0 {
			return 0, fmt.Errorf("PVP private character history owner mismatch")
		}
		if deleted.UpdatedAt.Add(time.Duration(p.RecreateDays) * 24 * time.Hour).After(now) {
			return 0, PrivateCreateError(151)
		}
		rating = max(rating, deleted.Stats[RankPointKey])
	}
	return rating, nil
}
