package experienceitem

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/pvf"
)

var ErrCapsule = errors.New("invalid experience capsule definition")

type CapsuleKind uint8

const (
	CapsuleFixed CapsuleKind = iota + 1
	CapsulePercent
	CapsuleTable
	CapsuleCrack
)

type CapsuleBand struct {
	Minimum int
	Maximum int
	Amount  uint32
}

type Capsule struct {
	ItemID           int64
	PVFPath          string
	Kind             CapsuleKind
	Amount           uint32
	MinimumLevel     int
	MaximumLevel     int
	Jobs             []string
	ExcludedJobs     []string
	Places           []string
	FirstGrowTypes   []int
	CoolTimeMS       int64
	DisallowHardcore bool
	Bands            []CapsuleBand
}

// Validate is the domain-side gate for definitions constructed by either the
// PVF parser or a bridge. It deliberately rejects non-canonical job tags so a
// caller cannot bypass parse-time normalization.
func (d Capsule) Validate() error {
	if d.ItemID <= 0 || d.ItemID > int64(math.MaxUint32) {
		return fmt.Errorf("%w: item=%d", ErrCapsule, d.ItemID)
	}
	if strings.TrimSpace(d.PVFPath) == "" {
		return fmt.Errorf("%w: item=%d pvf path is empty", ErrCapsule, d.ItemID)
	}
	if d.MinimumLevel < 1 || d.MaximumLevel < d.MinimumLevel || d.MaximumLevel > 255 {
		return fmt.Errorf(
			"%w: item=%d levels=%d..%d",
			ErrCapsule,
			d.ItemID,
			d.MinimumLevel,
			d.MaximumLevel,
		)
	}
	if d.CoolTimeMS < 0 || d.CoolTimeMS > math.MaxUint32 {
		return fmt.Errorf("%w: item=%d cooltime=%d", ErrCapsule, d.ItemID, d.CoolTimeMS)
	}
	if len(d.Jobs) == 0 {
		return fmt.Errorf("%w: item=%d jobs are empty", ErrCapsule, d.ItemID)
	}
	for _, tag := range d.Jobs {
		canonical, known := canonicalJobTag(tag, false)
		if !known || canonical != tag {
			return fmt.Errorf("%w: item=%d usable job %q", ErrCapsule, d.ItemID, tag)
		}
	}
	for _, tag := range d.ExcludedJobs {
		canonical, known := canonicalJobTag(tag, false)
		if !known || canonical != tag || canonical == "[all]" {
			return fmt.Errorf("%w: item=%d excluded job %q", ErrCapsule, d.ItemID, tag)
		}
	}
	for _, place := range d.Places {
		canonical, valid := canonicalPlace(place)
		if !valid || canonical != place {
			return fmt.Errorf("%w: item=%d place %q", ErrCapsule, d.ItemID, place)
		}
	}
	seenGrow := make(map[int]struct{}, len(d.FirstGrowTypes))
	for _, grow := range d.FirstGrowTypes {
		if grow < 0 || grow > jobmap.MaxID {
			return fmt.Errorf("%w: item=%d first grow type %d", ErrCapsule, d.ItemID, grow)
		}
		if _, duplicate := seenGrow[grow]; duplicate {
			return fmt.Errorf("%w: item=%d duplicate first grow type %d", ErrCapsule, d.ItemID, grow)
		}
		seenGrow[grow] = struct{}{}
	}
	if len(d.Bands) > 0 && d.Kind != CapsuleTable {
		return fmt.Errorf("%w: item=%d non-table kind has bands", ErrCapsule, d.ItemID)
	}
	switch d.Kind {
	case CapsuleFixed:
		if d.Amount == 0 {
			return fmt.Errorf("%w: item=%d fixed amount is zero", ErrCapsule, d.ItemID)
		}
	case CapsulePercent:
		if d.Amount < 1 || d.Amount > 100 {
			return fmt.Errorf("%w: item=%d percent=%d", ErrCapsule, d.ItemID, d.Amount)
		}
	case CapsuleTable:
		if d.Amount != 0 || len(d.Bands) == 0 {
			return fmt.Errorf("%w: item=%d table is empty", ErrCapsule, d.ItemID)
		}
		previousMaximum := 0
		for index, band := range d.Bands {
			if band.Minimum < 1 || band.Maximum < band.Minimum || band.Maximum > 255 || band.Amount == 0 {
				return fmt.Errorf("%w: item=%d band=%d", ErrCapsule, d.ItemID, index)
			}
			if index > 0 && band.Minimum <= previousMaximum {
				return fmt.Errorf("%w: item=%d bands overlap at %d", ErrCapsule, d.ItemID, index)
			}
			previousMaximum = band.Maximum
		}
	case CapsuleCrack:
		if d.Amount != 0 {
			return fmt.Errorf("%w: item=%d crack amount=%d", ErrCapsule, d.ItemID, d.Amount)
		}
	default:
		return fmt.Errorf("%w: item=%d kind=%d", ErrCapsule, d.ItemID, d.Kind)
	}
	return nil
}

// ParseCapsule recognizes only root [increase status type] experience effects.
// An unknown status tag returns found=false so skill books and other legacy
// INCREASE_STATUS handlers keep their existing path. Once one of the five
// capsule tags is recognized, every malformed field is a found=true error.
func ParseCapsule(id int64, doc *pvf.Document) (Capsule, bool, error) {
	if doc == nil {
		return Capsule{}, false, nil
	}

	statusSections, err := rootSectionIndices(doc, "increase status type")
	if err != nil {
		return Capsule{}, false, nil
	}
	if len(statusSections) == 0 {
		return Capsule{}, false, nil
	}

	recognized := false
	for _, sectionIndex := range statusSections {
		tokens, tokenErr := sectionTokens(doc, sectionIndex)
		if tokenErr != nil {
			continue
		}
		for _, token := range tokens {
			if isTextToken(token) && isCapsuleStatusTag(token.Value) {
				recognized = true
				break
			}
		}
	}
	if !recognized {
		return Capsule{}, false, nil
	}

	if err := validateCapsuleSections(doc); err != nil {
		return Capsule{}, true, err
	}
	statusTokens, found, err := uniqueSectionTokens(doc, "increase status type")
	if err != nil || !found {
		return Capsule{}, true, fmt.Errorf("%w: item=%d increase status section", ErrCapsule, id)
	}
	kind, amount, bands, err := parseCapsuleEffect(id, statusTokens)
	if err != nil {
		return Capsule{}, true, err
	}

	stackable, found, err := uniqueSectionTokens(doc, "stackable type")
	if err != nil || !found || !isCapsuleETCVariant(stackable) {
		return Capsule{}, true, fmt.Errorf("%w: item=%d stackable type must be [etc] 0 or 1", ErrCapsule, id)
	}

	minimumLevel, err := parseCapsuleLevel(doc, "minimum level", 1, true)
	if err != nil {
		return Capsule{}, true, err
	}
	maximumLevel, err := parseCapsuleLevel(doc, "maximum level", 255, false)
	if err != nil {
		return Capsule{}, true, err
	}

	jobsTokens, found, err := uniqueSectionTokens(doc, "usable job")
	if err != nil || !found {
		return Capsule{}, true, fmt.Errorf("%w: item=%d usable job section", ErrCapsule, id)
	}
	jobs, err := parseCapsuleJobs(jobsTokens, false)
	if err != nil || len(jobs) == 0 {
		return Capsule{}, true, fmt.Errorf("%w: item=%d usable jobs: %v", ErrCapsule, id, err)
	}

	excludedTokens, excludedFound, err := uniqueSectionTokens(doc, "impossible jobs")
	if err != nil {
		return Capsule{}, true, err
	}
	var excluded []string
	if excludedFound {
		excluded, err = parseCapsuleJobs(excludedTokens, true)
		if err != nil {
			return Capsule{}, true, err
		}
	}

	placesTokens, placesFound, err := uniqueSectionTokens(doc, "action usable place")
	if err != nil {
		return Capsule{}, true, err
	}
	places, err := parseCapsulePlaces(placesTokens, placesFound)
	if err != nil {
		return Capsule{}, true, err
	}

	growTokens, growFound, err := uniqueSectionTokens(doc, "usable first growtype")
	if err != nil {
		return Capsule{}, true, err
	}
	firstGrowTypes, err := parseFirstGrowTypes(growTokens, growFound)
	if err != nil {
		return Capsule{}, true, err
	}

	coolTimeMS, err := parseCapsuleCoolTime(doc)
	if err != nil {
		return Capsule{}, true, err
	}
	disallowHardcore, err := parseImpossibleContents(doc)
	if err != nil {
		return Capsule{}, true, err
	}

	capsule := Capsule{
		ItemID:           id,
		PVFPath:          doc.Path,
		Kind:             kind,
		Amount:           amount,
		MinimumLevel:     minimumLevel,
		MaximumLevel:     maximumLevel,
		Jobs:             jobs,
		ExcludedJobs:     excluded,
		Places:           places,
		FirstGrowTypes:   firstGrowTypes,
		CoolTimeMS:       coolTimeMS,
		DisallowHardcore: disallowHardcore,
		Bands:            bands,
	}
	if err := capsule.Validate(); err != nil {
		return Capsule{}, true, err
	}
	return capsule, true, nil
}

var capsuleSectionNames = map[string]struct{}{
	"name": {}, "explain": {}, "grade": {}, "rarity": {},
	"usable job": {}, "attach type": {}, "minimum level": {},
	"icon": {}, "field image": {}, "stackable type": {},
	"item group name": {}, "move wav": {}, "need material": {},
	"stack limit": {}, "increase status type": {}, "maximum level": {},
	"item category": {}, "impossible contents": {}, "flavor text": {},
	"impossible jobs": {}, "expiration date": {}, "usable period": {},
	"icon mark": {}, "usable expired item": {}, "daily purchase limit": {},
	"cool time": {}, "daily delete item": {}, "reset daily purchase item": {},
	"weight": {}, "action usable place": {}, "display exp up info": {},
	"purchase limit": {}, "npc gift disallowance": {}, "usable first growtype": {},
	"use wav": {}, "name2": {},
	// price 是道具的卖店价/商店价，纯交易数值，和胶囊行为无关。
	// 早期漏了它，导致「带卖店价的胶囊」一律被判为未知段而无法使用
	// （线上 10005308「(50百分比)兔兔成长胶囊」同时有 [price] 500000 与
	// [need material] 10003231 1，表示购买价，与使用消耗无关）。
	"price": {},
}

func isCapsuleStatusTag(value string) bool {
	switch normalizedStatusTag(value) {
	case "expup", "exppercentup", "expupbypercent", "expupbytable", "expupbycrackofdimension":
		return true
	default:
		return false
	}
}

func parseCapsuleEffect(id int64, tokens []pvf.Token) (CapsuleKind, uint32, []CapsuleBand, error) {
	if len(tokens) == 0 || !isTextToken(tokens[0]) {
		return 0, 0, nil, fmt.Errorf("%w: item=%d effect tag", ErrCapsule, id)
	}
	tag := normalizedStatusTag(tokens[0].Value)
	values := make([]int64, 0, len(tokens)-1)
	for _, token := range tokens[1:] {
		if token.Kind != pvf.TokenInt {
			return 0, 0, nil, fmt.Errorf("%w: item=%d effect values must be integers", ErrCapsule, id)
		}
		values = append(values, token.Int)
	}

	switch tag {
	case "expup":
		if len(values) != 1 {
			return 0, 0, nil, fmt.Errorf("%w: item=%d fixed values=%d", ErrCapsule, id, len(values))
		}
		amount, err := capsulePositiveU32(values[0])
		return CapsuleFixed, amount, nil, err
	case "exppercentup", "expupbypercent":
		if len(values) != 1 || values[0] < 1 || values[0] > 100 {
			return 0, 0, nil, fmt.Errorf("%w: item=%d percent=%v", ErrCapsule, id, values)
		}
		return CapsulePercent, uint32(values[0]), nil, nil
	case "expupbytable":
		bands, err := parseCapsuleBands(values)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("%w: item=%d table: %v", ErrCapsule, id, err)
		}
		return CapsuleTable, 0, bands, nil
	case "expupbycrackofdimension":
		if len(values) == 0 {
			return CapsuleCrack, 0, nil, nil
		}
		if len(values) != 1 || values[0] < 0 {
			return 0, 0, nil, fmt.Errorf("%w: item=%d crack values=%v", ErrCapsule, id, values)
		}
		if values[0] == 0 {
			return CapsuleCrack, 0, nil, nil
		}
		amount, err := capsulePositiveU32(values[0])
		return CapsuleFixed, amount, nil, err
	default:
		return 0, 0, nil, fmt.Errorf("%w: item=%d unknown recognized effect %q", ErrCapsule, id, tag)
	}
}

func parseCapsuleBands(values []int64) ([]CapsuleBand, error) {
	if len(values) == 0 || len(values)%3 != 0 {
		return nil, fmt.Errorf("integer count %d", len(values))
	}
	bands := make([]CapsuleBand, 0, len(values)/3)
	previousMaximum := int64(0)
	for index := 0; index < len(values); index += 3 {
		minimum, maximum := values[index], values[index+1]
		if minimum < 1 || minimum > 255 || maximum < minimum || maximum > 255 {
			return nil, fmt.Errorf("band %d levels=%d..%d", index/3, minimum, maximum)
		}
		if len(bands) > 0 && minimum <= previousMaximum {
			return nil, fmt.Errorf("band %d overlaps or is not increasing", index/3)
		}
		amount, err := capsulePositiveU32(values[index+2])
		if err != nil {
			return nil, fmt.Errorf("band %d amount: %w", index/3, err)
		}
		bands = append(bands, CapsuleBand{Minimum: int(minimum), Maximum: int(maximum), Amount: amount})
		previousMaximum = maximum
	}
	return bands, nil
}

func capsulePositiveU32(value int64) (uint32, error) {
	if value <= 0 || value > int64(math.MaxUint32) {
		return 0, fmt.Errorf("%w: amount=%d", ErrCapsule, value)
	}
	return uint32(value), nil
}

func parseCapsuleLevel(doc *pvf.Document, name string, defaultValue int, normalizeZero bool) (int, error) {
	tokens, found, err := uniqueSectionTokens(doc, name)
	if err != nil {
		return 0, err
	}
	if !found {
		return defaultValue, nil
	}
	if len(tokens) != 1 || tokens[0].Kind != pvf.TokenInt || tokens[0].Int < 0 || tokens[0].Int > 255 {
		return 0, fmt.Errorf("%w: %s must be one integer in 0..255", ErrCapsule, name)
	}
	value := int(tokens[0].Int)
	if normalizeZero && value == 0 {
		value = 1
	}
	return value, nil
}

func parseCapsuleJobs(tokens []pvf.Token, allowUnbracketed bool) ([]string, error) {
	jobs := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !isTextToken(token) {
			return nil, fmt.Errorf("%w: job tag must be text", ErrCapsule)
		}
		tag, known := canonicalJobTag(token.Value, allowUnbracketed)
		if !known {
			return nil, fmt.Errorf("%w: unknown job tag %q", ErrCapsule, token.Value)
		}
		jobs = append(jobs, tag)
	}
	return jobs, nil
}

func canonicalJobTag(value string, allowUnbracketed bool) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = "[" + strings.TrimSpace(value[1:len(value)-1]) + "]"
	} else {
		if !allowUnbracketed || strings.ContainsAny(value, "[]") {
			return "", false
		}
		value = "[" + value + "]"
	}
	if value == "[all]" {
		return value, true
	}
	for id := 0; id <= jobmap.MaxID; id++ {
		for _, known := range jobmap.QuestTags(id) {
			if value == known {
				return value, true
			}
		}
	}
	return "", false
}

func parseCapsulePlaces(tokens []pvf.Token, found bool) ([]string, error) {
	if !found || len(tokens) == 0 {
		return nil, nil
	}
	places := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !isTextToken(token) {
			return nil, fmt.Errorf("%w: usable place must be text", ErrCapsule)
		}
		place, valid := canonicalPlace(token.Value)
		if !valid {
			return nil, fmt.Errorf("%w: unknown usable place %q", ErrCapsule, token.Value)
		}
		places = append(places, place)
	}
	return places, nil
}

func canonicalPlace(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		value = "[" + value + "]"
	}
	switch value {
	case "[village]", "[seria room]":
		return value, true
	default:
		return "", false
	}
}

func parseFirstGrowTypes(tokens []pvf.Token, found bool) ([]int, error) {
	if !found || len(tokens) == 0 {
		return nil, nil
	}
	out := make([]int, 0, len(tokens))
	seen := make(map[int]struct{}, len(tokens))
	for _, token := range tokens {
		if token.Kind != pvf.TokenInt || token.Int < 0 || token.Int > int64(jobmap.MaxID) {
			return nil, fmt.Errorf("%w: first grow type must be an integer in 0..%d", ErrCapsule, jobmap.MaxID)
		}
		value := int(token.Int)
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func parseCapsuleCoolTime(doc *pvf.Document) (int64, error) {
	tokens, found, err := uniqueSectionTokens(doc, "cool time")
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	if len(tokens) != 1 || tokens[0].Kind != pvf.TokenInt || tokens[0].Int < 0 || tokens[0].Int > math.MaxUint32 {
		return 0, fmt.Errorf("%w: cool time must be one non-negative uint32", ErrCapsule)
	}
	return tokens[0].Int, nil
}

func parseImpossibleContents(doc *pvf.Document) (bool, error) {
	disallowHardcore := false
	for _, section := range doc.Sections {
		if normalizedSectionName(section.Name) != "impossible contents" {
			continue
		}
		tokens, err := sectionTokensFor(doc, section)
		if err != nil {
			return false, err
		}
		for _, token := range tokens {
			if !isTextToken(token) {
				return false, fmt.Errorf("%w: impossible content must be text", ErrCapsule)
			}
			switch normalizedSectionName(token.Value) {
			case "hardcore mode":
				disallowHardcore = true
			case "gift", "ban redeemitem", "charac cargo":
				// Transfer-only constraints do not block use.
			default:
				return false, fmt.Errorf("%w: unknown impossible content %q", ErrCapsule, token.Value)
			}
		}
	}
	return disallowHardcore, nil
}

func validateCapsuleSections(doc *pvf.Document) error {
	for _, section := range doc.Sections {
		name := normalizedSectionName(section.Name)
		if strings.HasPrefix(name, "/") {
			name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
			if _, allowed := capsuleSectionNames[name]; !allowed {
				return fmt.Errorf("%w: unknown closing section %q", ErrCapsule, section.Name)
			}
			continue
		}
		if _, allowed := capsuleSectionNames[name]; !allowed {
			return fmt.Errorf("%w: unknown behavior section %q", ErrCapsule, section.Name)
		}
	}
	return nil
}

func uniqueSectionTokens(doc *pvf.Document, name string) ([]pvf.Token, bool, error) {
	name = normalizedSectionName(name)
	var out []pvf.Token
	found := false
	for _, section := range doc.Sections {
		if normalizedSectionName(section.Name) != name {
			continue
		}
		if found {
			return nil, true, fmt.Errorf("%w: duplicate %s section", ErrCapsule, name)
		}
		found = true
		var err error
		out, err = sectionTokensFor(doc, section)
		if err != nil {
			return nil, true, err
		}
	}
	return out, found, nil
}

func sectionTokens(doc *pvf.Document, sectionIndex int) ([]pvf.Token, error) {
	if doc == nil || sectionIndex < 0 || sectionIndex >= len(doc.Sections) {
		return nil, fmt.Errorf("%w: invalid section index", ErrCapsule)
	}
	return sectionTokensFor(doc, doc.Sections[sectionIndex])
}

func sectionTokensFor(doc *pvf.Document, section pvf.Section) ([]pvf.Token, error) {
	if doc == nil || section.Start < 0 || section.Start > section.End || section.End > len(doc.Tokens) {
		return nil, fmt.Errorf("%w: invalid section bounds", ErrCapsule)
	}
	return doc.Tokens[section.Start:section.End], nil
}

// rootSectionIndices distinguishes root sections from fields wrapped by an
// explicit [/section] marker. Leaf fields without a closing marker do not
// increase depth.
func rootSectionIndices(doc *pvf.Document, name string) ([]int, error) {
	name = normalizedSectionName(name)
	closedNames := make(map[string]struct{})
	for _, section := range doc.Sections {
		sectionName := normalizedSectionName(section.Name)
		if strings.HasPrefix(sectionName, "/") {
			closedNames[strings.TrimSpace(strings.TrimPrefix(sectionName, "/"))] = struct{}{}
		}
	}
	var indices []int
	var stack []string
	for index, section := range doc.Sections {
		sectionName := normalizedSectionName(section.Name)
		if strings.HasPrefix(sectionName, "/") {
			target := strings.TrimSpace(strings.TrimPrefix(sectionName, "/"))
			for cursor := len(stack) - 1; cursor >= 0; cursor-- {
				if stack[cursor] != target {
					continue
				}
				stack = stack[:cursor]
				break
			}
			continue
		}
		if sectionName == name && len(stack) == 0 {
			indices = append(indices, index)
		}
		if _, closes := closedNames[sectionName]; closes {
			stack = append(stack, sectionName)
		}
	}
	return indices, nil
}

func normalizedSectionName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizedStatusTag(value string) string {
	value = normalizedSectionName(value)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func isTextToken(token pvf.Token) bool {
	return token.Kind == pvf.TokenString || token.Kind == pvf.TokenIdent
}

func isCapsuleETCVariant(tokens []pvf.Token) bool {
	return len(tokens) == 2 &&
		isTextToken(tokens[0]) &&
		strings.EqualFold(strings.TrimSpace(tokens[0].Value), "[etc]") &&
		tokens[1].Kind == pvf.TokenInt &&
		(tokens[1].Int == 0 || tokens[1].Int == 1)
}
