// Package imagecommunication loads the remote image-communication directory.
//
// The source-menu NPC and its town availability are intentionally outside this
// package. The entries parsed here are the remote NPC targets advertised by
// etc/imagecommunication.etc.
package imagecommunication

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const (
	// ConfigPath is the image-communication configuration in Script.pvf.
	ConfigPath = "etc/imagecommunication.etc"
	// NPCListPath maps NPC IDs to their npc/*.npc definitions.
	NPCListPath = "npc/npc.lst"
)

var (
	ErrSourceRequired = errors.New("image communication pvf source is required")
	ErrInvalidConfig  = errors.New("image communication config is invalid")
	ErrDuplicateNPC   = errors.New("image communication NPC is duplicated")
	ErrMissingNPC     = errors.New("image communication target NPC is missing")
)

// Position2D is the target's configured position in the communication UI.
type Position2D struct {
	X int64
	Y int64
}

// Position3D is a configured summon or effect position.
type Position3D struct {
	X int64
	Y int64
	Z int64
}

// Target is one remote NPC available from the image-communication menu.
//
// RequireQuests must all be present in the caller's active-quest set. Completed
// quests are deliberately not part of this type or its eligibility check.
type Target struct {
	NPCID          int64      `json:"npc_id"`
	Position       Position2D `json:"position"`
	RequireQuests  []int64    `json:"require_quests"`
	DefinitionPath string     `json:"definition_path"`
}

// Allows reports whether every required quest is active.
func (t Target) Allows(activeQuestIDs map[int64]struct{}) bool {
	if len(t.RequireQuests) == 0 {
		return false
	}
	for _, questID := range t.RequireQuests {
		if _, ok := activeQuestIDs[questID]; !ok {
			return false
		}
	}
	return true
}

// Config is the immutable directory snapshot returned to callers.
type Config struct {
	Charge         int64      `json:"charge"`
	SummonTime     int64      `json:"summon_time"`
	SummonPosition Position3D `json:"summon_position"`
	EffectPosition Position3D `json:"effect_position"`
	Targets        []Target   `json:"targets"`
}

// Catalog is a validated image-communication directory.
type Catalog struct {
	config Config
	order  []int64
	byNPC  map[int64]Target
}

// Load reads and validates etc/imagecommunication.etc and every configured
// target's npc/npc.lst definition.
func Load(ctx context.Context, source dnfpvf.Source) (*Catalog, error) {
	if source == nil {
		return nil, ErrSourceRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	text, err := source.ReadText(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read image communication config: %w", err)
	}
	config, err := parseConfig(text)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := attachNPCDefinitions(ctx, source, config.Targets); err != nil {
		return nil, err
	}

	catalog := &Catalog{
		config: config,
		order:  make([]int64, 0, len(config.Targets)),
		byNPC:  make(map[int64]Target, len(config.Targets)),
	}
	for _, target := range config.Targets {
		catalog.order = append(catalog.order, target.NPCID)
		catalog.byNPC[target.NPCID] = cloneTarget(target)
	}
	return catalog, nil
}

// Config returns a deep copy of the validated configuration snapshot.
func (c *Catalog) Config() Config {
	if c == nil {
		return Config{}
	}
	return cloneConfig(c.config)
}

// Targets returns the configured remote targets in PVF order.
func (c *Catalog) Targets() []Target {
	if c == nil {
		return nil
	}
	out := make([]Target, 0, len(c.order))
	for _, npcID := range c.order {
		out = append(out, cloneTarget(c.byNPC[npcID]))
	}
	return out
}

// Target returns one configured remote target by NPC ID.
func (c *Catalog) Target(npcID int64) (Target, bool) {
	if c == nil {
		return Target{}, false
	}
	target, ok := c.byNPC[npcID]
	if !ok {
		return Target{}, false
	}
	return cloneTarget(target), true
}

// Eligible returns configured targets whose complete require-quest list is in
// the active-quest set. Output preserves the order in the PVF.
func (c *Catalog) Eligible(activeQuestIDs map[int64]struct{}) []Target {
	if c == nil {
		return nil
	}
	out := make([]Target, 0, len(c.order))
	for _, npcID := range c.order {
		target := c.byNPC[npcID]
		if target.Allows(activeQuestIDs) {
			out = append(out, cloneTarget(target))
		}
	}
	return out
}

// IsEligible reports whether a known target's complete quest list is active.
func (c *Catalog) IsEligible(npcID int64, activeQuestIDs map[int64]struct{}) bool {
	target, ok := c.Target(npcID)
	return ok && target.Allows(activeQuestIDs)
}

type targetSectionState struct {
	target    Target
	seen      map[string]bool
	hasNPCID  bool
	hasNPCpos bool
	hasQuest  bool
}

func parseConfig(text string) (Config, error) {
	doc, err := dnfpvf.Parse(ConfigPath, text)
	if err != nil {
		return Config{}, fmt.Errorf("%w: parse %s: %v", ErrInvalidConfig, ConfigPath, err)
	}

	var (
		config       Config
		inRoot       bool
		closedRoot   bool
		current      *targetSectionState
		rootSections = make(map[string]bool)
		targetIDs    = make(map[int64]bool)
	)
	if len(doc.Sections) == 0 {
		return Config{}, invalid("missing [image communication]")
	}

	for sectionIndex, section := range doc.Sections {
		name := normalizedSection(section.Name)
		tokens := doc.Tokens[section.Start:section.End]

		switch name {
		case "image communication":
			if inRoot || closedRoot || current != nil {
				return Config{}, invalid("duplicate or misplaced [image communication]")
			}
			if sectionIndex != 0 {
				return Config{}, invalid("content precedes [image communication]")
			}
			if len(tokens) != 0 {
				return Config{}, invalid("[image communication] must not have values")
			}
			inRoot = true
			continue
		case "/image communication":
			if !inRoot || closedRoot || current != nil {
				return Config{}, invalid("misplaced [/image communication]")
			}
			if len(tokens) != 0 {
				return Config{}, invalid("[image communication] has trailing values")
			}
			inRoot = false
			closedRoot = true
			continue
		}

		if !inRoot {
			return Config{}, invalid("section [%s] is outside [image communication]", section.Name)
		}
		if closedRoot {
			return Config{}, invalid("section [%s] follows [/image communication]", section.Name)
		}

		if current != nil {
			switch name {
			case "npc":
				return Config{}, invalid("nested [npc]")
			case "/npc":
				if len(tokens) != 0 {
					return Config{}, invalid("[/npc] must not have values")
				}
				target, err := finishTarget(current)
				if err != nil {
					return Config{}, err
				}
				if targetIDs[target.NPCID] {
					return Config{}, fmt.Errorf("%w: NPC %d", ErrDuplicateNPC, target.NPCID)
				}
				targetIDs[target.NPCID] = true
				config.Targets = append(config.Targets, target)
				current = nil
			case "npc index":
				if current.seen[name] {
					return Config{}, invalid("duplicate [npc index] for one NPC")
				}
				values, err := exactIntegers(name, tokens, 1)
				if err != nil {
					return Config{}, err
				}
				if values[0] <= 0 {
					return Config{}, invalid("[npc index] must be positive")
				}
				current.target.NPCID = values[0]
				current.hasNPCID = true
				current.seen[name] = true
			case "npc pos":
				if current.seen[name] {
					return Config{}, invalid("duplicate [npc pos] for one NPC")
				}
				values, err := exactIntegers(name, tokens, 2)
				if err != nil {
					return Config{}, err
				}
				current.target.Position = Position2D{X: values[0], Y: values[1]}
				current.hasNPCpos = true
				current.seen[name] = true
			case "require quest":
				if current.seen[name] {
					return Config{}, invalid("duplicate [require quest] for one NPC")
				}
				values, err := integers(name, tokens)
				if err != nil {
					return Config{}, err
				}
				if len(values) == 0 {
					return Config{}, invalid("[require quest] must not be empty")
				}
				seen := make(map[int64]bool, len(values))
				for _, questID := range values {
					if questID <= 0 {
						return Config{}, invalid("[require quest] IDs must be positive")
					}
					if seen[questID] {
						return Config{}, invalid("duplicate require quest %d for NPC %d", questID, current.target.NPCID)
					}
					seen[questID] = true
				}
				current.target.RequireQuests = values
				current.hasQuest = true
				current.seen[name] = true
			default:
				return Config{}, invalid("unexpected section [%s] inside [npc]", section.Name)
			}
			continue
		}

		switch name {
		case "npc":
			if len(tokens) != 0 {
				return Config{}, invalid("[npc] must not have values")
			}
			current = &targetSectionState{
				target: Target{},
				seen:   make(map[string]bool),
			}
		case "charge":
			if rootSections[name] {
				return Config{}, invalid("duplicate [charge]")
			}
			values, err := exactIntegers(name, tokens, 1)
			if err != nil {
				return Config{}, err
			}
			if values[0] < 0 || values[0] > math.MaxUint32 {
				return Config{}, invalid("[charge] must be between 0 and %d", uint64(math.MaxUint32))
			}
			config.Charge = values[0]
			rootSections[name] = true
		case "summon time":
			if rootSections[name] {
				return Config{}, invalid("duplicate [summon time]")
			}
			values, err := exactIntegers(name, tokens, 1)
			if err != nil {
				return Config{}, err
			}
			if values[0] < 0 {
				return Config{}, invalid("[summon time] must not be negative")
			}
			config.SummonTime = values[0]
			rootSections[name] = true
		case "summon pos":
			if rootSections[name] {
				return Config{}, invalid("duplicate [summon pos]")
			}
			values, err := exactIntegers(name, tokens, 3)
			if err != nil {
				return Config{}, err
			}
			config.SummonPosition = Position3D{X: values[0], Y: values[1], Z: values[2]}
			rootSections[name] = true
		case "effect pos":
			if rootSections[name] {
				return Config{}, invalid("duplicate [effect pos]")
			}
			values, err := exactIntegers(name, tokens, 3)
			if err != nil {
				return Config{}, err
			}
			config.EffectPosition = Position3D{X: values[0], Y: values[1], Z: values[2]}
			rootSections[name] = true
		default:
			return Config{}, invalid("unexpected section [%s] inside [image communication]", section.Name)
		}
	}

	if !closedRoot || current != nil {
		return Config{}, invalid("unclosed image communication structure")
	}
	for _, required := range []string{"charge", "summon time", "summon pos", "effect pos"} {
		if !rootSections[required] {
			return Config{}, invalid("missing [%s]", required)
		}
	}
	if len(config.Targets) == 0 {
		return Config{}, invalid("at least one [npc] target is required")
	}
	return config, nil
}

func finishTarget(state *targetSectionState) (Target, error) {
	if state == nil {
		return Target{}, invalid("missing [npc] state")
	}
	if !state.hasNPCID {
		return Target{}, invalid("[npc] is missing [npc index]")
	}
	if !state.hasNPCpos {
		return Target{}, invalid("NPC %d is missing [npc pos]", state.target.NPCID)
	}
	if !state.hasQuest || len(state.target.RequireQuests) == 0 {
		return Target{}, invalid("NPC %d is missing [require quest]", state.target.NPCID)
	}
	return state.target, nil
}

func attachNPCDefinitions(ctx context.Context, source dnfpvf.Source, targets []Target) error {
	text, err := source.ReadText(NPCListPath)
	if err != nil {
		return fmt.Errorf("%w: read %s: %v", ErrMissingNPC, NPCListPath, err)
	}
	doc, err := dnfpvf.Parse(NPCListPath, text)
	if err != nil {
		return fmt.Errorf("%w: parse %s: %v", ErrInvalidConfig, NPCListPath, err)
	}

	references := make(map[int64][]string)
	for _, entry := range dnfpvf.ParseList(doc) {
		references[entry.ID] = append(references[entry.ID], resolveNPCReference(entry.Path))
	}

	for index := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		paths := references[targets[index].NPCID]
		if len(paths) == 0 {
			return fmt.Errorf("%w: NPC %d is absent from %s", ErrMissingNPC, targets[index].NPCID, NPCListPath)
		}
		if len(paths) != 1 {
			return fmt.Errorf("%w: NPC %d has %d definitions in %s", ErrDuplicateNPC, targets[index].NPCID, len(paths), NPCListPath)
		}
		definitionPath := paths[0]
		if !strings.HasPrefix(strings.ToLower(definitionPath), "npc/") || strings.Contains(definitionPath, "..") {
			return invalid("NPC %d definition path %q is outside npc/", targets[index].NPCID, definitionPath)
		}
		definitionText, err := source.ReadText(definitionPath)
		if err != nil {
			return fmt.Errorf("%w: NPC %d at %s: %v", ErrMissingNPC, targets[index].NPCID, definitionPath, err)
		}
		definition, err := dnfpvf.Parse(definitionPath, definitionText)
		if err != nil {
			return fmt.Errorf("%w: parse NPC %d at %s: %v", ErrInvalidConfig, targets[index].NPCID, definitionPath, err)
		}
		if len(definition.Tokens) == 0 {
			return fmt.Errorf("%w: NPC %d definition at %s is empty", ErrMissingNPC, targets[index].NPCID, definitionPath)
		}
		targets[index].DefinitionPath = definitionPath
	}
	return nil
}

func resolveNPCReference(reference string) string {
	reference = strings.TrimSpace(strings.Trim(reference, "`\"'"))
	reference = strings.ReplaceAll(reference, "\\", "/")
	if strings.HasPrefix(strings.ToLower(reference), "npc/") {
		return path.Clean(reference)
	}
	return path.Clean(path.Join(path.Dir(NPCListPath), reference))
}

func exactIntegers(section string, tokens []dnfpvf.Token, want int) ([]int64, error) {
	values, err := integers(section, tokens)
	if err != nil {
		return nil, err
	}
	if len(values) != want {
		return nil, invalid("[%s] has %d integers, want %d", section, len(values), want)
	}
	return values, nil
}

func integers(section string, tokens []dnfpvf.Token) ([]int64, error) {
	values := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind != dnfpvf.TokenInt {
			return nil, invalid("[%s] contains non-integer token %q", section, token.Raw)
		}
		values = append(values, token.Int)
	}
	return values, nil
}

func normalizedSection(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}

func cloneConfig(config Config) Config {
	out := config
	out.Targets = cloneTargets(config.Targets)
	return out
}

func cloneTargets(targets []Target) []Target {
	if targets == nil {
		return nil
	}
	out := make([]Target, len(targets))
	for index, target := range targets {
		out[index] = cloneTarget(target)
	}
	return out
}

func cloneTarget(target Target) Target {
	out := target
	if target.RequireQuests != nil {
		out.RequireQuests = append([]int64(nil), target.RequireQuests...)
	}
	return out
}
