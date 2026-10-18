package worldmap

import dnfpvf "longheng.io/server/internal/modules/dnf/pvf"

type SectionValueKind string

const (
	SectionFlag     SectionValueKind = "flag"
	SectionIntegers SectionValueKind = "integers"
	SectionNumbers  SectionValueKind = "numbers"
	SectionTexts    SectionValueKind = "texts"
	SectionMixed    SectionValueKind = "mixed"
)

type ExtensionSection struct {
	Name       string           `json:"name"`
	Occurrence int              `json:"occurrence"`
	Scope      []string         `json:"scope,omitempty"`
	Block      bool             `json:"block,omitempty"`
	Kind       SectionValueKind `json:"kind"`
	Integers   []int64          `json:"integers,omitempty"`
	Numbers    []float64        `json:"numbers,omitempty"`
	Texts      []string         `json:"texts,omitempty"`
	Symbols    []string         `json:"symbols,omitempty"`
	Tokens     []dnfpvf.Token   `json:"tokens,omitempty"`
}

type OptionalNumber struct {
	Value float64 `json:"value"`
	Set   bool    `json:"set"`
}

// DungeonRequiredItem 对应厂商 Tool\PvfLib\Models\DungeonFile.cs:38-43 的
// `DungeonRequiredItem`，即 `[required item]` 的一行 `ItemId Count Flag`。
// `ConsumeOnEntry` 只在第 3 个 token == 1 时为真（DungeonFile.cs:938）。
type DungeonRequiredItem struct {
	ItemID         int64 `json:"item_id"`
	Count          int64 `json:"count"`
	ConsumeOnEntry bool  `json:"consume_on_entry,omitempty"`
}

// DungeonStoryMode is the typed [story mode] block. DifficultySize and
// IncreaseExperienceRates preserve the values declared by the PVF; this parser
// does not infer a business index range. The bridge owns eligibility, the
// difficulty guard, and the experience calculation. Malformed means the block
// was present but at least one required field was missing, duplicated,
// non-integral, negative, or dimensionally inconsistent, so no experience bonus
// may be used.
type DungeonStoryMode struct {
	DifficultySize          int64   `json:"difficulty_size,omitempty"`
	IncreaseExperienceRates []int64 `json:"increase_experience_rates,omitempty"`
	QuestIDs                []int64 `json:"quest_ids,omitempty"`
	Malformed               bool    `json:"malformed,omitempty"`
}

type DungeonMetadata struct {
	Name                      string              `json:"name,omitempty"`
	Explain                   string              `json:"explain,omitempty"`
	CutsceneImage             Resource            `json:"cutscene_image"`
	MinimapImage              string              `json:"minimap_image,omitempty"`
	EnteringTitle             string              `json:"entering_title,omitempty"`
	MinimumRequiredLevel      OptionalInt         `json:"minimum_required_level"`
	BasisLevel                OptionalInt         `json:"basis_level"`
	ExperienceIncreasingPoint OptionalNumber      `json:"experience_increasing_point"`
	BackgroundPosition        OptionalInt         `json:"background_position"`
	DungeonType               string              `json:"dungeon_type,omitempty"`
	Champion                  []int64             `json:"champion,omitempty"`
	PathgateObjects           []int64             `json:"pathgate_objects,omitempty"`
	WorldMapPatternInfo       []RawSection        `json:"worldmap_pattern_info,omitempty"`
	WorldMapInfo              []RawSection        `json:"worldmap_info,omitempty"`
	Difficulty                OptionalInt         `json:"difficulty"`
	DifficultyLevels          []int64             `json:"difficulty_levels,omitempty"`
	DesignatedDifficulties    []int64             `json:"designated_difficulties,omitempty"`
	StoryMode                 *DungeonStoryMode   `json:"story_mode,omitempty"`
	TutorialDungeon           OptionalInt         `json:"tutorial_dungeon"`
	NoFatigue                 bool                `json:"no_fatigue,omitempty"`
	NamedMonsters             []int64             `json:"named_monsters,omitempty"`
	RecommendedLevels         []int64             `json:"recommended_levels,omitempty"`
	LimitPartyCount           OptionalInt         `json:"limit_party_count"`
	IntegerValues             map[string]int64    `json:"integer_values,omitempty"`
	NumberValues              map[string]float64  `json:"number_values,omitempty"`
	Flags                     map[string]bool     `json:"flags,omitempty"`
	TextValues                map[string][]string `json:"text_values,omitempty"`

	// ── 锦标赛/大会族（厂商 GameWorld\TournamentDungeonDefinition.cs:280-509）──
	//
	// 以下字段是 `[tournament dungeon]` 副本的准入与结算依据。厂商对它们做**硬校验**：
	// 缺项/畸形一律让整次副本准备失败，而不是填默认值。
	RequiredItems              []DungeonRequiredItem        `json:"required_items,omitempty"`
	EventRequiredItems         []DungeonRequiredItem        `json:"event_required_items,omitempty"`
	AddedRequiredItems         []DungeonRequiredItem        `json:"added_required_items,omitempty"`
	TournamentResultCards      []TournamentResultCard       `json:"tournament_result_cards,omitempty"`
	TournamentRewardItemRates  []TournamentRewardItemRate   `json:"tournament_reward_item_rates,omitempty"`
	TournamentClearExperiences []TournamentRewardExperience `json:"tournament_clear_experiences,omitempty"`

	// Malformed 标志：厂商 DungeonFile.cs:299-303 的同名标志。
	TournamentResultCardMalformed     bool `json:"tournament_result_card_malformed,omitempty"`
	TournamentRewardItemRateMalformed bool `json:"tournament_reward_item_rate_malformed,omitempty"`
	TournamentRewardExpMalformed      bool `json:"tournament_reward_exp_malformed,omitempty"`
}

type MazePoint struct {
	X      int64   `json:"x"`
	Y      int64   `json:"y"`
	Params []int64 `json:"params,omitempty"`
}

type MapSpecification struct {
	Type              string  `json:"type"`
	Coordinate        Point   `json:"coordinate"`
	MapIDs            []int64 `json:"map_ids"`
	SectionOccurrence int     `json:"section_occurrence"`
}

type RidableObject struct {
	ObjectType      string             `json:"object_type,omitempty"`
	MazeCoordinate  *Point             `json:"maze_coordinate,omitempty"`
	ObjectIndex     OptionalInt        `json:"object_index"`
	Team            string             `json:"team,omitempty"`
	Position        *Point             `json:"position,omitempty"`
	SourceSections  []RawSection       `json:"source_sections,omitempty"`
	UnknownSections []RawSection       `json:"unknown_sections,omitempty"`
	Extensions      []ExtensionSection `json:"extensions,omitempty"`
}

type RandomizedObjectScript struct {
	SelectCount     OptionalInt        `json:"select_count"`
	Regenerate      OptionalInt        `json:"regenerate"`
	MinimapIcon     OptionalInt        `json:"minimap_icon"`
	Objects         []RidableObject    `json:"objects,omitempty"`
	SourceSections  []RawSection       `json:"source_sections,omitempty"`
	UnknownSections []RawSection       `json:"unknown_sections,omitempty"`
	Extensions      []ExtensionSection `json:"extensions,omitempty"`
}

type ClearCondition struct {
	Type     string `json:"type"`
	TargetID int64  `json:"target_id"`
	Count    int64  `json:"count"`
}

type Maze struct {
	Index                    int                      `json:"index"`
	Width                    OptionalInt              `json:"width"`
	Height                   OptionalInt              `json:"height"`
	Greed                    string                   `json:"greed,omitempty"`
	MapSpecifications        []MapSpecification       `json:"map_specifications,omitempty"`
	Start                    *MazePoint               `json:"start,omitempty"`
	Boss                     *MazePoint               `json:"boss,omitempty"`
	HitCount                 []int64                  `json:"hit_count,omitempty"`
	SealDoorAppearRate       OptionalInt              `json:"seal_door_appear_rate"`
	SealDoorMapIndex         OptionalInt              `json:"seal_door_map_index"`
	SealDoorPosition         []int64                  `json:"seal_door_position,omitempty"`
	QuestConnection          []int64                  `json:"quest_connection,omitempty"`
	BossMapSpecifications    []RawSection             `json:"boss_map_specifications,omitempty"`
	LayeredMapSpecifications []RawSection             `json:"layered_map_specifications,omitempty"`
	BossSpecifications       []MapSpecification       `json:"boss_specifications,omitempty"`
	LayeredSpecifications    []MapSpecification       `json:"layered_specifications,omitempty"`
	RandomizedObjects        []RandomizedObjectScript `json:"randomized_objects,omitempty"`
	ClearConditions          []ClearCondition         `json:"clear_conditions,omitempty"`
	SourceSections           []RawSection             `json:"source_sections,omitempty"`
	OpaqueSections           []RawSection             `json:"opaque_sections,omitempty"`
	UnknownSections          []RawSection             `json:"unknown_sections,omitempty"`
	Extensions               []ExtensionSection       `json:"extensions,omitempty"`
	Diagnostics              []Diagnostic             `json:"diagnostics,omitempty"`
}

type Dungeon struct {
	ID              int64              `json:"id"`
	Path            string             `json:"path"`
	Metadata        DungeonMetadata    `json:"metadata"`
	Mazes           []Maze             `json:"mazes,omitempty"`
	SourceSections  []RawSection       `json:"source_sections,omitempty"`
	OpaqueSections  []RawSection       `json:"opaque_sections,omitempty"`
	UnknownSections []RawSection       `json:"unknown_sections,omitempty"`
	Extensions      []ExtensionSection `json:"extensions,omitempty"`
	Diagnostics     []Diagnostic       `json:"diagnostics,omitempty"`
}
