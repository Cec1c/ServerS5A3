package quest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/profession"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const DefaultList = "n_quest/quest.lst"

var ErrCatalogEmpty = errors.New("dnf quest catalog is empty")

var (
	ErrQuestDefinitionMissing         = errors.New("dnf quest definition is missing")
	ErrQuestNotAcceptable             = errors.New("dnf quest is not acceptable for this character state")
	ErrQuestInitialTriggerUnsupported = errors.New("dnf quest initial trigger rule is not implemented")
	ErrQuestAcceptEventItemsRequired  = errors.New("dnf quest acceptance requires event-item transaction")
	ErrQuestRewardUnsupported         = errors.New("dnf quest reward type is unsupported")
	ErrQuestRewardMalformed           = errors.New("dnf quest reward data is malformed")
	ErrQuestRewardSelectionInvalid    = errors.New("dnf quest reward selection is invalid")
)

// RewardItemRule preserves one exact PVF [reward int data] item rule. Job and
// grow-type filters are evaluated only after the selected character is known.
type RewardItemRule struct {
	ItemID       int64
	Count        int64
	HasJobFilter bool
	Job          int
	GrowType     int
}

// TargetCharacterRule preserves one PVF [target character] tuple:
// job tag, first-grow branch, and awakening stage.
type TargetCharacterRule struct {
	JobTag         string
	FirstGrowType  int
	AwakeningStage int
}

// MonsterRewardItemEntry preserves one PVF [monster reward item] 7-tuple:
// monster_code dungeon_id difficulty item_id count drop_rate max_stack.
// dungeon_id -1 and difficulty -1 are wildcards (86JP QuestDropProvider).
type MonsterRewardItemEntry struct {
	MonsterCode int64
	DungeonID   int64
	Difficulty  int64
	ItemID      int64
	Count       int64
	DropRate    int64
	MaxStack    int64
}

// EnemyRewardItemEntry preserves one PVF [enemy reward item] 8-tuple:
// enemy_code enemy_type dungeon_id difficulty item_id count drop_rate max_stack.
// enemy_type 2 = AI character, 3 = passive object (86JP QuestDropProvider).
type EnemyRewardItemEntry struct {
	EnemyCode  int64
	EnemyType  int64
	DungeonID  int64
	Difficulty int64
	ItemID     int64
	Count      int64
	DropRate   int64
	MaxStack   int64
}

type FinishRewardPlan struct {
	QuestID             int64
	PVFPath             string
	QuestLevel          int
	Difficulty          string
	IgnoreLevel4Exp     bool
	HasGoldReward       bool
	GoldMultiple        int
	Items               []RewardItemRule
	RewardSelectionUsed bool
	ProfessionRequest   profession.Request
	Profession          profession.Transition
	HasProfession       bool
	ExpertJobType       byte
	HasExpertJob        bool
	HasSlotExpansion    bool
	SlotExpansionIndex  uint32
	SlotExpansionBit    byte
	// hellChallenge 记录 `[reward type] `[hell challenge]`` 这一档：PVF 里一共 10 个
	// 任务用它（`n_quest/hell/achievement_*_hell_*.qst`，「深渊派对Ep1..Ep5」，
	// `[reward int data] 1`），它们的 `[type]` 是 `[hunt enemy]`，打的是深渊派对里的
	// 敌人。本档**不发放任何道具/货币/标志位**：本版没有深渊派对运行时，这个计数
	// 存到哪里没有任何证据（见 dnfbridge/dungeon_hell_party_entry.go）。它存在的意义
	// 只是让这 10 个任务能正常结算归档 —— 此前它们落到
	// `ErrQuestRewardUnsupported`，被 op34 回失败 22，等于**永远做不完**。
	HasHellChallenge   bool
	HellChallengeValue int64
}

// Definition is the subset of a PVF quest definition required to decide
// whether the quest belongs in ENUM_NOTIPACKET_ACCEPTABLE_QUEST_LIST.
type Definition struct {
	ID   int64
	Path string
	// Name 是 PVF [name] 的原文。90CN 真档实测：n_quest/quest.lst 的 2996 条里
	// 2741 条带该段（另 255 条没有），所以调用方必须自己决定缺段时怎么显示，
	// 不能在这里补默认文案。
	Name string
	// DependMessage / ConditionMessage / SolveMessage 是 PVF 的
	// [depend message] / [condition message] / [solve message] 原文（只做
	// 首尾裁剪与 `%%` -> `%` 还原）。同一次实测里三段的存在率分别是
	// 2589 / 2993 / 2636，**缺段即空串**；GM 面板据此显示「PVF 未提供」，
	// 绝不允许在这里合成文案。
	DependMessage          string
	ConditionMessage       string
	SolveMessage           string
	Grade                  string
	Type                   string
	SubType                int
	IntData                []int64
	CheckCount             []int64
	ConditionData          []int64
	DependGiveItemData     []int64
	HasDependGiveItem      bool
	CantGiveUp             bool
	LevelMin               int
	LevelMax               int
	Job                    string
	TargetCharacter        string
	TargetCharacterRules   []TargetCharacterRule
	GrowType               int
	HasGrowType            bool
	JobChangeQuest         int
	ExposedByNPC           int64
	IsEvent                bool
	ShowQuestOnEvent       int64
	HasCreatureRequirement bool
	HasExpertRequirement   bool
	MainQuestID            int64
	PreRequiredGroups      [][]int64
	PreRequiredAnswers     []int64
	QuestionOptionCount    int
	CollisionQuests        []int64
	Difficulty             string
	IgnoreQuestLevel4Exp   bool
	NoExperience           bool
	RewardType             string
	RewardIntData          []int64
	HasGoldReward          bool
	GoldMultiple           int
	RewardItems            []RewardItemRule
	RewardSelectionItems   []RewardItemRule
	RewardParseError       string
	MonsterRewardItems     []MonsterRewardItemEntry
	EnemyRewardItems       []EnemyRewardItemEntry
}

type Catalog struct {
	ordered []Definition
	byID    map[int64]Definition
}

type Snapshot struct {
	Definitions int
	Epic        int
	Normal      int
}

type CharacterEligibility struct {
	Level                      int
	Job                        int
	GrowType                   int
	epicRoadEvent350Enabled    bool
	epicRoadEvent350Recognized bool
}

type EligibilityResult struct {
	IDs         []int32
	EpicCount   int
	ActiveCount int
	// PreflightExcluded counts candidates that the per-quest PVF/state rule
	// accepted but that the accept preflight (DecideAccept) or the [main quest]
	// closure refused. QuestList is the only producer of a non-zero value; the
	// bridge logs it so "the client was offered a quest the server refuses" is
	// observable instead of being an inference.
	PreflightExcluded int
}

// AcceptDecision is the single authoritative answer to "may this character
// accept this quest right now?".
//
// The client-facing acceptable-quest notification
// (ENUM_NOTIPACKET_ACCEPTABLE_QUEST_LIST, dnfbridge op21) and the op31 accept
// request must never disagree: a quest the client is told it may accept has to
// be accepted by the server, and a quest the server refuses must never be
// advertised. Both sides therefore run through DecideAccept /
// decideAcceptForDefinition below instead of deriving a second predicate.
type AcceptDecision uint8

const (
	// AcceptRejected means the accept path refuses: Owner.ApplyAccept returns a
	// business error and the bridge answers nothing (or failure 23). Such a
	// quest must never appear in the client-facing list.
	AcceptRejected AcceptDecision = iota
	// AcceptNew means ApplyAccept would persist a new active row.
	AcceptNew
	// AcceptIdempotent means the quest is already active, so ApplyAccept
	// acknowledges the persisted active state without mutating anything.
	AcceptIdempotent
)

type AcceptPlan struct {
	QuestID         int64
	Path            string
	Type            string
	InitTrigger     uint32
	LinkedSubQuests []AcceptLinkedSubQuestPlan
}

type AcceptLinkedSubQuestPlan struct {
	QuestID     int64
	Path        string
	Type        string
	InitTrigger uint32
}

func Load(ctx context.Context, index *dnfpvf.Index) (*Catalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	entries := index.List(DefaultList)
	if len(entries) == 0 {
		return nil, ErrCatalogEmpty
	}
	catalog := &Catalog{
		ordered: make([]Definition, 0, len(entries)),
		byID:    make(map[int64]Definition, len(entries)),
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.ID <= 0 || entry.ID > math.MaxInt32 {
			continue
		}
		if _, exists := catalog.byID[entry.ID]; exists {
			continue
		}
		doc, ok := index.Document(entry.Path)
		if !ok {
			continue
		}
		definition := parseDefinition(entry, doc)
		catalog.ordered = append(catalog.ordered, definition)
		catalog.byID[definition.ID] = definition
	}
	if len(catalog.ordered) == 0 {
		return nil, ErrCatalogEmpty
	}
	return catalog, nil
}

func (c *Catalog) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	snapshot := Snapshot{Definitions: len(c.ordered)}
	for _, definition := range c.ordered {
		switch normalizeQuestTag(definition.Grade) {
		case "epic":
			snapshot.Epic++
		case "", "normal":
			snapshot.Normal++
		}
	}
	return snapshot
}

// ExpertJobTransitionQuestIDs returns every quest explicitly assigned to the
// PVF expert-job transition chain, preserving quest-list order.
func (c *Catalog) ExpertJobTransitionQuestIDs() []int64 {
	if c == nil {
		return nil
	}
	ids := make([]int64, 0)
	seen := make(map[int64]struct{})
	for _, definition := range c.ordered {
		if definition.ID <= 0 || definition.JobChangeQuest != 20 {
			continue
		}
		if _, exists := seen[definition.ID]; exists {
			continue
		}
		seen[definition.ID] = struct{}{}
		ids = append(ids, definition.ID)
	}
	return ids
}

// ExpertJobTransitionTerminalQuestIDs returns transition quests that are not
// prerequisites of another transition quest in the same PVF chain.
func (c *Catalog) ExpertJobTransitionTerminalQuestIDs() []int64 {
	ids := c.ExpertJobTransitionQuestIDs()
	if len(ids) == 0 {
		return nil
	}
	members := make(map[int64]struct{}, len(ids))
	for _, questID := range ids {
		members[questID] = struct{}{}
	}
	nonTerminal := make(map[int64]struct{})
	for _, questID := range ids {
		for _, successor := range c.Successors(questID) {
			if _, ok := members[successor.ID]; ok {
				nonTerminal[questID] = struct{}{}
				break
			}
		}
	}
	terminal := make([]int64, 0, len(ids))
	for _, questID := range ids {
		if _, exists := nonTerminal[questID]; !exists {
			terminal = append(terminal, questID)
		}
	}
	return terminal
}

func (c *Catalog) Find(id int64) (Definition, bool) {
	if c == nil {
		return Definition{}, false
	}
	definition, ok := c.byID[id]
	return cloneDefinition(definition), ok
}

// Search 在**当前 PVF 的任务目录**里做有界的文本检索，返回目录顺序稳定的
// 一页结果（上限 200，<=0 时取 30）。空 query 故意返回第一页，让 GM 面板不必
// 再维护第二份客户端索引。
//
// 命中规则（与上游 0.1.8 的 dnfquest.Catalog.Search 同口径）：
//   - query 能解析成十进制任务 ID 时按 ID 精确命中；
//   - 否则按 [name] 子串命中；
//   - 再否则按 PVF 路径子串命中。
//
// 注意 [name] 不是每个任务都有（90CN 真档 2996 条里 255 条缺段），缺段的任务
// 只能靠路径或 ID 命中——这是降级，不是造数据。
func (c *Catalog) Search(query string, limit int) []Definition {
	if c == nil {
		return nil
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}
	query = strings.ToLower(strings.TrimSpace(query))
	numericID, numericErr := strconv.ParseInt(query, 10, 64)
	out := make([]Definition, 0, limit)
	for _, definition := range c.ordered {
		if query != "" && !questSearchHit(definition, query, numericID, numericErr == nil) {
			continue
		}
		out = append(out, cloneDefinition(definition))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func questSearchHit(definition Definition, lowerQuery string, numericID int64, numeric bool) bool {
	if numeric && definition.ID == numericID {
		return true
	}
	if strings.Contains(strings.ToLower(definition.Name), lowerQuery) {
		return true
	}
	return strings.Contains(strings.ToLower(definition.Path), lowerQuery)
}

// Successors returns the definitions that list questID in any prerequisite
// group, preserving catalog order. It is the static PVF chain step used to
// resolve the next quest after a completed one; it does not inspect character
// quest state.
func (c *Catalog) Successors(questID int64) []Definition {
	if c == nil {
		return nil
	}
	var successors []Definition
	for _, definition := range c.ordered {
		for _, group := range definition.PreRequiredGroups {
			for _, prerequisite := range group {
				if prerequisite == questID {
					successors = append(successors, cloneDefinition(definition))
					goto nextDefinition
				}
			}
		}
	nextDefinition:
	}
	return successors
}

// Acceptable answers the per-quest question only: may this character start this
// quest as a new active row right now, judged from PVF rules plus the persisted
// record? It deliberately excludes already-active quests and applies no
// client-node closure; QuestList is the client-facing projection and
// DecideAccept is the accept preflight.
func (c *Catalog) Acceptable(character CharacterEligibility, record dnfrepo.QuestRecord) EligibilityResult {
	return c.AcceptableAt(character, record, time.Now())
}

// AcceptableAt is Acceptable with an explicit clock, so the per-service-day
// daily-quest rollover projection can be pinned in tests. Production callers
// use Acceptable.
func (c *Catalog) AcceptableAt(character CharacterEligibility, record dnfrepo.QuestRecord, now time.Time) EligibilityResult {
	if c == nil || !jobmap.Valid(character.Job) {
		return EligibilityResult{}
	}
	if character.Level <= 0 {
		character.Level = 1
	}
	completed, active := questStateSets(c.ProjectDailyQuestRollover(record, now))
	result := EligibilityResult{IDs: make([]int32, 0, 64)}
	for _, definition := range c.ordered {
		if !definitionAcceptable(c, definition, character, record, completed, active, false) {
			continue
		}
		result.IDs = append(result.IDs, int32(definition.ID))
		if _, exists := active[definition.ID]; exists {
			result.ActiveCount++
		}
		if normalizeQuestTag(definition.Grade) == "epic" {
			result.EpicCount++
		}
	}
	return result
}

// QuestList returns the PVF-backed IDs carried by the current EXE's
// ENUM_NOTIPACKET_ACCEPTABLE_QUEST_LIST notification. The task manual's All
// view combines this definition list with the active-progress snapshot, so an
// already active quest must remain present here even though PlanAccept must
// continue to reject accepting it a second time.
//
// Every listed ID satisfies the accept preflight, and so does every [main quest]
// ancestor of a listed ID. This is the invariant that keeps the client honest:
// the current client renders one task-manual node per main quest and answers a
// click on that node with op31 carrying the MAIN quest id, so listing a [sub]
// row whose main quest the server would refuse produced a permanently failing
// "可接取" entry. Live 90CN character 3 reproduced exactly that: the
// main-story sweep completed the [epic] [quest clear] parent 3971
// ("离别时刻", n_quest/quest_renewal/epic_quest/devildom/timebeast/
// timebeast_epic_25.qst) while its [sub] children 3972/3973 - grade [sub], so
// outside the sweep - stayed acceptable, and the client kept offering node
// 3971 while op31 for 3971 was rejected with ErrQuestNotAcceptable.
func (c *Catalog) QuestList(character CharacterEligibility, record dnfrepo.QuestRecord) EligibilityResult {
	return c.QuestListAt(character, record, time.Now())
}

// QuestListAt is QuestList with an explicit clock. The client-facing
// acceptable-quest list (op21) and the accept preflight must agree on which
// quests are still completed today, so both run the same daily rollover
// projection before the accept predicate.
func (c *Catalog) QuestListAt(character CharacterEligibility, record dnfrepo.QuestRecord, now time.Time) EligibilityResult {
	if c == nil || !jobmap.Valid(character.Job) {
		return EligibilityResult{}
	}
	if character.Level <= 0 {
		character.Level = 1
	}
	completed, active := questStateSets(c.ProjectDailyQuestRollover(record, now))
	result := EligibilityResult{IDs: make([]int32, 0, 64)}
	for _, definition := range c.ordered {
		if !definitionAcceptable(c, definition, character, record, completed, active, true) {
			continue
		}
		if !c.clientListNodeAcceptable(definition, character, record, completed, active) {
			result.PreflightExcluded++
			continue
		}
		result.IDs = append(result.IDs, int32(definition.ID))
		if _, exists := active[definition.ID]; exists {
			result.ActiveCount++
		}
		if normalizeQuestTag(definition.Grade) == "epic" {
			result.EpicCount++
		}
	}
	return result
}

// DecideAccept is the accept preflight for one quest, expressed once. It mirrors
// Owner.ApplyAccept, which itself calls this function before planning or
// persisting anything.
func (c *Catalog) DecideAccept(character CharacterEligibility, record dnfrepo.QuestRecord, questID int64) (AcceptDecision, error) {
	return c.DecideAcceptAt(character, record, questID, time.Now())
}

// DecideAcceptAt is DecideAccept with an explicit clock; see QuestListAt.
func (c *Catalog) DecideAcceptAt(character CharacterEligibility, record dnfrepo.QuestRecord, questID int64, now time.Time) (AcceptDecision, error) {
	definition, ok := c.Find(questID)
	if !ok {
		return AcceptRejected, ErrQuestDefinitionMissing
	}
	if !jobmap.Valid(character.Job) {
		return AcceptRejected, ErrQuestNotAcceptable
	}
	if character.Level <= 0 {
		character.Level = 1
	}
	completed, active := questStateSets(c.ProjectDailyQuestRollover(record, now))
	return c.decideAcceptForDefinition(definition, character, record, completed, active)
}

// decideAcceptForDefinition is DecideAccept without the catalog lookup, so the
// client-list builder can evaluate the quest and its ancestors from definitions
// it already holds. It is the only place the accept predicate is implemented.
func (c *Catalog) decideAcceptForDefinition(definition Definition, character CharacterEligibility, record dnfrepo.QuestRecord, completed, active map[int64]struct{}) (AcceptDecision, error) {
	// Owner.ApplyAccept refuses PVF depend-give-item quests before anything
	// else: they need an asset transaction this owner does not provide, so the
	// outcome is a refusal even when the row is already active.
	if definition.HasDependGiveItem {
		return AcceptRejected, ErrQuestAcceptEventItemsRequired
	}
	if existing, known := questStateFor(record, definition.ID); known && isActiveQuestStatus(existing.Status) {
		return AcceptIdempotent, nil
	}
	if !definitionAcceptable(c, definition, character, record, completed, active, false) {
		return AcceptRejected, ErrQuestNotAcceptable
	}
	if c.activeClassChangeConflict(definition, active) {
		return AcceptRejected, ErrQuestNotAcceptable
	}
	return AcceptNew, nil
}

// clientListNodeAcceptable is the one rule behind the client-facing list: the
// quest itself and its whole [main quest] ancestry must survive the accept
// preflight. A missing ancestor definition is not treated as a refusal - such a
// row cannot be resolved to a client node, and dropping playable quests on an
// unparsable PVF link would be the larger risk.
func (c *Catalog) clientListNodeAcceptable(definition Definition, character CharacterEligibility, record dnfrepo.QuestRecord, completed, active map[int64]struct{}) bool {
	if decision, _ := c.decideAcceptForDefinition(definition, character, record, completed, active); decision == AcceptRejected {
		return false
	}
	for ancestorID := definition.MainQuestID; ancestorID > 0; {
		ancestor, ok := c.byID[ancestorID]
		if !ok {
			break
		}
		if decision, _ := c.decideAcceptForDefinition(ancestor, character, record, completed, active); decision == AcceptRejected {
			return false
		}
		if ancestor.MainQuestID == ancestorID {
			break
		}
		ancestorID = ancestor.MainQuestID
	}
	return true
}

// PlanAccept reuses the same PVF/character/database eligibility decision used
// for the acceptable-quest snapshot and derives only initial-trigger rules
// that are represented in the typed catalog. Quests that grant event items at
// acceptance remain blocked until inventory and quest state share one unit of
// work.
func (c *Catalog) PlanAccept(character CharacterEligibility, record dnfrepo.QuestRecord, questID int64) (AcceptPlan, error) {
	return c.PlanAcceptAt(character, record, questID, time.Now())
}

// PlanAcceptAt is PlanAccept with an explicit clock; see QuestListAt.
func (c *Catalog) PlanAcceptAt(character CharacterEligibility, record dnfrepo.QuestRecord, questID int64, now time.Time) (AcceptPlan, error) {
	definition, ok := c.Find(questID)
	if !ok {
		return AcceptPlan{}, ErrQuestDefinitionMissing
	}
	if !jobmap.Valid(character.Job) {
		return AcceptPlan{}, ErrQuestNotAcceptable
	}
	if character.Level <= 0 {
		character.Level = 1
	}
	completed, active := questStateSets(c.ProjectDailyQuestRollover(record, now))
	decision, err := c.decideAcceptForDefinition(definition, character, record, completed, active)
	if decision != AcceptNew {
		if err == nil {
			// Already active: PlanAccept only ever plans a NEW accept, which is
			// the contract the active-quest regression pins.
			err = ErrQuestNotAcceptable
		}
		return AcceptPlan{}, err
	}
	// Keep the accepted task's trigger in the same domain state as the C#
	// QuestData.ComputeInitTrigger implementation.  This is deliberately a
	// PVF/state calculation only; it does not infer any packet fields.
	trigger := definitionInitialTrigger(definition, completed)
	return AcceptPlan{
		QuestID:         definition.ID,
		Path:            definition.Path,
		Type:            definition.Type,
		InitTrigger:     trigger,
		LinkedSubQuests: c.linkedSubQuestAcceptPlans(definition, character, record, completed, active),
	}, nil
}

// PlanFinishReward parses the detached PVF reward for the current character.
// Profession rewards remain requests until the settlement owner resolves them
// through the job profile and current grow_type inside the repository UoW. It
// does not allocate slots, mutate repositories, calculate EXP, or emit an ACK.
func (c *Catalog) PlanFinishReward(character CharacterEligibility, questID int64, rewardSelectIndex uint16, hasRewardSelect bool) (FinishRewardPlan, error) {
	definition, ok := c.Find(questID)
	if !ok {
		return FinishRewardPlan{}, ErrQuestDefinitionMissing
	}
	if !jobmap.Valid(character.Job) {
		return FinishRewardPlan{}, ErrQuestNotAcceptable
	}
	plan := FinishRewardPlan{
		QuestID:         definition.ID,
		PVFPath:         definition.Path,
		QuestLevel:      definition.LevelMin,
		Difficulty:      "G",
		IgnoreLevel4Exp: definition.IgnoreQuestLevel4Exp,
		HasGoldReward:   definition.HasGoldReward,
		GoldMultiple:    definition.GoldMultiple,
	}
	// `[difficulty]` is a string key: the runtime PVF declares both one-character
	// ranks and the two-digit ranks `10`..`17`, so keep the whole trimmed token
	// (upper-cased, since the data mixes `f`/`F`) instead of requiring a single
	// rune. Dropping the two-digit ranks silently downgraded those main quests to
	// the default `G` weight.
	if difficulty := strings.ToUpper(strings.TrimSpace(definition.Difficulty)); difficulty != "" {
		plan.Difficulty = difficulty
	}
	rewardType := normalizeQuestTag(definition.RewardType)
	if rewardType == "" {
		if !emptyRewardDataValid(definition.RewardIntData) {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d empty reward type has data=%v", ErrQuestRewardMalformed, questID, definition.RewardIntData)
		}
		if hasRewardSelect {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d has no selectable reward", ErrQuestRewardSelectionInvalid, questID)
		}
		return plan, nil
	}
	if rewardType == "grow type" || rewardType == "awakening type" {
		request, err := profession.ParseReward(definition.JobChangeQuest, definition.RewardType, definition.RewardIntData)
		if err != nil {
			if errors.Is(err, profession.ErrRewardMalformed) {
				return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d: %v", ErrQuestRewardMalformed, questID, err)
			}
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d: %v", ErrQuestRewardUnsupported, questID, err)
		}
		plan.ProfessionRequest = request
		plan.HasProfession = true
		if hasRewardSelect {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d profession reward has no selection", ErrQuestRewardSelectionInvalid, questID)
		}
		return plan, nil
	}
	if rewardType == "expert job" {
		// The current PVF encodes the chosen auxiliary-profession type as one
		// positive byte (for example, quest 2710 supplies 3 for disjointer).
		// Current EXE op34 consumes that value under chain type 20.
		if len(definition.RewardIntData) != 1 || definition.RewardIntData[0] <= 0 || definition.RewardIntData[0] > math.MaxUint8 {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d expert job values=%v", ErrQuestRewardMalformed, questID, definition.RewardIntData)
		}
		if hasRewardSelect {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d expert job reward has no selection", ErrQuestRewardSelectionInvalid, questID)
		}
		plan.ExpertJobType = byte(definition.RewardIntData[0])
		plan.HasExpertJob = true
		return plan, nil
	}
	if rewardType == "slot expansion" {
		if hasRewardSelect {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d slot expansion reward has no selection", ErrQuestRewardSelectionInvalid, questID)
		}
		// The current runtime PVF stores the slot-expansion index in
		// [reward int data]: 0=support, 1=magic stone, 2=earring. The durable
		// character column and current global actor snapshots store the
		// corresponding bit.
		if len(definition.RewardIntData) != 1 || definition.RewardIntData[0] < 0 || definition.RewardIntData[0] > 2 {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d slot expansion values=%v", ErrQuestRewardMalformed, questID, definition.RewardIntData)
		}
		plan.HasSlotExpansion = true
		plan.SlotExpansionIndex = uint32(definition.RewardIntData[0])
		plan.SlotExpansionBit, _ = ExEquipSlotBitForPVFIndex(plan.SlotExpansionIndex)
		return plan, nil
	}
	if rewardType == "hell challenge" || rewardType == "inferno challenge" {
		// 「深渊派对任务无法完成」的根因就在这里：`[reward type] `[hell challenge]``
		// 既不是 `[item]` 也不是本函数已支持的 grow/awakening/expert job/slot expansion，
		// 于是掉进下面那句 `ErrQuestRewardUnsupported`；dnfbridge 把它算成业务失败
		// （quest_finish.go:719）并回 class1/op34 失败 22，客户端永远拿不到完成回执。
		//
		// 本档按"无资产奖励"处理：任务照常归档（EXP/金币仍按 questParameter.etc 的
		// 等级×难度规则计算，与其他任务一致），**不发明**任何道具、货币或标志位 ——
		// 这个"深渊挑战"计数存在哪里没有任何证据（本版也没有深渊派对运行时）。
		if len(definition.RewardIntData) == 0 || definition.RewardIntData[0] < 0 {
			return FinishRewardPlan{}, fmt.Errorf(
				"%w: quest=%d hell challenge values=%v",
				ErrQuestRewardMalformed,
				questID,
				definition.RewardIntData,
			)
		}
		if hasRewardSelect {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d hell challenge reward has no selection", ErrQuestRewardSelectionInvalid, questID)
		}
		plan.HasHellChallenge = true
		plan.HellChallengeValue = definition.RewardIntData[0]
		return plan, nil
	}
	if rewardType != "item" {
		return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d type=%q", ErrQuestRewardUnsupported, questID, definition.RewardType)
	}
	if definition.RewardParseError != "" {
		return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d: %s", ErrQuestRewardMalformed, questID, definition.RewardParseError)
	}
	for _, rule := range definition.RewardItems {
		if rewardRuleMatches(rule, character) {
			plan.Items = append(plan.Items, rule)
		}
	}
	selectable := make([]RewardItemRule, 0, len(definition.RewardSelectionItems))
	for _, rule := range definition.RewardSelectionItems {
		if rewardRuleMatches(rule, character) {
			selectable = append(selectable, rule)
		}
	}
	if len(selectable) == 0 {
		if hasRewardSelect {
			return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d has no selectable reward", ErrQuestRewardSelectionInvalid, questID)
		}
		return plan, nil
	}
	if !hasRewardSelect || int(rewardSelectIndex) >= len(selectable) {
		return FinishRewardPlan{}, fmt.Errorf("%w: quest=%d selected=%d choices=%d", ErrQuestRewardSelectionInvalid, questID, rewardSelectIndex, len(selectable))
	}
	plan.Items = append(plan.Items, selectable[int(rewardSelectIndex)])
	plan.RewardSelectionUsed = true
	return plan, nil
}

func emptyRewardDataValid(values []int64) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}

func parseDefinition(entry dnfpvf.ListEntry, doc *dnfpvf.Document) Definition {
	definition := Definition{
		ID:           entry.ID,
		Path:         entry.Path,
		LevelMin:     1,
		LevelMax:     99,
		GrowType:     -1,
		ExposedByNPC: -1,
	}
	definition.Name, _ = doc.Text("name")
	definition.DependMessage = questPresentationText(doc, "depend message")
	definition.ConditionMessage = questPresentationText(doc, "condition message")
	definition.SolveMessage = questPresentationText(doc, "solve message")
	definition.Grade, _ = doc.Text("grade")
	definition.Type, _ = doc.Text("type")
	definition.Difficulty, _ = doc.Text("difficulty")
	definition.RewardType, _ = doc.Text("reward type")
	definition.RewardIntData = append([]int64(nil), doc.Ints("reward int data")...)
	if value, ok := doc.Int("gold multiple"); ok {
		definition.GoldMultiple = boundedInt(value)
	}
	if tokens, ok := doc.Section("ignore quest level 4 exp"); ok {
		definition.IgnoreQuestLevel4Exp = len(tokens) == 0
		for _, token := range tokens {
			if token.Kind == dnfpvf.TokenInt {
				definition.IgnoreQuestLevel4Exp = token.Int != 0
				break
			}
		}
	}
	for _, attribute := range doc.Texts("attribute") {
		if normalizeQuestTag(attribute) == "not give exp quest" {
			definition.NoExperience = true
			break
		}
	}
	if normalizeQuestTag(definition.RewardType) == "item" {
		definition.RewardItems, definition.HasGoldReward, definition.RewardParseError = parseRewardItemRules(doc, "reward int data")
		definition.RewardSelectionItems, definition.RewardParseError = parseRewardItemRulesAfter(definition.RewardParseError, doc, "reward selection int data")
	}
	if value, ok := doc.Int("sub type"); ok {
		definition.SubType = boundedInt(value)
	}
	definition.IntData = append([]int64(nil), doc.Ints("int data")...)
	definition.CheckCount = append([]int64(nil), doc.Ints("check count")...)
	definition.ConditionData = append([]int64(nil), doc.Ints("condition data")...)
	if _, ok := doc.Section("depend give item"); ok {
		definition.HasDependGiveItem = true
		definition.DependGiveItemData = append([]int64(nil), doc.Ints("depend give item")...)
	}
	_, definition.CantGiveUp = doc.Section("cant giveup")
	definition.Job = strings.Join(doc.Texts("job"), " ")
	definition.TargetCharacter = strings.Join(doc.Texts("target character"), " ")
	if tokens, ok := doc.Section("target character"); ok {
		definition.TargetCharacterRules = parseTargetCharacterRules(tokens)
	}
	if levels := doc.Ints("level"); len(levels) > 0 {
		definition.LevelMin = boundedInt(levels[0])
		if len(levels) > 1 {
			definition.LevelMax = boundedInt(levels[1])
		}
	}
	if value, ok := doc.Int("grow type"); ok {
		definition.GrowType = boundedInt(value)
		definition.HasGrowType = true
	}
	if value, ok := doc.Int("job change quest"); ok {
		definition.JobChangeQuest = boundedInt(value)
	}
	if value, ok := doc.Int("exposed by npc"); ok {
		definition.ExposedByNPC = value
	}
	if value, ok := doc.Int("main quest"); ok {
		definition.MainQuestID = value
	}
	if value, ok := doc.Int("show quest on event"); ok {
		definition.ShowQuestOnEvent = value
	}
	if value, ok := doc.Int("event"); ok && value != 0 {
		definition.IsEvent = true
	}
	if value, ok := doc.Int("creature kind"); ok && value >= 0 {
		definition.HasCreatureRequirement = true
	}
	if values := doc.Ints("expertjob level"); len(values) >= 2 && values[0] >= 0 && values[1] >= 0 {
		definition.HasExpertRequirement = true
	}
	definition.PreRequiredGroups = sectionIntGroups(doc, "pre required quest")
	definition.PreRequiredAnswers = flattenIntGroups(sectionIntGroups(doc, "pre required quest answer"))
	if values := doc.Texts("question info"); len(values) > 2 {
		definition.QuestionOptionCount = len(values) - 2
	}
	definition.CollisionQuests = flattenIntGroups(sectionIntGroups(doc, "collision quest"))
	definition.MonsterRewardItems = parseMonsterRewardItems(doc)
	definition.EnemyRewardItems = parseEnemyRewardItems(doc)
	return definition
}

func parseRewardItemRulesAfter(previous string, doc *dnfpvf.Document, section string) ([]RewardItemRule, string) {
	rules, _, parseError := parseRewardItemRules(doc, section)
	if previous != "" {
		return rules, previous
	}
	return rules, parseError
}

func parseRewardItemRules(doc *dnfpvf.Document, section string) ([]RewardItemRule, bool, string) {
	tokens, ok := doc.Section(section)
	if !ok || len(tokens) == 0 {
		return nil, false, ""
	}
	rules := make([]RewardItemRule, 0, len(tokens)/2)
	hasGoldReward := false
	for index := 0; index < len(tokens); {
		if tokens[index].Kind != dnfpvf.TokenInt {
			return nil, false, fmt.Sprintf("[%s] token %d must be item id, got kind=%s raw=%q", section, index, tokens[index].Kind, tokens[index].Raw)
		}
		rule := RewardItemRule{ItemID: tokens[index].Int, Job: -1, GrowType: -1}
		index++
		if index < len(tokens) && rewardJobMarker(tokens[index]) {
			if index+3 >= len(tokens) || tokens[index+1].Kind != dnfpvf.TokenInt || tokens[index+2].Kind != dnfpvf.TokenInt || tokens[index+3].Kind != dnfpvf.TokenInt {
				return nil, false, fmt.Sprintf("[%s] item %d has incomplete [job] tuple", section, rule.ItemID)
			}
			rule.HasJobFilter = true
			rule.Job = boundedInt(tokens[index+1].Int)
			rule.GrowType = boundedInt(tokens[index+2].Int)
			rule.Count = tokens[index+3].Int
			index += 4
		} else {
			if index >= len(tokens) || tokens[index].Kind != dnfpvf.TokenInt {
				return nil, false, fmt.Sprintf("[%s] item %d is missing count", section, rule.ItemID)
			}
			rule.Count = tokens[index].Int
			index++
		}
		if rule.ItemID == 0 {
			hasGoldReward = hasGoldReward || rule.Count > 0
			continue
		}
		if rule.ItemID < 0 || rule.Count <= 0 || rule.Count > math.MaxUint32 {
			return nil, false, fmt.Sprintf("[%s] invalid item/count %d/%d", section, rule.ItemID, rule.Count)
		}
		rules = append(rules, rule)
	}
	return rules, hasGoldReward, ""
}

func rewardJobMarker(token dnfpvf.Token) bool {
	if token.Kind != dnfpvf.TokenString && token.Kind != dnfpvf.TokenIdent {
		return false
	}
	return normalizeQuestTag(token.Value) == "job"
}

// parseMonsterRewardItems parses the PVF [monster reward item] section.
// Each entry is a 7-tuple: monster_code dungeon_id difficulty item_id count
// drop_rate max_stack. dungeon_id -1 and difficulty -1 are wildcards.
func parseMonsterRewardItems(doc *dnfpvf.Document) []MonsterRewardItemEntry {
	tokens, ok := doc.Section("monster reward item")
	if !ok || len(tokens) == 0 {
		return nil
	}
	ints := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenInt {
			ints = append(ints, token.Int)
		}
	}
	entries := make([]MonsterRewardItemEntry, 0, len(ints)/7)
	for i := 0; i+6 < len(ints); i += 7 {
		entries = append(entries, MonsterRewardItemEntry{
			MonsterCode: ints[i],
			DungeonID:   ints[i+1],
			Difficulty:  ints[i+2],
			ItemID:      ints[i+3],
			Count:       ints[i+4],
			DropRate:    ints[i+5],
			MaxStack:    ints[i+6],
		})
	}
	return entries
}

// parseEnemyRewardItems parses the PVF [enemy reward item] section.
// Each entry is an 8-tuple: enemy_code enemy_type dungeon_id difficulty
// item_id count drop_rate max_stack. enemy_type 2 = AI character,
// 3 = passive object.
func parseEnemyRewardItems(doc *dnfpvf.Document) []EnemyRewardItemEntry {
	tokens, ok := doc.Section("enemy reward item")
	if !ok || len(tokens) == 0 {
		return nil
	}
	ints := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenInt {
			ints = append(ints, token.Int)
		}
	}
	entries := make([]EnemyRewardItemEntry, 0, len(ints)/8)
	for i := 0; i+7 < len(ints); i += 8 {
		entries = append(entries, EnemyRewardItemEntry{
			EnemyCode:  ints[i],
			EnemyType:  ints[i+1],
			DungeonID:  ints[i+2],
			Difficulty: ints[i+3],
			ItemID:     ints[i+4],
			Count:      ints[i+5],
			DropRate:   ints[i+6],
			MaxStack:   ints[i+7],
		})
	}
	return entries
}

func parseTargetCharacterRules(tokens []dnfpvf.Token) []TargetCharacterRule {
	rules := make([]TargetCharacterRule, 0, len(tokens)/3)
	for index := 0; index+2 < len(tokens); {
		jobToken := tokens[index]
		if (jobToken.Kind != dnfpvf.TokenString && jobToken.Kind != dnfpvf.TokenIdent) ||
			tokens[index+1].Kind != dnfpvf.TokenInt || tokens[index+2].Kind != dnfpvf.TokenInt {
			index++
			continue
		}
		rules = append(rules, TargetCharacterRule{
			JobTag: jobToken.Value, FirstGrowType: boundedInt(tokens[index+1].Int), AwakeningStage: boundedInt(tokens[index+2].Int),
		})
		index += 3
	}
	return rules
}

func rewardRuleMatches(rule RewardItemRule, character CharacterEligibility) bool {
	if rule.ItemID <= 0 || rule.Count <= 0 {
		return false
	}
	if !rule.HasJobFilter {
		return true
	}
	return rule.Job == character.Job && (rule.GrowType < 0 || rule.GrowType == character.GrowType&0x0f)
}

// definitionInitialTrigger ports the C# QuestData.ComputeInitTrigger domain
// rules.  The current EXE accepts the resulting u32 through the existing
// typed accept ACK; C# packet layouts are not used here.
//
// C# intentionally uses one as the fallback for a recognised-but-not-yet
// specialised quest type.  Returning a fake "unsupported" error after the
// task was advertised in the acceptable-quest list made the second main-line
// task (3146) impossible to accept.
func definitionInitialTrigger(definition Definition, completed map[int64]struct{}) uint32 {
	tag := normalizeQuestTag(definition.Type)
	switch tag {
	case "seek n meet npc":
		return seekAndMeetNPCInitialTrigger(definition.IntData)
	case "quest clear", "clear quest":
		return questClearInitialTrigger(definition.IntData, completed)
	case "condition under clear":
		// The live PVF's subtype 6 uses (dungeon, minimum difficulty,
		// required clears), e.g. 5478 needs three and 8512 needs six.
		// A four-integer stride silently initialized every such task to one.
		if definition.SubType == 6 && len(definition.IntData) == 3 && definition.IntData[2] > 0 {
			return boundedTriggerChannel(definition.IntData[2])
		}
		return triggerFromIntData(definition.IntData, 4)
	case "clear map":
		return triggerFromIntData(definition.IntData, 4)
	case "condition under clear2":
		return triggerFromIntData(definition.IntData, 5)
	case "normal clear":
		return packTriggerChannels(1, 1, 0)
	case "raid phase clear":
		// The current PVF's four-field rows put required clears at offset 2.
		// This initialization alone does not establish other fields' semantics.
		// The wire trigger packs remaining counts, not completed counts.
		counts := make([]int64, 0, 3)
		for offset := 2; offset < len(definition.IntData) && len(counts) < 3; offset += 4 {
			counts = append(counts, definition.IntData[offset])
		}
		return packTriggerChannels(counts...)
	case "hunt monster":
		return triggerFromIntData(definition.IntData, 4)
	case "hunt enemy":
		return triggerFromIntData(definition.IntData, 5)
	}
	if csharpTypeOneQuest(tag) && definition.SubType == 6 && len(definition.IntData) >= 3 && definition.IntData[2] > 0 {
		return boundedTriggerChannel(definition.IntData[2])
	}
	return 1
}

func (c *Catalog) linkedSubQuestAcceptPlans(definition Definition, character CharacterEligibility, record dnfrepo.QuestRecord, completed, active map[int64]struct{}) []AcceptLinkedSubQuestPlan {
	if c == nil {
		return nil
	}
	if !questClearDefinition(definition) {
		return nil
	}
	seen := make(map[int64]struct{}, len(definition.IntData))
	plans := make([]AcceptLinkedSubQuestPlan, 0, len(definition.IntData))
	for _, childID := range definition.IntData {
		if childID <= 0 {
			continue
		}
		if _, duplicate := seen[childID]; duplicate {
			continue
		}
		seen[childID] = struct{}{}
		if _, exists := completed[childID]; exists {
			continue
		}
		if _, exists := active[childID]; exists {
			continue
		}
		child, ok := c.byID[childID]
		if !ok || !linkedQuestChainChild(definition, child) {
			continue
		}
		if !shouldAutoCompleteNoRewardSubQuest(child) {
			continue
		}
		// The child is neither completed nor active here (both sets were checked
		// above), so the shared PVF/state rule alone decides; the parent's accept
		// owner re-runs the full DecideAccept preflight before persisting.
		if !definitionAcceptable(c, child, character, record, completed, active, false) {
			continue
		}
		active[childID] = struct{}{}
		plans = append(plans, AcceptLinkedSubQuestPlan{
			QuestID:     child.ID,
			Path:        child.Path,
			Type:        child.Type,
			InitTrigger: definitionInitialTrigger(child, completed),
		})
	}
	return plans
}

func seekAndMeetNPCInitialTrigger(values []int64) uint32 {
	if len(values) < 3 {
		return 1
	}
	meetNPCCount := int64(0)
	if values[len(values)-1] > 0 {
		meetNPCCount = 1
	}
	// The client evaluates all item/count pairs as one material predicate.
	// Its satisfied branch sends one decrement per remaining low-channel
	// unit; putting an item quantity here amplifies snapshot callbacks.
	return packTriggerChannels(1, meetNPCCount, 0)
}

func questClearInitialTrigger(values []int64, completed map[int64]struct{}) uint32 {
	missing := int64(0)
	for _, questID := range values {
		if questID > 0 {
			if _, exists := completed[questID]; !exists {
				missing++
			}
		}
	}
	if missing == 0 {
		return 1
	}
	return boundedTriggerChannel(missing)
}

func csharpTypeOneQuest(tag string) bool {
	switch tag {
	case "seeking", "hunt monster", "meet npc", "hunt enemy", "use item", "get item", "get score",
		"clear quest", "quest clear", "custom quest", "send chatting", "check life", "amplify item",
		"disjoint item", "equipped item", "check time", "use fortune coin", "meet secret npc",
		"turn gold card", "ui click", "seek n meet npc", "assault count", "mobile":
		return true
	default:
		return false
	}
}

func triggerFromIntData(values []int64, stride int) uint32 {
	if len(values) == 0 || stride <= 0 {
		return 1
	}
	counts := make([]int64, 0, 3)
	for offset := stride - 1; offset < len(values) && len(counts) < 3; offset += stride {
		counts = append(counts, values[offset])
	}
	if len(counts) == 0 {
		return 1
	}
	return packTriggerChannels(counts...)
}

func packTriggerChannels(values ...int64) uint32 {
	var packed uint32
	for index, value := range values {
		if index == 3 {
			break
		}
		packed |= boundedTriggerChannel(value) << (index * 9)
	}
	return packed
}

func boundedTriggerChannel(value int64) uint32 {
	if value <= 0 {
		return 0
	}
	if value > 0x1ff {
		return 0x1ff
	}
	return uint32(value)
}

func definitionAcceptable(catalog *Catalog, definition Definition, character CharacterEligibility, record dnfrepo.QuestRecord, completed, active map[int64]struct{}, includeActive bool) bool {
	_, alreadyActive := active[definition.ID]
	allowedEpicRoad := character.epicRoadEvent350Recognized && (character.epicRoadEvent350Enabled || alreadyActive) && (definition.ID == 5578 || definition.ID == 5579)
	if definition.ExposedByNPC == 0 || (definition.IsEvent && !allowedEpicRoad) || definition.HasCreatureRequirement || definition.HasExpertRequirement {
		return false
	}
	grade := normalizeQuestTag(definition.Grade)
	if !(selectableGrade(grade) || allowedEpicRoad && grade == "special") || grade == "training" {
		return false
	}
	if character.Level < definition.LevelMin || character.Level > definition.LevelMax {
		return false
	}
	// The renewal forest and the old NPC chains can award the same class
	// through different terminal IDs. A completed forest terminal therefore
	// does not satisfy the old chain's collision list. Match the existing
	// profession transition rule before offering another initial-class reward.
	// Keep active rows visible so a task accepted before a GM class change can
	// still be abandoned, and keep story/follow-up nodes that grant no class.
	if !alreadyActive && character.GrowType != 0 && definition.JobChangeQuest == 1 &&
		normalizeQuestTag(definition.RewardType) == "grow type" {
		return false
	}
	if !definitionMatchesCharacter(definition, character) {
		return false
	}
	_, isActive := active[definition.ID]
	if isActive && !includeActive {
		return false
	}
	if !isActive && !classChangeRewardAcceptable(definition, character) {
		return false
	}
	if !isActive && !awakeningRewardAcceptable(definition, character) {
		return false
	}
	// 每日任务的“隔天重新可接”由 ProjectDailyQuestRollover 负责：它只把更早服务日
	// 的完成投影掉，这里的完成判定保持既有规则。grade daily 在 90CN 侧被钉为“立即
	// 重复”（repeatableGrade，见 client_acceptable_preflight_test.go 的 5020 回归）；
	// 真档安徒恩每日任务用的 daily mission 不在 repeatable 里，同一天完成后仍不可
	// 再接，隔天完成行过期后重新进 op21 列表。两档行为都不改。
	if _, exists := completed[definition.ID]; exists && !repeatableGrade(grade) {
		return false
	}
	if len(definition.PreRequiredGroups) > 0 {
		matched := false
		for _, group := range definition.PreRequiredGroups {
			if completedAll(group, completed) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(definition.PreRequiredAnswers) > 0 {
		if !questionAnswerPrerequisiteSatisfied(catalog, record, definition.PreRequiredAnswers) {
			return false
		}
	}
	for _, collision := range definition.CollisionQuests {
		if collision <= 0 {
			continue
		}
		if _, exists := completed[collision]; exists {
			return false
		}
	}
	return true
}

// classChangeRewardAcceptable 用 profession 的域规则判断一条 [reward type] [grow type]
// 转职任务是否还可能被当前角色结算成功。真档 15 级转职任务（例：7803 剑魂、7814 阿修罗、
// 2633 决战者）的 PVF 只有 [job] 与 [grow type] -1，没有转职阶段门槛，所以已经转职
// （first != 0）或已觉醒的角色过去仍能接取它们，而 Owner.ApplyFinishSettlement 的
// PlanTransition 必然以 ErrTransitionOutOfStep 拒绝，形成永远做不完的 active 任务。
// 这里复用 profession.CheckClassChangePrecondition（PlanTransition 的同一前置）把
// 接取预检与结算对齐，并让可接列表投影同步收敛；觉醒任务与分支主线（[item] 奖励）
// 不在此判断范围内。
func classChangeRewardAcceptable(definition Definition, character CharacterEligibility) bool {
	request, isClassChange := classChangeRequest(definition)
	if !isClassChange {
		// 解析失败或不是转职链：保持既有可接取判定，不在这里发明新的拒绝条件。
		return true
	}
	if character.GrowType < 0 || character.GrowType > math.MaxUint8 {
		return false
	}
	return profession.CheckClassChangePrecondition(byte(character.GrowType), request) == nil
}

// classChangeRequest 解析一条 [reward type] [grow type] 转职任务的结算请求。
// 奖励类型不是 [grow type]、解析失败或不是 class change 链时第二个返回值为 false。
func classChangeRequest(definition Definition) (profession.Request, bool) {
	if normalizeQuestTag(definition.RewardType) != "grow type" {
		return profession.Request{}, false
	}
	request, err := profession.ParseReward(definition.JobChangeQuest, definition.RewardType, definition.RewardIntData)
	if err != nil || request.Kind != profession.KindClassChange {
		return profession.Request{}, false
	}
	return request, true
}

// awakeningRewardAcceptable 用 profession 的域规则判断一条 [reward type] awakening type
// 的觉醒任务是否还可能被当前角色结算成功。真档部分觉醒奖励任务按转职分支筛选，
// 不限制觉醒阶段；重复一觉或跳过一觉的二觉奖励仍会被结算阶段规则拒绝。
// 这里复用 profession.CheckAwakeningPrecondition（PlanTransition 的同一前置），让新接取、
// 结算与可接列表投影一致；合法的第一次/第二次觉醒不受影响。
func awakeningRewardAcceptable(definition Definition, character CharacterEligibility) bool {
	if normalizeQuestTag(definition.RewardType) != "awakening type" {
		return true
	}
	request, err := profession.ParseReward(definition.JobChangeQuest, definition.RewardType, definition.RewardIntData)
	if err != nil || request.Kind != profession.KindAwakening {
		// 解析失败：保持既有可接取判定，不在这里发明新的拒绝条件。
		return true
	}
	if character.GrowType < 0 || character.GrowType > math.MaxUint8 {
		return false
	}
	return profession.CheckAwakeningPrecondition(byte(character.GrowType), request) == nil
}

// activeClassChangeConflict rejects a second unsettled class-change quest only
// when both definitions are class changes and their PVF collision declarations
// name each other. Ordinary quests and item-reward branches remain independent.
// Existing active quests return AcceptIdempotent before this check.
func (c *Catalog) activeClassChangeConflict(definition Definition, active map[int64]struct{}) bool {
	if _, isClassChange := classChangeRequest(definition); !isClassChange {
		return false
	}
	for id := range active {
		if id == definition.ID {
			continue
		}
		other, known := c.byID[id]
		if !known {
			continue
		}
		if _, isClassChange := classChangeRequest(other); !isClassChange {
			continue
		}
		if !mutualQuestCollision(definition, other) {
			continue
		}
		return true
	}
	return false
}

// mutualQuestCollision 判断两条任务是否在 PVF 的 [collision quest] 里互相点名。
// 只认双向声明：单向的历史/畸形数据不足以证明两个家族互斥。
func mutualQuestCollision(left, right Definition) bool {
	return declaresQuestCollision(left, right.ID) && declaresQuestCollision(right, left.ID)
}

func declaresQuestCollision(definition Definition, otherID int64) bool {
	for _, id := range definition.CollisionQuests {
		if id == otherID {
			return true
		}
	}
	return false
}

// definitionMatchesCharacter is the shared PVF job/grow-type predicate for
// ordinary acceptance and server-owned main-quest skip planning. It contains
// no level, prerequisite, collision, event, or quest-state policy.
func definitionMatchesCharacter(definition Definition, character CharacterEligibility) bool {
	if job := strings.TrimSpace(definition.Job); job != "" && normalizeQuestTag(job) != "all" && !jobmap.MatchesQuestTag(job, character.Job) {
		return false
	}
	// 普通支线同样使用职业、转职分支及觉醒阶段三元组。只对转职任务
	// 检查会让帕拉丁同时接到精灵骑士、混沌魔灵的同名盾牌任务。
	if len(definition.TargetCharacterRules) != 0 {
		matched := false
		firstGrowType := character.GrowType & 0x0f
		awakeningStage := (character.GrowType >> 4) & 0x0f
		for _, rule := range definition.TargetCharacterRules {
			// Actual forest transfer quests use 0/-1 and existing-profession
			// variants use -1/-1. -1 leaves that tuple component unrestricted;
			// it cannot equal a packed grow-type nibble (0..15).
			if jobmap.MatchesQuestTag(rule.JobTag, character.Job) &&
				(rule.FirstGrowType == -1 || rule.FirstGrowType == firstGrowType) &&
				(rule.AwakeningStage == -1 || rule.AwakeningStage == awakeningStage) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	} else if target := strings.TrimSpace(definition.TargetCharacter); target != "" && !jobmap.MatchesQuestTag(target, character.Job) {
		return false
	}
	if definition.HasGrowType && definition.GrowType >= 0 {
		switch definition.JobChangeQuest {
		case 1, 10, 20:
		case 2, 3:
			if definition.GrowType != character.GrowType&0x0f {
				return false
			}
		default:
			if definition.GrowType != character.GrowType {
				return false
			}
		}
	}
	return true
}

func questStateSets(record dnfrepo.QuestRecord) (map[int64]struct{}, map[int64]struct{}) {
	completed := make(map[int64]struct{})
	active := make(map[int64]struct{})
	collect := func(states map[int64]dnfrepo.QuestState) {
		for id, state := range states {
			if id <= 0 {
				continue
			}
			if isCompletedQuestStatus(state.Status) {
				completed[id] = struct{}{}
				continue
			}
			if isActiveQuestStatus(state.Status) {
				active[id] = struct{}{}
			}
		}
	}
	collect(record.States)
	collect(record.Progress)
	return completed, active
}

func isCompletedQuestStatus(status string) bool {
	switch normalizeStatus(status) {
	case "complete", "completed", "cleared", "finished", "done":
		return true
	default:
		return false
	}
}

func normalizeStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func selectableGrade(grade string) bool {
	switch grade {
	case "", "normal", "side", "sub", "epic", "realization", "training", "achievement", "mission", "daily", "daily random", "normaly repeat", "special daily", "common unique", "system":
		return true
	default:
		return strings.HasPrefix(grade, "daily") || strings.HasPrefix(grade, "special daily")
	}
}

func repeatableGrade(grade string) bool {
	return grade == "daily" || grade == "normaly repeat" || grade == "special daily" || strings.HasPrefix(grade, "special daily")
}

func normalizeQuestTag(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) >= 2 && value[0] == '[' && value[len(value)-1] == ']' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func sectionIntGroups(doc *dnfpvf.Document, name string) [][]int64 {
	if doc == nil {
		return nil
	}
	want := normalizeQuestTag(name)
	groups := make([][]int64, 0, 1)
	for _, section := range doc.Sections {
		if normalizeQuestTag(section.Name) != want || section.Start < 0 || section.End > len(doc.Tokens) || section.Start > section.End {
			continue
		}
		group := make([]int64, 0, section.End-section.Start)
		for _, token := range doc.Tokens[section.Start:section.End] {
			if token.Kind == dnfpvf.TokenInt {
				group = append(group, token.Int)
			}
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

func flattenIntGroups(groups [][]int64) []int64 {
	var values []int64
	for _, group := range groups {
		values = append(values, group...)
	}
	return values
}

func completedAll(ids []int64, completed map[int64]struct{}) bool {
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := completed[id]; !exists {
			return false
		}
	}
	return true
}

func boundedInt(value int64) int {
	if value > int64(math.MaxInt) {
		return math.MaxInt
	}
	if value < int64(math.MinInt) {
		return math.MinInt
	}
	return int(value)
}

func cloneDefinition(definition Definition) Definition {
	definition.IntData = append([]int64(nil), definition.IntData...)
	definition.CheckCount = append([]int64(nil), definition.CheckCount...)
	definition.ConditionData = append([]int64(nil), definition.ConditionData...)
	definition.DependGiveItemData = append([]int64(nil), definition.DependGiveItemData...)
	definition.PreRequiredGroups = append([][]int64(nil), definition.PreRequiredGroups...)
	for index := range definition.PreRequiredGroups {
		definition.PreRequiredGroups[index] = append([]int64(nil), definition.PreRequiredGroups[index]...)
	}
	definition.PreRequiredAnswers = append([]int64(nil), definition.PreRequiredAnswers...)
	definition.CollisionQuests = append([]int64(nil), definition.CollisionQuests...)
	definition.RewardIntData = append([]int64(nil), definition.RewardIntData...)
	definition.RewardItems = append([]RewardItemRule(nil), definition.RewardItems...)
	definition.RewardSelectionItems = append([]RewardItemRule(nil), definition.RewardSelectionItems...)
	definition.TargetCharacterRules = append([]TargetCharacterRule(nil), definition.TargetCharacterRules...)
	definition.MonsterRewardItems = append([]MonsterRewardItemEntry(nil), definition.MonsterRewardItems...)
	definition.EnemyRewardItems = append([]EnemyRewardItemEntry(nil), definition.EnemyRewardItems...)
	return definition
}

func SortedIDs(result EligibilityResult) []int32 {
	ids := append([]int32(nil), result.IDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ExEquipSlotStat is the durable current-client bitset selected by the runtime
// PVF [slot expansion] reward index. Current Script.pvf uses indices 0, 1, and
// 2, but the current NoPack UI bits are deliberately not contiguous: its
// support/magic-stone/earring checks use 0x01, 0x02, and 0x10 respectively.
const (
	ExEquipSlotSupport    byte = 0x01
	ExEquipSlotMagicStone byte = 0x02
	ExEquipSlotEarring    byte = 0x10
	ExEquipSlotAll        byte = ExEquipSlotSupport | ExEquipSlotMagicStone | ExEquipSlotEarring
)

func ExEquipSlotBitForPVFIndex(index uint32) (byte, bool) {
	switch index {
	case 0:
		return ExEquipSlotSupport, true
	case 1:
		return ExEquipSlotMagicStone, true
	case 2:
		return ExEquipSlotEarring, true
	default:
		return 0, false
	}
}

// QuestIDsBySlotExpansionIndex 返回运行时 PVF 里全部「`[reward type] `[slot
// expansion]“ 且 `[reward int data]` 索引等于 index」的任务 id，按 id 升序。
//
// 它是 GM 面板「开启特殊装备槽」的唯一 id 来源：真档 Script.pvf 里这样的任务
// 只有三个（0=support 649、1=magic stone 650、2=earring 2636），但工具的写库
// 目标一律从这里取，而不是把 id 抄进工具 —— PVF 换版后自动跟着走，取不到就
// 让调用方显式报错，绝不把位开到别的槽上。
func (c *Catalog) QuestIDsBySlotExpansionIndex(index uint32) []int64 {
	if c == nil {
		return nil
	}
	ids := make([]int64, 0, 2)
	for _, definition := range c.ordered {
		if normalizeQuestTag(definition.RewardType) != "slot expansion" {
			continue
		}
		if len(definition.RewardIntData) != 1 || definition.RewardIntData[0] < 0 {
			continue
		}
		if uint32(definition.RewardIntData[0]) != index {
			continue
		}
		ids = append(ids, definition.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
