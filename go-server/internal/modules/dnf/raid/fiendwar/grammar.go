package fiendwar

import (
	"errors"
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const (
	// AntonPVFPath is the only archive member read by this catalog.
	AntonPVFPath = "etc/raid/anton.etc"

	antonOrdinaryPhaseCount  = 2
	antonMovieEndValueCount  = 4
	antonSingleRaidPhaseName = "single raid phase"
)

var (
	ErrAntonPVFSourceRequired   = errors.New("anton raid PVF source is required")
	ErrAntonPVFDocumentInvalid  = errors.New("anton raid PVF document is invalid")
	ErrAntonPVFSectionMissing   = errors.New("anton raid PVF section is missing")
	ErrAntonPVFSectionShape     = errors.New("anton raid PVF section shape is invalid")
	ErrAntonPVFValueInvalid     = errors.New("anton raid PVF value is invalid")
	ErrAntonPVFDuplicateDungeon = errors.New("anton raid PVF dungeon is duplicated")
)

// AntonDungeonState is one native [INIT STATE] value.
type AntonDungeonState string

const (
	AntonDungeonOpen   AntonDungeonState = "open"
	AntonDungeonHide   AntonDungeonState = "hide"
	AntonDungeonClosed AntonDungeonState = "close"

	// AntonDungeonCleared is the runtime-only "this gate has been finished"
	// state. etc/raid/anton.etc never spells it -- the parser below still accepts
	// only the three PVF words -- but the client draws it differently from a
	// merely hidden gate, and the 86JP reference server sets it (its value 3) the
	// moment a gate's clear count reaches its requirement
	// (RaidHandler.Progression.cs:240 for the legs, ClearBlackFogSourceAsync for
	// the regenerated fog).
	AntonDungeonCleared AntonDungeonState = "cleared"
)

// AntonEntryCost is the item/count pair inside the global [ENTRY COST] block.
type AntonEntryCost struct {
	ItemID uint32
	Count  uint32
}

// AntonStateReward preserves one [STATE REWARD] row in source order. ItemID is
// the reward-definition item, not an instruction to grant that item directly.
type AntonStateReward struct {
	Kind             string
	Rank             int
	Weight           int
	DefinitionItemID uint32
	Flag             byte
}

// AntonRankCondition is one [DEAD COUNT] row inside a phase's
// [CONDITION FOR RANK]: the inclusive team death-count range that earns Rank.
// The rows are kept in source order and raw. The current catalogs leave a
// one-death gap between adjacent rows (0-1, 2-5, 6-9, 10-14), so collapsing
// them into thresholds would invent a rule for the gap.
type AntonRankCondition struct {
	MinimumDeaths int
	MaximumDeaths int
	Rank          int
}

// AntonDungeonInfo preserves one ordinary-phase [DUNGEON INFO] block.
type AntonDungeonInfo struct {
	Index     uint32
	InitState AntonDungeonState
	HP        int
	PartyMax  int
	// LockoutType is the native [LOCKOUT TYPE] pair. Only the Luke catalog
	// carries the section (its phase-two gates use 4 on the light route, 5 on
	// the dark one and 0 on the two shared rooms); etc/raid/anton.etc omits it
	// and leaves the pair at zero. What the value gates is not proven by the
	// PVF alone, so both numbers stay raw.
	LockoutType [2]int
	// DestroyObjects lists the object-group markers declared under this gate's
	// [DESTROY OBJECT INFO], in source order and with the native section
	// spelling. The section has no closing tag, so its entries run to the end
	// of the [DUNGEON INFO] block. Only the Luke phase-one sanctums declare it
	// ("birth sanctum object" and its two siblings).
	DestroyObjects []string
}

// AntonPhase preserves one phase block and its ordered rewards and dungeons.
// The single-mode phase uses the same shape even though its PVF block omits
// the ordinary minimum-clear-count section.
type AntonPhase struct {
	Name                     string
	MinimalDungeonClearCount int
	Rewards                  []AntonStateReward
	Dungeons                 []AntonDungeonInfo
	// RankConditions is the phase's [CONDITION FOR RANK] table. Every phase of
	// every current raid catalog declares one, including the single-mode phase.
	RankConditions []AntonRankCondition
	// CoinLimit is the optional [PHASE COIN LIMIT] revive-coin cap. Zero means
	// the phase declares no cap, which is every Anton phase and Luke's first.
	CoinLimit int
}

// AntonRaidModeKind identifies a mode advertised by the native Anton PVF.
type AntonRaidModeKind string

const (
	AntonRaidModeTeam   AntonRaidModeKind = "team"
	AntonRaidModeSingle AntonRaidModeKind = "single"
)

// AntonRaidModeChoice is the small, read-only projection needed by a mode
// selection UI. The complete phase data remains available on AntonConfig.
type AntonRaidModeChoice struct {
	Kind                AntonRaidModeKind
	PhaseNames          []string
	StartMinimumMembers int
	TimeLimitSeconds    []int
}

// AntonSingleModeConfig is the complete phase data declared by the native
// [SINGLE RAID PHASE]. Trigger expressions which are client/server script
// details remain opaque; the dungeon graph is advanced by the runtime owner.
type AntonSingleModeConfig struct {
	Name             string
	TimeLimitSeconds int
	Phase            AntonPhase
}

// AntonConfig is the strictly validated, read-only subset of the current
// etc/raid/anton.etc needed by both ordinary and single-mode raid runtimes.
type AntonConfig struct {
	PhaseMax                  int
	MemberMax                 int
	StartMinimumMembers       int
	SingleStartMinimumMembers int
	StartDelaySeconds         int
	// DisconnMemberWaitTime is the global [DISCONN MEMBER WAIT TIME]: how long a
	// dropped member's attack-party seat is held before the member is removed.
	// Both shipped raids declare 300 (etc/raid/anton.etc and
	// etc/raid/luke/luke.etc). Zero means the document does not declare it, and
	// the runtime then keeps its own 300-second default.
	DisconnMemberWaitTime int
	PhaseTimeoutSeconds   []int
	PhaseBreakSeconds     int
	MovieEndSeconds       []int
	EntryCost             AntonEntryCost
	// WeeklyEnterCount preserves the two global [WEEKLY ENTER COUNT] values in
	// source order (Luke 20 2, Anton 30 3). The client draws a weekly counter of
	// its own (dstr 75388 "周卢克 %d/%d"), but which of the two values is the
	// roster limit and which the per-character share is not proven here, so both
	// stay raw and no admission policy is derived from them yet.
	WeeklyEnterCount [2]int
	Phases           []AntonPhase
	SingleMode       *AntonSingleModeConfig
}

const MaxAttackPartyMembers = 8

type antonSectionBlock struct {
	open  int
	close int
}

func antonFindBlocks(document *dnfpvf.Document, name string, start, end int) ([]antonSectionBlock, error) {
	want := antonSectionName(name)
	closeName := "/" + want
	openIndex := -1
	blocks := make([]antonSectionBlock, 0)
	for index := start; index < end; index++ {
		sectionName := antonSectionName(document.Sections[index].Name)
		switch sectionName {
		case want:
			if openIndex >= 0 {
				return nil, antonShape(name, fmt.Sprintf("nested_open_at=%d", index))
			}
			openIndex = index
		case closeName:
			if openIndex < 0 {
				return nil, antonShape(name, fmt.Sprintf("unmatched_close_at=%d", index))
			}
			blocks = append(blocks, antonSectionBlock{open: openIndex, close: index})
			openIndex = -1
		}
	}
	if openIndex >= 0 {
		return nil, antonShape(name, fmt.Sprintf("unclosed_open_at=%d", openIndex))
	}
	return blocks, nil
}

func antonIntToken(token dnfpvf.Token, field string) (int, error) {
	if token.Kind != dnfpvf.TokenInt || token.Int < math.MinInt32 || token.Int > math.MaxInt32 {
		return 0, antonInvalid(field, token.Raw, "want=int32")
	}
	return int(token.Int), nil
}

func antonInvalid(field string, value any, detail string) error {
	return fmt.Errorf("%w: path=%s field=%s value=%v %s", ErrAntonPVFValueInvalid, AntonPVFPath, field, value, detail)
}

func antonMissing(section, context string) error {
	return fmt.Errorf("%w: path=%s section=%s context=%s", ErrAntonPVFSectionMissing, AntonPVFPath, section, context)
}

func antonOptionalUniqueSection(document *dnfpvf.Document, name string, start, end int) (int, bool, error) {
	want := antonSectionName(name)
	found := -1
	for index := start; index < end; index++ {
		if antonSectionName(document.Sections[index].Name) != want {
			continue
		}
		if found >= 0 {
			return -1, false, antonShape(name, fmt.Sprintf("duplicate_at=%d,%d", found, index))
		}
		found = index
	}
	if found < 0 {
		return -1, false, nil
	}
	return found, true, nil
}

func antonParseEntryCost(document *dnfpvf.Document, start, end int) (AntonEntryCost, error) {
	blocks, err := antonFindBlocks(document, "entry cost", start, end)
	if err != nil {
		return AntonEntryCost{}, err
	}
	if len(blocks) == 0 {
		return AntonEntryCost{}, antonMissing("entry cost", "global")
	}
	if len(blocks) != 1 {
		return AntonEntryCost{}, antonShape("entry cost", fmt.Sprintf("blocks=%d want=1", len(blocks)))
	}
	block := blocks[0]
	if block.close != block.open+1 {
		return AntonEntryCost{}, antonShape("entry cost", "unexpected nested sections")
	}
	if err := antonRequireEmptySection(document, block.close); err != nil {
		return AntonEntryCost{}, err
	}
	tokens, err := antonSectionTokens(document, block.open)
	if err != nil {
		return AntonEntryCost{}, err
	}
	if len(tokens) != 2 {
		return AntonEntryCost{}, antonShape("entry cost", fmt.Sprintf("values=%d want=2", len(tokens)))
	}
	itemID, err := antonPositiveUint32Token(tokens[0], "entry cost item id")
	if err != nil {
		return AntonEntryCost{}, err
	}
	count, err := antonPositiveUint32Token(tokens[1], "entry cost count")
	if err != nil {
		return AntonEntryCost{}, err
	}
	return AntonEntryCost{ItemID: itemID, Count: count}, nil
}

func antonParseRewards(document *dnfpvf.Document, block antonSectionBlock, phaseIndex int) ([]AntonStateReward, error) {
	if err := antonRequireEmptySection(document, block.open); err != nil {
		return nil, err
	}
	if err := antonRequireEmptySection(document, block.close); err != nil {
		return nil, err
	}
	rewards := make([]AntonStateReward, 0, block.close-block.open-1)
	for sectionIndex := block.open + 1; sectionIndex < block.close; sectionIndex++ {
		if antonSectionName(document.Sections[sectionIndex].Name) != "state reward" {
			return nil, antonShape("clear reward item", fmt.Sprintf("phase=%d unexpected_section=%q", phaseIndex+1, document.Sections[sectionIndex].Name))
		}
		tokens, err := antonSectionTokens(document, sectionIndex)
		if err != nil {
			return nil, err
		}
		if len(tokens) != 5 || tokens[0].Kind != dnfpvf.TokenString || strings.TrimSpace(tokens[0].Value) == "" {
			return nil, antonShape("state reward", fmt.Sprintf("phase=%d values=%d want=kind+4_ints", phaseIndex+1, len(tokens)))
		}
		rank, err := antonIntToken(tokens[1], "state reward rank")
		if err != nil {
			return nil, err
		}
		if rank < -1 {
			return nil, antonInvalid("state reward rank", rank, "want>=-1")
		}
		weight, err := antonPositiveIntToken(tokens[2], "state reward weight")
		if err != nil {
			return nil, err
		}
		itemID, err := antonPositiveUint32Token(tokens[3], "state reward definition item id")
		if err != nil {
			return nil, err
		}
		flag, err := antonIntToken(tokens[4], "state reward flag")
		if err != nil {
			return nil, err
		}
		if flag != 0 && flag != 1 {
			return nil, antonInvalid("state reward flag", flag, "want=0_or_1")
		}
		rewards = append(rewards, AntonStateReward{
			Kind: tokens[0].Value, Rank: rank, Weight: weight, DefinitionItemID: itemID, Flag: byte(flag),
		})
	}
	if len(rewards) == 0 {
		return nil, antonMissing("state reward", fmt.Sprintf("phase=%d", phaseIndex+1))
	}
	return rewards, nil
}

func antonParseSingleModeName(document *dnfpvf.Document, block antonSectionBlock) (string, error) {
	if err := antonRequireEmptySection(document, block.open); err != nil {
		return "", err
	}
	if err := antonRequireEmptySection(document, block.close); err != nil {
		return "", err
	}
	start, end := block.open+1, block.close
	nameIndex, err := antonUniqueSection(document, "phase name", start, end)
	if err != nil {
		return "", err
	}
	nameTokens, err := antonSectionTokens(document, nameIndex)
	if err != nil {
		return "", err
	}
	if len(nameTokens) != 1 || nameTokens[0].Kind != dnfpvf.TokenString || strings.TrimSpace(nameTokens[0].Value) == "" {
		return "", antonShape("phase name", "single phase want=one nonempty string")
	}
	return nameTokens[0].Value, nil
}

func antonPathKey(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimLeft(value, "/")
	return strings.ToLower(value)
}

func antonPositiveIntAt(document *dnfpvf.Document, sectionIndex int, field string) (int, error) {
	tokens, err := antonSectionTokens(document, sectionIndex)
	if err != nil {
		return 0, err
	}
	if len(tokens) != 1 {
		return 0, antonShape(field, fmt.Sprintf("values=%d want=1", len(tokens)))
	}
	return antonPositiveIntToken(tokens[0], field)
}

func antonPositiveIntToken(token dnfpvf.Token, field string) (int, error) {
	value, err := antonIntToken(token, field)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, antonInvalid(field, value, "want_positive")
	}
	return value, nil
}

func antonPositiveUint32Token(token dnfpvf.Token, field string) (uint32, error) {
	if token.Kind != dnfpvf.TokenInt || token.Int <= 0 || token.Int > math.MaxUint32 {
		return 0, antonInvalid(field, token.Raw, "want=positive_uint32")
	}
	return uint32(token.Int), nil
}

func antonReadPositiveIntListSection(document *dnfpvf.Document, name string, start, end, want int) ([]int, error) {
	sectionIndex, err := antonUniqueSection(document, name, start, end)
	if err != nil {
		return nil, err
	}
	tokens, err := antonSectionTokens(document, sectionIndex)
	if err != nil {
		return nil, err
	}
	if len(tokens) != want {
		return nil, antonShape(name, fmt.Sprintf("values=%d want=%d", len(tokens), want))
	}
	values := make([]int, 0, len(tokens))
	for _, token := range tokens {
		value, valueErr := antonPositiveIntToken(token, name)
		if valueErr != nil {
			return nil, valueErr
		}
		values = append(values, value)
	}
	return values, nil
}

func antonReadPositiveIntSection(document *dnfpvf.Document, name string, start, end int) (int, error) {
	sectionIndex, err := antonUniqueSection(document, name, start, end)
	if err != nil {
		return 0, err
	}
	return antonPositiveIntAt(document, sectionIndex, name)
}

func antonRequireEmptySection(document *dnfpvf.Document, sectionIndex int) error {
	tokens, err := antonSectionTokens(document, sectionIndex)
	if err != nil {
		return err
	}
	if len(tokens) != 0 {
		return antonShape(document.Sections[sectionIndex].Name, fmt.Sprintf("values=%d want=0", len(tokens)))
	}
	return nil
}

func antonSectionName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func antonSectionTokens(document *dnfpvf.Document, sectionIndex int) ([]dnfpvf.Token, error) {
	if sectionIndex < 0 || sectionIndex >= len(document.Sections) {
		return nil, antonShape("section", fmt.Sprintf("index=%d sections=%d", sectionIndex, len(document.Sections)))
	}
	section := document.Sections[sectionIndex]
	if section.Start < 0 || section.End < section.Start || section.End > len(document.Tokens) {
		return nil, antonShape(section.Name, fmt.Sprintf("token_range=%d:%d tokens=%d", section.Start, section.End, len(document.Tokens)))
	}
	return document.Tokens[section.Start:section.End], nil
}

func antonShape(section, detail string) error {
	return fmt.Errorf("%w: path=%s section=%s %s", ErrAntonPVFSectionShape, AntonPVFPath, section, detail)
}

func antonUniqueSection(document *dnfpvf.Document, name string, start, end int) (int, error) {
	found, ok, err := antonOptionalUniqueSection(document, name, start, end)
	if err != nil {
		return -1, err
	}
	if !ok {
		return -1, antonMissing(name, "required")
	}
	return found, nil
}
