package adventuregroup

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const CollectionMetadataPrefix = "adventure_collection_v1_"

var ErrCollectionRepresentative = errors.New("adventure collection representative is unavailable or ineligible")

type CollectionGroup struct {
	ID        byte
	Transfers [][2]byte
}

func parseCollectionGroups(doc *dnfpvf.Document) []CollectionGroup {
	var groups []CollectionGroup
	inExpert := false
	group := -1
	for _, section := range doc.Sections {
		switch normalizedPVFName(section.Name) {
		case "expert list":
			inExpert = true
		case "/expert list":
			inExpert = false
		case "group":
			if !inExpert {
				continue
			}
			id := firstInt(sectionTokens(doc, section))
			if id < 0 || id >= ContentTypeCount {
				continue
			}
			groups = append(groups, CollectionGroup{ID: byte(id)})
			group = len(groups) - 1
		case "info":
			if !inExpert || group < 0 {
				continue
			}
			tokens := sectionTokens(doc, section)
			if len(tokens) != 3 || tokens[0].Kind != dnfpvf.TokenString || tokens[1].Kind != dnfpvf.TokenString || tokens[2].Kind != dnfpvf.TokenInt || tokens[2].Int <= 0 || tokens[2].Int > 15 {
				continue
			}
			for job := 0; job <= 15; job++ {
				if jobmap.MatchesQuestTag(tokens[0].Value, job) {
					groups[group].Transfers = append(groups[group].Transfers, [2]byte{byte(job), byte(tokens[2].Int)})
					break
				}
			}
		}
	}
	return groups
}

func (c RuntimeConfig) CollectionGroupFor(job, branch byte) (byte, bool) {
	for _, group := range c.CollectionGroups {
		for _, transfer := range group.Transfers {
			if transfer == [2]byte{job, branch} {
				return group.ID, true
			}
		}
	}
	return 0, false
}

func (c RuntimeConfig) CollectionCounts(rows []dnfrepo.CharacterRecord, levelCap int) [ContentTypeCount]uint16 {
	var result [ContentTypeCount]uint16
	for _, row := range rows {
		if row.Name == "" {
			continue
		}
		if _, ok := CollectionKey(row, levelCap); !ok {
			continue
		}
		job, _ := strconv.Atoi(row.Job)
		packed, _ := dnfrepo.CharacterGrowTypeInDomain(row)
		if group, ok := c.CollectionGroupFor(byte(job), byte(dnfrepo.CharacterGrowTypeBranch(packed))); ok {
			result[group]++
		}
	}
	return result
}

// CollectionKey uses the base transfer branch, not the packed awakening byte.
// Collection eligibility is checked separately against the persisted growth.
func CollectionKey(character dnfrepo.CharacterRecord, levelCap int) (string, bool) {
	packed, present := dnfrepo.CharacterGrowTypeInDomain(character)
	branch, awakening := dnfrepo.DecodeCharacterGrowType(packed)
	job, err := strconv.Atoi(character.Job)
	if !present || err != nil || job < 0 || job > 15 || branch == 0 || awakening != 2 || character.Level < levelCap {
		return "", false
	}
	return strconv.Itoa(job) + "_" + strconv.Itoa(branch), true
}

func CollectionSelections(account dnfrepo.AccountRecord) map[string]string {
	result := make(map[string]string)
	for key, value := range account.Metadata {
		if strings.HasPrefix(key, CollectionMetadataPrefix) {
			result[strings.TrimPrefix(key, CollectionMetadataPrefix)] = value
		}
	}
	return result
}

// CollectionRows projects exactly one row for each eligible transfer. An empty
// name means selectable; only a still-owned, still-eligible saved choice is
// presented as selected. The account character count is independent of this.
func CollectionRows(characters []dnfrepo.CharacterRecord, selections map[string]string, levelCap int) []dnfrepo.CharacterRecord {
	ordered := append([]dnfrepo.CharacterRecord(nil), characters...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Slot != ordered[j].Slot {
			return ordered[i].Slot < ordered[j].Slot
		}
		left, _ := strconv.ParseUint(ordered[i].CharacterID, 10, 32)
		right, _ := strconv.ParseUint(ordered[j].CharacterID, 10, 32)
		return left < right
	})
	rows := make([]dnfrepo.CharacterRecord, 0)
	indexes := make(map[string]int)
	for _, character := range ordered {
		key, eligible := CollectionKey(character, levelCap)
		if !eligible {
			continue
		}
		index, found := indexes[key]
		selected := selections[key] == character.CharacterID
		if found && !selected {
			continue
		}
		row := character
		if !selected {
			row.Name = ""
		}
		if found {
			rows[index] = row
		} else {
			indexes[key] = len(rows)
			rows = append(rows, row)
		}
	}
	return rows
}

type SelectCollectionCommand struct {
	AccountID          string
	CharacterID        string
	Job                byte
	Branch             byte
	ServerGroup        byte
	DefaultServerGroup byte
	LevelCap           int
	UpdatedAt          time.Time
}

func SelectCollection(ctx context.Context, repositories dnfrepo.Group, command SelectCollectionCommand) (dnfrepo.CharacterRecord, error) {
	if repositories.Account == nil || repositories.Character == nil || command.AccountID == "" || command.CharacterID == "" || command.LevelCap <= 0 {
		return dnfrepo.CharacterRecord{}, ErrCollectionRepresentative
	}
	character, found, err := repositories.Character.Load(ctx, command.CharacterID)
	if err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	key, eligible := CollectionKey(character, command.LevelCap)
	serverGroup, validGroup := CollectionServerGroup(character, command.DefaultServerGroup)
	if !found || character.AccountID != command.AccountID || !eligible || key != strconv.Itoa(int(command.Job))+"_"+strconv.Itoa(int(command.Branch)) || !validGroup || serverGroup != command.ServerGroup {
		return dnfrepo.CharacterRecord{}, ErrCollectionRepresentative
	}
	account, found, err := repositories.Account.Load(ctx, command.AccountID)
	if err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	if !found || account.AccountID != command.AccountID {
		return dnfrepo.CharacterRecord{}, ErrAccountNotFound
	}
	if err := dnfrepo.SaveAccountMetadataEntry(ctx, repositories.Account, account, CollectionMetadataPrefix+key, character.CharacterID, command.UpdatedAt); err != nil {
		return dnfrepo.CharacterRecord{}, err
	}
	return character, nil
}

// CollectionServerGroup uses an explicit positive character group when present.
// Existing local characters have no group (or the old zero placeholder), so
// their group comes from the configured source server, never the advertised
// zero-based online channel index. BE0259 requires a nonzero selection group.
func CollectionServerGroup(character dnfrepo.CharacterRecord, sourceServerGroup byte) (byte, bool) {
	value := character.Stats["server_group_id"]
	if value < 0 || value > 255 {
		return 0, false
	}
	if value == 0 {
		return sourceServerGroup, sourceServerGroup != 0
	}
	return byte(value), true
}
