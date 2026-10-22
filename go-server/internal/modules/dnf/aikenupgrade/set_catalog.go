package aikenupgrade

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

// ErrSetCatalog rejects a partsetcustominfo document whose structure or
// arithmetic inputs cannot be represented without guessing.
var ErrSetCatalog = errors.New("aiken set catalog rejected")

// ErrInsufficientSetPoints is returned when the sequential SpendPoints
// allocation would drive the remaining points below zero. The native path
// stops before it produces any material rows in that case.
var ErrInsufficientSetPoints = errors.New("aiken set points insufficient")

// ErrSetSelection rejects a selection that does not satisfy the part-set
// grade, part-count, option-count, deduplication, or option-group rules.
var ErrSetSelection = errors.New("aiken set selection rejected")

const setCatalogPath = "etc/customability/partsetcustominfo.etc"
const setCatalogMaxInt32 = int64(1<<31 - 1)

// MaterialRate is one [material rate by point] row. The rate is kept as
// float32 because the native material multiplication and truncation use
// float32 operands.
type MaterialRate struct {
	ItemID int64
	Rate   float32
}

// MaxPointKey is the proven [max point table] selector. Grade uses the
// client's established mapping D=0, C=1, B=2, A=3; PartCount is the literal
// second selector value.
type MaxPointKey struct {
	Grade     int
	PartCount int
}

// SetPointRule is one [point info] record from partsetcustominfo.etc.
type SetPointRule struct {
	PartSetIndex int
	MinPoint     int64
	MaxPoint     int64
	MinPartCount int
	MaxPartCount int
	MaxPoints    map[MaxPointKey]int64
	Materials    []MaterialRate
	DiscountRate float32
	EnchantMax   int
}

// OptionGroup is one [group] record. Indexes are option IDs, not group keys.
type OptionGroup struct {
	Key                int
	Indexes            []byte
	ConditionPartCount int
}

// SetCatalog is the pure data projection of etc/customability/
// partsetcustominfo.etc. It contains no repository, transaction, or wire
// behavior.
type SetCatalog struct {
	Points     map[int]SetPointRule
	Spend      map[byte]int64
	Groups     []OptionGroup
	SetNames   map[string]string
	GroupNames map[int]string
}

// CostStep records one positive option allocation. Before and After are the
// remaining point values around the subtraction.
type CostStep struct {
	Option byte
	Base   int64
	Before int64
	Cost   int64
	After  int64
}

// CostPlan is the proven point allocation followed by material projection.
// A successful plan has no zero or negative material rows removed: the native
// arithmetic appends every rate row, including rows that truncate to zero.
type CostPlan struct {
	Available int64
	Spent     int64
	Remaining int64
	Steps     []CostStep
	Materials []MaterialCost
}

// ParseSetCatalog parses partsetcustominfo text.
func ParseSetCatalog(text string) (*SetCatalog, error) {
	doc, err := pvf.Parse(setCatalogPath, text)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSetCatalog, err)
	}
	return ParseSetCatalogTokens(doc.Tokens)
}

// ParseSetCatalogTokens parses a PVF token stream. Section tokens define the
// boundaries, so a full pvf.Document token slice and an equivalent standalone
// token slice produce the same catalog.
func ParseSetCatalogTokens(tokens []pvf.Token) (*SetCatalog, error) {
	sections, err := setCatalogSections(tokens)
	if err != nil {
		return nil, err
	}
	cursor := setCatalogCursor{tokens: tokens, sections: sections}
	result := &SetCatalog{
		Points:     make(map[int]SetPointRule),
		Spend:      make(map[byte]int64),
		SetNames:   make(map[string]string),
		GroupNames: make(map[int]string),
	}

	for cursor.nextIs("point info") {
		rule, err := parseSetPointRule(&cursor)
		if err != nil {
			return nil, err
		}
		if _, exists := result.Points[rule.PartSetIndex]; exists {
			return nil, fmt.Errorf("%w: duplicate part set index %d", ErrSetCatalog, rule.PartSetIndex)
		}
		result.Points[rule.PartSetIndex] = rule
	}
	if len(result.Points) == 0 {
		return nil, fmt.Errorf("%w: missing point info", ErrSetCatalog)
	}

	spendTokens, err := cursor.take("spend grade point")
	if err != nil {
		return nil, err
	}
	if err := parseSetSpendPoints(spendTokens, result.Spend); err != nil {
		return nil, err
	}
	if err := cursor.empty("/spend grade point"); err != nil {
		return nil, err
	}
	if len(result.Spend) == 0 {
		return nil, fmt.Errorf("%w: empty spend grade point", ErrSetCatalog)
	}

	seenGroups := make(map[int]bool)
	for cursor.nextIs("group") {
		group, err := parseSetOptionGroup(&cursor)
		if err != nil {
			return nil, err
		}
		if seenGroups[group.Key] {
			return nil, fmt.Errorf("%w: duplicate group key %d", ErrSetCatalog, group.Key)
		}
		seenGroups[group.Key] = true
		result.Groups = append(result.Groups, group)
	}
	if len(result.Groups) == 0 {
		return nil, fmt.Errorf("%w: missing groups", ErrSetCatalog)
	}

	for cursor.nextIs("set item name") {
		values, err := cursor.take("set item name")
		if err != nil {
			return nil, err
		}
		if len(values) != 2 || !setCatalogTextToken(values[0]) || !setCatalogTextToken(values[1]) {
			return nil, fmt.Errorf("%w: malformed set item name", ErrSetCatalog)
		}
		label := strings.TrimSpace(values[0].Value)
		name := strings.TrimSpace(values[1].Value)
		if label == "" || name == "" {
			return nil, fmt.Errorf("%w: malformed set item name", ErrSetCatalog)
		}
		if _, exists := result.SetNames[label]; exists {
			return nil, fmt.Errorf("%w: duplicate set item label %s", ErrSetCatalog, label)
		}
		result.SetNames[label] = name
	}

	for cursor.nextIs("group name") {
		values, err := cursor.take("group name")
		if err != nil {
			return nil, err
		}
		if len(values) != 2 || !setCatalogTextToken(values[0]) || !setCatalogTextToken(values[1]) {
			return nil, fmt.Errorf("%w: malformed group name", ErrSetCatalog)
		}
		key, err := setCatalogDecimal(values[0].Value)
		if err != nil || key <= 0 {
			return nil, fmt.Errorf("%w: malformed group name key", ErrSetCatalog)
		}
		name := strings.TrimSpace(values[1].Value)
		if name == "" {
			return nil, fmt.Errorf("%w: empty group name", ErrSetCatalog)
		}
		if _, exists := result.GroupNames[int(key)]; exists {
			return nil, fmt.Errorf("%w: duplicate group name %d", ErrSetCatalog, key)
		}
		result.GroupNames[int(key)] = name
	}

	if cursor.pos != len(cursor.sections) {
		return nil, fmt.Errorf("%w: unsupported section %s", ErrSetCatalog, cursor.sections[cursor.pos].Name)
	}
	return result, nil
}

// SelectGrade reproduces 0x3B5B5E0: grades outside 0..3 are ignored, the
// remaining byte values are summed, and the unsigned integer quotient is
// returned. An empty valid set returns the native sentinel 4 and count 0.
func SelectGrade(grades []byte) (grade int, valid int) {
	var sum int64
	for _, value := range grades {
		if value >= 4 {
			continue
		}
		sum += int64(value)
		valid++
	}
	if valid == 0 {
		return 4, 0
	}
	return int(sum / int64(valid)), valid
}

// MaxPoint returns the [max point table] value for a part set, grade, and
// part count. The caller supplies the client grade index (D=0..A=3).
func (c *SetCatalog) MaxPoint(partSetIndex, grade, partCount int) (int64, bool) {
	if c == nil {
		return 0, false
	}
	rule, ok := c.Points[partSetIndex]
	if !ok {
		return 0, false
	}
	points, ok := rule.MaxPoints[MaxPointKey{Grade: grade, PartCount: partCount}]
	return points, ok
}

// OptionCost implements the proven 0x3B5CED0 arithmetic:
//
//	floor(base * (remaining + 1000) / (base + 1000))
//
// Non-positive base or remaining returns zero. All inputs here are
// non-negative and the native evidence does not extend the formula to
// overflowing signed 32-bit products.
func OptionCost(base, remaining int64) int64 {
	if base <= 0 || remaining <= 0 ||
		base > setCatalogMaxInt32-1000 || remaining > setCatalogMaxInt32-1000 {
		return 0
	}
	product := base * (remaining + 1000)
	if product < 0 || product > setCatalogMaxInt32 {
		return 0
	}
	return product / (base + 1000)
}

// MaterialCounts applies one material row per rate using the native float32
// multiply and truncation: floor(float32(points) * float32(rate)). Positive
// results above the native signed int32 range saturate at MaxInt32 so the
// public helper cannot wrap; PlanCost rejects such a catalog before use.
func MaterialCounts(points int64, rates []MaterialRate) []MaterialCost {
	out := make([]MaterialCost, 0, len(rates))
	if points < 0 {
		points = 0
	}
	for _, rate := range rates {
		count := setCatalogMaterialCount(points, rate.Rate)
		out = append(out, MaterialCost{ItemID: rate.ItemID, Count: count})
	}
	return out
}

// PlanCost applies the proven sequence: look up the max point value, allocate
// each positive option in wire order, accumulate only non-negative remaining
// states, then project every material row from the total spent points.
func (c *SetCatalog) PlanCost(partSetIndex, grade, partCount int, options []byte) (CostPlan, error) {
	var plan CostPlan
	if c == nil {
		return plan, fmt.Errorf("%w: nil catalog", ErrSetCatalog)
	}
	rule, ok := c.Points[partSetIndex]
	if !ok {
		return plan, fmt.Errorf("%w: unknown part set %d", ErrSetCatalog, partSetIndex)
	}
	if grade < 0 || grade > 3 || rule.MinPartCount < 1 ||
		partCount < rule.MinPartCount || partCount > rule.MaxPartCount {
		return plan, fmt.Errorf("%w: grade or part count outside part set bounds", ErrSetCatalog)
	}
	available, ok := rule.MaxPoints[MaxPointKey{Grade: grade, PartCount: partCount}]
	if !ok {
		return plan, fmt.Errorf("%w: missing max point row (%d,%d,%d)", ErrSetCatalog, partSetIndex, grade, partCount)
	}
	if available < 0 || available > setCatalogMaxInt32 {
		return plan, fmt.Errorf("%w: max point %d is outside int32", ErrSetCatalog, available)
	}
	if len(rule.Materials) == 0 {
		return plan, fmt.Errorf("%w: part set %d has no material rates", ErrSetCatalog, partSetIndex)
	}
	for _, material := range rule.Materials {
		if material.ItemID <= 0 || material.Rate < 0 ||
			math.IsNaN(float64(material.Rate)) || math.IsInf(float64(material.Rate), 0) {
			return plan, fmt.Errorf("%w: invalid material rate row", ErrSetCatalog)
		}
		if setCatalogMaterialOverflow(available, material.Rate) {
			return plan, fmt.Errorf("%w: material count exceeds int32", ErrSetCatalog)
		}
	}
	plan.Available = available
	remaining := available
	for _, option := range options {
		if option == 0 {
			return plan, fmt.Errorf("%w: zero option id", ErrSetCatalog)
		}
		base, ok := c.Spend[option]
		if !ok {
			return plan, fmt.Errorf("%w: missing spend grade point %d", ErrSetCatalog, option)
		}
		if base <= 0 || base > setCatalogMaxInt32-1000 {
			return plan, fmt.Errorf("%w: spend grade point %d is outside int32", ErrSetCatalog, option)
		}
		if remaining <= 0 {
			return plan, ErrInsufficientSetPoints
		}
		if remaining > setCatalogMaxInt32-1000 {
			return plan, fmt.Errorf("%w: remaining points %d are outside int32", ErrSetCatalog, remaining)
		}
		product := base * (remaining + 1000)
		if product < 0 || product > setCatalogMaxInt32 {
			return plan, fmt.Errorf("%w: option %d product exceeds int32", ErrSetCatalog, option)
		}
		cost := OptionCost(base, remaining)
		if cost <= 0 {
			return plan, fmt.Errorf("%w: option %d has non-positive cost", ErrSetCatalog, option)
		}
		before := remaining
		remaining -= cost
		if remaining < 0 {
			plan.Remaining = remaining
			return plan, ErrInsufficientSetPoints
		}
		plan.Spent += cost
		plan.Steps = append(plan.Steps, CostStep{
			Option: option,
			Base:   base,
			Before: before,
			Cost:   cost,
			After:  remaining,
		})
	}
	plan.Remaining = remaining
	plan.Materials = MaterialCounts(plan.Spent, rule.Materials)
	return plan, nil
}

// PlanSelection validates the player-visible selection before calculating its
// cost. Every supplied grade must be 0..3. The number of grades must be within
// the part-set bounds, and the number of options must be 1..EnchantMax.
// Options must be non-zero, unique, and each belong to a distinct option group. A
// group's ConditionPartCount is treated as a minimum part count.
func (c *SetCatalog) PlanSelection(partSetIndex int, grades []byte, options []byte) (int, CostPlan, error) {
	var plan CostPlan
	if c == nil {
		return 0, plan, fmt.Errorf("%w: nil catalog", ErrSetSelection)
	}
	rule, ok := c.Points[partSetIndex]
	if !ok {
		return 0, plan, fmt.Errorf("%w: unknown part set %d", ErrSetSelection, partSetIndex)
	}
	if rule.MinPartCount < 1 || rule.MaxPartCount < rule.MinPartCount ||
		rule.EnchantMax < 1 || rule.EnchantMax > 255 {
		return 0, plan, fmt.Errorf("%w: invalid part set bounds", ErrSetSelection)
	}
	count := len(grades)
	if count < rule.MinPartCount || count > rule.MaxPartCount {
		return 0, plan, fmt.Errorf("%w: part count %d outside %d..%d", ErrSetSelection, count, rule.MinPartCount, rule.MaxPartCount)
	}
	for _, grade := range grades {
		if grade >= 4 {
			return 0, plan, fmt.Errorf("%w: grade %d outside 0..3", ErrSetSelection, grade)
		}
	}
	if len(options) < 1 || len(options) > rule.EnchantMax {
		return 0, plan, fmt.Errorf("%w: option count %d outside 1..%d", ErrSetSelection, len(options), rule.EnchantMax)
	}
	selected := make(map[byte]bool, len(options))
	for _, option := range options {
		if option == 0 || selected[option] {
			return 0, plan, fmt.Errorf("%w: zero or duplicate option %d", ErrSetSelection, option)
		}
		selected[option] = true
	}

	groupsByOption := make(map[byte]OptionGroup)
	for _, group := range c.Groups {
		if group.Key <= 0 || len(group.Indexes) == 0 || group.ConditionPartCount < 0 {
			return 0, plan, fmt.Errorf("%w: malformed option group", ErrSetSelection)
		}
		seen := make(map[byte]bool, len(group.Indexes))
		for _, option := range group.Indexes {
			if option == 0 || seen[option] {
				return 0, plan, fmt.Errorf("%w: malformed option group %d", ErrSetSelection, group.Key)
			}
			seen[option] = true
			if _, exists := groupsByOption[option]; exists {
				return 0, plan, fmt.Errorf("%w: option %d belongs to multiple groups", ErrSetSelection, option)
			}
			groupsByOption[option] = group
		}
	}

	chosen := map[int]bool{}
	for _, option := range options {
		group, ok := groupsByOption[option]
		if !ok {
			return 0, plan, fmt.Errorf("%w: option %d has no group", ErrSetSelection, option)
		}
		if chosen[group.Key] {
			return 0, plan, fmt.Errorf("%w: multiple options from group %d", ErrSetSelection, group.Key)
		}
		chosen[group.Key] = true
		if group.ConditionPartCount > 0 && count < group.ConditionPartCount {
			return 0, plan, fmt.Errorf("%w: group %d requires at least %d parts", ErrSetSelection, group.Key, group.ConditionPartCount)
		}
	}
	grade, valid := SelectGrade(grades)
	if valid != count {
		return 0, plan, fmt.Errorf("%w: invalid grade count", ErrSetSelection)
	}
	plan, err := c.PlanCost(partSetIndex, grade, count, options)
	if err != nil {
		return grade, plan, err
	}
	return grade, plan, nil
}

// ApplyDiscountRate implements the proven E73110 material multiplication. The
// rate is clamped to 0..1 and each material row is truncated independently.
// The evidence does not establish which recipes enter this path, so PlanCost
// never applies it implicitly.
func ApplyDiscountRate(rows []MaterialCost, rate float32) []MaterialCost {
	out := make([]MaterialCost, 0, len(rows))
	if math.IsNaN(float64(rate)) || rate <= 0 {
		for _, row := range rows {
			out = append(out, MaterialCost{ItemID: row.ItemID})
		}
		return out
	}
	if rate > 1 {
		rate = 1
	}
	for _, row := range rows {
		count := setCatalogMaterialCount(row.Count, rate)
		out = append(out, MaterialCost{ItemID: row.ItemID, Count: count})
	}
	return out
}

func setCatalogMaterialCount(points int64, rate float32) int64 {
	if points <= 0 || rate <= 0 || math.IsNaN(float64(rate)) || math.IsInf(float64(rate), 0) {
		return 0
	}
	product := float32(points) * rate
	if math.IsNaN(float64(product)) || math.IsInf(float64(product), 0) ||
		float64(product) >= float64(setCatalogMaxInt32) {
		return setCatalogMaxInt32
	}
	count := int64(product)
	if count < 0 {
		return 0
	}
	return count
}

func setCatalogMaterialOverflow(points int64, rate float32) bool {
	if points <= 0 || rate <= 0 {
		return false
	}
	product := float32(points) * rate
	return math.IsNaN(float64(product)) || math.IsInf(float64(product), 0) ||
		float64(product) >= float64(setCatalogMaxInt32)
}

// NetMaterials implements the proven list merge at 0x10D1CA0: each base row
// is reduced by the first deduction row with the same item ID, clamped at
// zero. Base order and duplicate rows are preserved. Deduction rows that have
// no matching base row are ignored.
func NetMaterials(base, deduction []MaterialCost) []MaterialCost {
	first := make(map[int64]int, len(deduction))
	for i, row := range deduction {
		if _, exists := first[row.ItemID]; !exists {
			first[row.ItemID] = i
		}
	}
	out := make([]MaterialCost, 0, len(base))
	for _, row := range base {
		count := row.Count
		if index, ok := first[row.ItemID]; ok {
			count -= deduction[index].Count
			if count < 0 {
				count = 0
			}
		}
		out = append(out, MaterialCost{ItemID: row.ItemID, Count: count})
	}
	return out
}

func parseSetPointRule(cursor *setCatalogCursor) (SetPointRule, error) {
	var rule SetPointRule
	if err := cursor.empty("point info"); err != nil {
		return rule, err
	}
	index, err := cursor.oneInt("part set index")
	if err != nil || index <= 0 || index > setCatalogMaxInt32 {
		return rule, fmt.Errorf("%w: invalid part set index", ErrSetCatalog)
	}
	rule.PartSetIndex = int(index)
	if rule.MinPoint, err = cursor.oneInt("min point"); err != nil {
		return rule, err
	}
	if rule.MaxPoint, err = cursor.oneInt("max point"); err != nil {
		return rule, err
	}
	if rule.MinPoint < 0 || rule.MaxPoint < rule.MinPoint || rule.MaxPoint > setCatalogMaxInt32 {
		return rule, fmt.Errorf("%w: invalid point bounds", ErrSetCatalog)
	}
	minParts, err := cursor.oneInt("min part count")
	if err != nil {
		return rule, err
	}
	maxParts, err := cursor.oneInt("max part count")
	if err != nil {
		return rule, err
	}
	if minParts < 1 || maxParts < minParts || maxParts > setCatalogMaxInt32 {
		return rule, fmt.Errorf("%w: invalid part count bounds", ErrSetCatalog)
	}
	rule.MinPartCount, rule.MaxPartCount = int(minParts), int(maxParts)

	tableTokens, err := cursor.take("max point table")
	if err != nil {
		return rule, err
	}
	if rule.MaxPoints, err = parseSetMaxPoints(tableTokens); err != nil {
		return rule, err
	}
	if err := cursor.empty("/max point table"); err != nil {
		return rule, err
	}

	materialTokens, err := cursor.take("material rate by point")
	if err != nil {
		return rule, err
	}
	if rule.Materials, err = parseSetMaterialRates(materialTokens); err != nil {
		return rule, err
	}
	if err := cursor.empty("/material rate by point"); err != nil {
		return rule, err
	}

	discount, err := cursor.take("discount enchant rate")
	if err != nil {
		return rule, err
	}
	if len(discount) != 1 || discount[0].Kind != pvf.TokenFloat {
		return rule, fmt.Errorf("%w: malformed discount enchant rate", ErrSetCatalog)
	}
	if discount[0].Float < 0 || math.IsNaN(discount[0].Float) || math.IsInf(discount[0].Float, 0) {
		return rule, fmt.Errorf("%w: invalid discount enchant rate", ErrSetCatalog)
	}
	discountRate := float32(discount[0].Float)
	if math.IsInf(float64(discountRate), 0) {
		return rule, fmt.Errorf("%w: discount enchant rate overflows float32", ErrSetCatalog)
	}
	rule.DiscountRate = discountRate

	enchantMax, err := cursor.oneInt("enchant max count")
	if err != nil || enchantMax < 1 || enchantMax > setCatalogMaxInt32 {
		return rule, fmt.Errorf("%w: invalid enchant max count", ErrSetCatalog)
	}
	rule.EnchantMax = int(enchantMax)
	if err := cursor.empty("/point info"); err != nil {
		return rule, err
	}
	return rule, nil
}

func parseSetMaxPoints(tokens []pvf.Token) (map[MaxPointKey]int64, error) {
	if len(tokens) == 0 || len(tokens)%3 != 0 {
		return nil, fmt.Errorf("%w: malformed max point table", ErrSetCatalog)
	}
	table := make(map[MaxPointKey]int64, len(tokens)/3)
	for pos := 0; pos < len(tokens); pos += 3 {
		gradeToken, partsToken, pointsToken := tokens[pos], tokens[pos+1], tokens[pos+2]
		if !setCatalogTextToken(gradeToken) || partsToken.Kind != pvf.TokenInt || pointsToken.Kind != pvf.TokenInt {
			return nil, fmt.Errorf("%w: malformed max point row", ErrSetCatalog)
		}
		grade, ok := setCatalogGrade(strings.ToUpper(strings.TrimSpace(gradeToken.Value)))
		if !ok || partsToken.Int < 1 || partsToken.Int > setCatalogMaxInt32 ||
			pointsToken.Int <= 0 || pointsToken.Int > setCatalogMaxInt32 {
			return nil, fmt.Errorf("%w: malformed max point row", ErrSetCatalog)
		}
		key := MaxPointKey{Grade: grade, PartCount: int(partsToken.Int)}
		if _, exists := table[key]; exists {
			return nil, fmt.Errorf("%w: duplicate max point row", ErrSetCatalog)
		}
		table[key] = pointsToken.Int
	}
	return table, nil
}

func parseSetMaterialRates(tokens []pvf.Token) ([]MaterialRate, error) {
	if len(tokens) == 0 || len(tokens)%2 != 0 {
		return nil, fmt.Errorf("%w: malformed material rate by point", ErrSetCatalog)
	}
	out := make([]MaterialRate, 0, len(tokens)/2)
	for pos := 0; pos < len(tokens); pos += 2 {
		item, rate := tokens[pos], tokens[pos+1]
		if item.Kind != pvf.TokenInt || item.Int <= 0 || rate.Kind != pvf.TokenFloat ||
			rate.Float < 0 || math.IsNaN(rate.Float) || math.IsInf(rate.Float, 0) {
			return nil, fmt.Errorf("%w: malformed material rate row", ErrSetCatalog)
		}
		rate32 := float32(rate.Float)
		if math.IsInf(float64(rate32), 0) {
			return nil, fmt.Errorf("%w: material rate overflows float32", ErrSetCatalog)
		}
		out = append(out, MaterialRate{ItemID: item.Int, Rate: rate32})
	}
	return out, nil
}

func parseSetSpendPoints(tokens []pvf.Token, out map[byte]int64) error {
	if len(tokens) == 0 || len(tokens)%2 != 0 {
		return fmt.Errorf("%w: malformed spend grade point", ErrSetCatalog)
	}
	for pos := 0; pos < len(tokens); pos += 2 {
		option, points := tokens[pos], tokens[pos+1]
		if option.Kind != pvf.TokenInt || points.Kind != pvf.TokenInt ||
			option.Int < 1 || option.Int > 255 || points.Int <= 0 || points.Int > setCatalogMaxInt32 {
			return fmt.Errorf("%w: malformed spend row", ErrSetCatalog)
		}
		key := byte(option.Int)
		if _, exists := out[key]; exists {
			return fmt.Errorf("%w: duplicate spend option %d", ErrSetCatalog, key)
		}
		out[key] = points.Int
	}
	return nil
}

func parseSetOptionGroup(cursor *setCatalogCursor) (OptionGroup, error) {
	var group OptionGroup
	if err := cursor.empty("group"); err != nil {
		return group, err
	}
	keyTokens, err := cursor.take("key")
	if err != nil {
		return group, err
	}
	if len(keyTokens) != 1 || !setCatalogTextToken(keyTokens[0]) {
		return group, fmt.Errorf("%w: malformed group key", ErrSetCatalog)
	}
	key, err := setCatalogDecimal(keyTokens[0].Value)
	if err != nil || key <= 0 || key > setCatalogMaxInt32 {
		return group, fmt.Errorf("%w: malformed group key", ErrSetCatalog)
	}
	group.Key = int(key)

	indexTokens, err := cursor.take("indexes")
	if err != nil {
		return group, err
	}
	if len(indexTokens) == 0 {
		return group, fmt.Errorf("%w: empty group indexes", ErrSetCatalog)
	}
	seen := make(map[byte]bool, len(indexTokens))
	for _, token := range indexTokens {
		if token.Kind != pvf.TokenInt || token.Int < 1 || token.Int > 255 {
			return group, fmt.Errorf("%w: malformed group index", ErrSetCatalog)
		}
		option := byte(token.Int)
		if seen[option] {
			return group, fmt.Errorf("%w: duplicate group index %d", ErrSetCatalog, option)
		}
		seen[option] = true
		group.Indexes = append(group.Indexes, option)
	}
	if err := cursor.empty("/indexes"); err != nil {
		return group, err
	}

	if cursor.nextIs("condition") {
		condition, err := cursor.take("condition")
		if err != nil {
			return group, err
		}
		if len(condition) != 2 || !setCatalogTextToken(condition[0]) ||
			!strings.EqualFold(strings.TrimSpace(condition[0].Value), "part count") ||
			condition[1].Kind != pvf.TokenInt || condition[1].Int < 1 || condition[1].Int > setCatalogMaxInt32 {
			return group, fmt.Errorf("%w: malformed group condition", ErrSetCatalog)
		}
		group.ConditionPartCount = int(condition[1].Int)
		if err := cursor.empty("/condition"); err != nil {
			return group, err
		}
	}
	if err := cursor.empty("/group"); err != nil {
		return group, err
	}
	return group, nil
}

func setCatalogGrade(value string) (int, bool) {
	switch value {
	case "D":
		return 0, true
	case "C":
		return 1, true
	case "B":
		return 2, true
	case "A":
		return 3, true
	default:
		return 0, false
	}
}

func setCatalogTextToken(token pvf.Token) bool {
	return token.Kind == pvf.TokenString || token.Kind == pvf.TokenIdent
}

func setCatalogDecimal(value string) (int64, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return 0, fmt.Errorf("%w: malformed decimal", ErrSetCatalog)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: malformed decimal", ErrSetCatalog)
	}
	return parsed, nil
}

func setCatalogSections(tokens []pvf.Token) ([]pvf.Section, error) {
	var sections []pvf.Section
	for i, token := range tokens {
		if token.Kind != pvf.TokenSection {
			continue
		}
		if len(sections) > 0 {
			sections[len(sections)-1].End = i
		}
		sections = append(sections, pvf.Section{Name: token.Value, Start: i + 1})
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("%w: no PVF sections", ErrSetCatalog)
	}
	sections[len(sections)-1].End = len(tokens)
	for _, section := range sections {
		if section.Start < 0 || section.End < section.Start || section.End > len(tokens) {
			return nil, fmt.Errorf("%w: invalid section range", ErrSetCatalog)
		}
	}
	return sections, nil
}

type setCatalogCursor struct {
	tokens   []pvf.Token
	sections []pvf.Section
	pos      int
}

func (c *setCatalogCursor) take(name string) ([]pvf.Token, error) {
	if c == nil || c.pos < 0 || c.pos >= len(c.sections) {
		return nil, fmt.Errorf("%w: missing %s section", ErrSetCatalog, name)
	}
	section := c.sections[c.pos]
	if !strings.EqualFold(strings.TrimSpace(section.Name), name) {
		return nil, fmt.Errorf("%w: want %s, got %s", ErrSetCatalog, name, section.Name)
	}
	if section.Start < 0 || section.End < section.Start || section.End > len(c.tokens) {
		return nil, fmt.Errorf("%w: invalid %s token range", ErrSetCatalog, name)
	}
	c.pos++
	return c.tokens[section.Start:section.End], nil
}

func (c *setCatalogCursor) empty(name string) error {
	tokens, err := c.take(name)
	if err != nil {
		return err
	}
	if len(tokens) != 0 {
		return fmt.Errorf("%w: %s must be empty", ErrSetCatalog, name)
	}
	return nil
}

func (c *setCatalogCursor) oneInt(name string) (int64, error) {
	tokens, err := c.take(name)
	if err != nil {
		return 0, err
	}
	if len(tokens) != 1 || tokens[0].Kind != pvf.TokenInt {
		return 0, fmt.Errorf("%w: %s must contain one integer", ErrSetCatalog, name)
	}
	return tokens[0].Int, nil
}

func (c *setCatalogCursor) nextIs(name string) bool {
	return c != nil && c.pos < len(c.sections) &&
		strings.EqualFold(strings.TrimSpace(c.sections[c.pos].Name), name)
}
