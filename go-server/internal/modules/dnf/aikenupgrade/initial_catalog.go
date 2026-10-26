package aikenupgrade

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const (
	initialCatalogPath      = "etc/customability/individualcustominfo.etc"
	initialCatalogListPath  = "etc/customability/individualcustomability.lst"
	initialGradeWeightTotal = uint32(10000)
)

// ErrInitialCatalog rejects a PVF projection that cannot produce an
// unambiguous initial Aiken option roll.
var ErrInitialCatalog = errors.New("aiken initial catalog rejected")

// InitialCatalog is the immutable data projection needed by new dungeon
// drops. The client stores two independent option IDs, an unused zero pair,
// and a grade in D, C, B, A order (0..3).
type InitialCatalog struct {
	options          []byte
	validOptions     map[byte]bool
	optionGroups     map[byte]int
	gradeWeights     [4]uint32
	gradeWeightTotal uint32
}

type initialListEntry struct {
	id   byte
	path string
}

// NewInitialCatalog reads individualcustominfo.etc and its option list. It
// validates the group/index sets, grade weights, and list references without
// reading the referenced option documents.
func NewInitialCatalog(source pvf.Source) (*InitialCatalog, error) {
	if source == nil {
		return nil, ErrInitialCatalog
	}
	info, err := readInitialDocument(source, initialCatalogPath)
	if err != nil {
		return nil, err
	}
	options, optionGroups, err := parseInitialGroups(info)
	if err != nil {
		return nil, err
	}
	gradeWeights, gradeWeightTotal, err := parseInitialGradeWeights(info)
	if err != nil {
		return nil, err
	}
	list, err := readInitialDocument(source, initialCatalogListPath)
	if err != nil {
		return nil, err
	}
	entries, err := parseInitialList(list)
	if err != nil {
		return nil, err
	}
	if err := validateInitialList(entries, optionGroups); err != nil {
		return nil, err
	}

	validOptions := make(map[byte]bool, len(options))
	for _, option := range options {
		validOptions[option] = true
	}
	return &InitialCatalog{
		options:          options,
		validOptions:     validOptions,
		optionGroups:     optionGroups,
		gradeWeights:     gradeWeights,
		gradeWeightTotal: gradeWeightTotal,
	}, nil
}

// Roll independently selects both option slots from the complete validated
// option set, then selects a grade from the PVF cumulative weights. The draw
// order is first option, second option, grade. Equal options are permitted.
func (c *InitialCatalog) Roll(roll func(uint32) (uint32, error)) ([5]byte, error) {
	var state [5]byte
	if c == nil || roll == nil || len(c.options) == 0 ||
		len(c.validOptions) != len(c.options) ||
		c.gradeWeightTotal != initialGradeWeightTotal {
		return state, fmt.Errorf("%w: uninitialized catalog or draw source", ErrInitialCatalog)
	}

	for slot := 0; slot < 2; slot++ {
		draw, err := drawInitial(roll, uint32(len(c.options)), fmt.Sprintf("option slot %d", slot))
		if err != nil {
			return [5]byte{}, err
		}
		state[slot] = c.options[draw]
	}
	draw, err := drawInitial(roll, c.gradeWeightTotal, "grade")
	if err != nil {
		return [5]byte{}, err
	}
	grade, ok := c.gradeForWeight(draw)
	if !ok {
		return [5]byte{}, fmt.Errorf("%w: grade draw %d is outside cumulative weights", ErrInitialCatalog, draw)
	}
	state[4] = grade
	return state, nil
}

// Valid reports whether state is a fresh two-option drop state. Both option
// IDs must exist and may be equal. The third option and set key remain zero;
// the grade must be one of 0..3.
func (c *InitialCatalog) Valid(state [5]byte) bool {
	if c == nil {
		return false
	}
	return state[0] != 0 && state[1] != 0 &&
		c.validOptions[state[0]] && c.validOptions[state[1]] &&
		state[2] == 0 && state[3] == 0 && state[4] <= 3
}

// OptionGroup returns the PVF group key for an option ID. This is useful when
// validating selections against the client's native dropdown groups.
func (c *InitialCatalog) OptionGroup(option byte) (int, bool) {
	if c == nil || option == 0 {
		return 0, false
	}
	group, ok := c.optionGroups[option]
	return group, ok
}

func readInitialDocument(source pvf.Source, docPath string) (*pvf.Document, error) {
	text, err := source.ReadText(docPath)
	if err != nil {
		return nil, errors.Join(ErrInitialCatalog, fmt.Errorf("read %s: %w", docPath, err))
	}
	document, err := pvf.Parse(docPath, text)
	if err != nil {
		return nil, errors.Join(ErrInitialCatalog, fmt.Errorf("parse %s: %w", docPath, err))
	}
	return document, nil
}

func parseInitialGroups(document *pvf.Document) ([]byte, map[byte]int, error) {
	if document == nil {
		return nil, nil, fmt.Errorf("%w: missing individual custom info document", ErrInitialCatalog)
	}

	groups := make(map[byte]int)
	seenKeys := make(map[int]bool)
	options := make([]byte, 0)
	inGroup := false
	var currentKey int
	var keyFound bool
	var indexesFound bool
	var indexes []byte

	commitGroup := func() error {
		if !keyFound {
			return fmt.Errorf("%w: group is missing key", ErrInitialCatalog)
		}
		if !indexesFound || len(indexes) == 0 {
			return fmt.Errorf("%w: group %d is missing indexes", ErrInitialCatalog, currentKey)
		}
		if seenKeys[currentKey] {
			return fmt.Errorf("%w: duplicate group key %d", ErrInitialCatalog, currentKey)
		}
		seenKeys[currentKey] = true
		for _, option := range indexes {
			if _, exists := groups[option]; exists {
				return fmt.Errorf("%w: option %d belongs to multiple groups", ErrInitialCatalog, option)
			}
			groups[option] = currentKey
			options = append(options, option)
		}
		return nil
	}

	for sectionIndex, section := range document.Sections {
		if section.Start < 0 || section.End < section.Start || section.End > len(document.Tokens) {
			return nil, nil, fmt.Errorf("%w: invalid section range", ErrInitialCatalog)
		}
		name := initialSectionName(section.Name)
		tokens := document.Tokens[section.Start:section.End]
		switch name {
		case "group":
			if inGroup {
				return nil, nil, fmt.Errorf("%w: nested group or missing /group", ErrInitialCatalog)
			}
			if len(tokens) != 0 {
				return nil, nil, fmt.Errorf("%w: group must be empty", ErrInitialCatalog)
			}
			inGroup = true
			currentKey = 0
			keyFound = false
			indexesFound = false
			indexes = nil
		case "/group":
			if !inGroup {
				return nil, nil, fmt.Errorf("%w: /group without group", ErrInitialCatalog)
			}
			if len(tokens) != 0 {
				return nil, nil, fmt.Errorf("%w: /group must be empty", ErrInitialCatalog)
			}
			if err := commitGroup(); err != nil {
				return nil, nil, err
			}
			inGroup = false
		case "key":
			if !inGroup {
				continue
			}
			if keyFound {
				return nil, nil, fmt.Errorf("%w: duplicate key in group", ErrInitialCatalog)
			}
			if len(tokens) != 1 {
				return nil, nil, fmt.Errorf("%w: group key must contain one value", ErrInitialCatalog)
			}
			key, ok := initialByteValue(tokens[0])
			if !ok {
				return nil, nil, fmt.Errorf("%w: group key is not a nonzero byte", ErrInitialCatalog)
			}
			currentKey = int(key)
			keyFound = true
		case "indexes":
			if !inGroup {
				continue
			}
			if indexesFound {
				return nil, nil, fmt.Errorf("%w: duplicate indexes in group", ErrInitialCatalog)
			}
			if len(tokens) == 0 {
				return nil, nil, fmt.Errorf("%w: empty group indexes", ErrInitialCatalog)
			}
			local := make(map[byte]bool, len(tokens))
			indexes = make([]byte, 0, len(tokens))
			for _, token := range tokens {
				if token.Kind != pvf.TokenInt || token.Int < 1 || token.Int > 255 {
					return nil, nil, fmt.Errorf("%w: option index is not a nonzero byte", ErrInitialCatalog)
				}
				option := byte(token.Int)
				if local[option] {
					return nil, nil, fmt.Errorf("%w: duplicate option %d in group", ErrInitialCatalog, option)
				}
				local[option] = true
				indexes = append(indexes, option)
			}
			if sectionIndex+1 >= len(document.Sections) ||
				initialSectionName(document.Sections[sectionIndex+1].Name) != "/indexes" ||
				document.Sections[sectionIndex+1].Start != document.Sections[sectionIndex+1].End {
				return nil, nil, fmt.Errorf("%w: indexes are not closed", ErrInitialCatalog)
			}
			indexesFound = true
		}
	}
	if inGroup {
		return nil, nil, fmt.Errorf("%w: missing /group", ErrInitialCatalog)
	}
	if len(seenKeys) == 0 {
		return nil, nil, fmt.Errorf("%w: missing groups", ErrInitialCatalog)
	}
	sort.Slice(options, func(i, j int) bool { return options[i] < options[j] })
	return options, groups, nil
}

func parseInitialGradeWeights(document *pvf.Document) ([4]uint32, uint32, error) {
	var weights [4]uint32
	if document == nil {
		return weights, 0, fmt.Errorf("%w: missing individual custom info document", ErrInitialCatalog)
	}
	sectionIndex := -1
	for index, section := range document.Sections {
		if initialSectionName(section.Name) != "grade probability" {
			continue
		}
		if sectionIndex >= 0 {
			return weights, 0, fmt.Errorf("%w: duplicate grade probability", ErrInitialCatalog)
		}
		sectionIndex = index
	}
	if sectionIndex < 0 {
		return weights, 0, fmt.Errorf("%w: missing grade probability", ErrInitialCatalog)
	}
	if sectionIndex+1 >= len(document.Sections) ||
		initialSectionName(document.Sections[sectionIndex+1].Name) != "/grade probability" {
		return weights, 0, fmt.Errorf("%w: grade probability is not closed", ErrInitialCatalog)
	}
	opening := document.Sections[sectionIndex]
	closing := document.Sections[sectionIndex+1]
	if opening.Start < 0 || opening.End < opening.Start || opening.End > len(document.Tokens) ||
		closing.Start != closing.End {
		return weights, 0, fmt.Errorf("%w: malformed grade probability section", ErrInitialCatalog)
	}
	tokens := document.Tokens[opening.Start:opening.End]
	if len(tokens) != 8 {
		return weights, 0, fmt.Errorf("%w: grade probability must contain four pairs", ErrInitialCatalog)
	}

	seen := make(map[byte]bool, 4)
	var total int64
	for position := 0; position < len(tokens); position += 2 {
		gradeToken := tokens[position]
		weightToken := tokens[position+1]
		if !initialTextToken(gradeToken) {
			return weights, 0, fmt.Errorf("%w: malformed grade label", ErrInitialCatalog)
		}
		grade, ok := initialGrade(gradeToken.Value)
		if !ok || seen[grade] {
			return weights, 0, fmt.Errorf("%w: missing or duplicate grade %q", ErrInitialCatalog, gradeToken.Value)
		}
		if weightToken.Kind != pvf.TokenInt ||
			weightToken.Int < 0 || weightToken.Int > int64(initialGradeWeightTotal) {
			return weights, 0, fmt.Errorf("%w: invalid weight for grade %q", ErrInitialCatalog, gradeToken.Value)
		}
		seen[grade] = true
		weights[grade] = uint32(weightToken.Int)
		total += weightToken.Int
	}
	if len(seen) != 4 || total != int64(initialGradeWeightTotal) {
		return weights, 0, fmt.Errorf("%w: grade weights total %d, want %d", ErrInitialCatalog, total, initialGradeWeightTotal)
	}
	return weights, uint32(total), nil
}

func parseInitialList(document *pvf.Document) ([]initialListEntry, error) {
	if document == nil || len(document.Tokens) == 0 {
		return nil, fmt.Errorf("%w: missing option list", ErrInitialCatalog)
	}
	if len(document.Sections) != 0 || len(document.Tokens)%2 != 0 {
		return nil, fmt.Errorf("%w: malformed option list", ErrInitialCatalog)
	}
	seenIDs := make(map[byte]bool)
	seenPaths := make(map[string]bool)
	entries := make([]initialListEntry, 0, len(document.Tokens)/2)
	for position := 0; position < len(document.Tokens); position += 2 {
		idToken := document.Tokens[position]
		pathToken := document.Tokens[position+1]
		if idToken.Kind != pvf.TokenInt || idToken.Int < 1 || idToken.Int > 255 {
			return nil, fmt.Errorf("%w: option list ID is not a nonzero byte", ErrInitialCatalog)
		}
		id := byte(idToken.Int)
		if seenIDs[id] {
			return nil, fmt.Errorf("%w: duplicate option list ID %d", ErrInitialCatalog, id)
		}
		if !initialTextToken(pathToken) {
			return nil, fmt.Errorf("%w: option list ID %d has no path", ErrInitialCatalog, id)
		}
		entryPath, err := initialSafeListPath(pathToken.Value)
		if err != nil {
			return nil, err
		}
		pathKey := strings.ToLower(entryPath)
		if seenPaths[pathKey] {
			return nil, fmt.Errorf("%w: duplicate option list path %q", ErrInitialCatalog, entryPath)
		}
		seenIDs[id] = true
		seenPaths[pathKey] = true
		entries = append(entries, initialListEntry{id: id, path: entryPath})
	}
	return entries, nil
}

func initialSafeListPath(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.IndexByte(value, 0) >= 0 ||
		strings.ContainsRune(value, rune(92)) || strings.ContainsRune(value, ':') ||
		path.IsAbs(value) {
		return "", fmt.Errorf("%w: illegal option list path %q", ErrInitialCatalog, value)
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned != value {
		return "", fmt.Errorf("%w: illegal option list path %q", ErrInitialCatalog, value)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: illegal option list path %q", ErrInitialCatalog, value)
		}
	}
	return value, nil
}

func validateInitialList(entries []initialListEntry, groups map[byte]int) error {
	if len(entries) != len(groups) {
		return fmt.Errorf("%w: option list has %d IDs, groups have %d", ErrInitialCatalog, len(entries), len(groups))
	}
	listed := make(map[byte]bool, len(entries))
	for _, entry := range entries {
		if _, ok := groups[entry.id]; !ok {
			return fmt.Errorf("%w: option list ID %d is absent from groups", ErrInitialCatalog, entry.id)
		}
		listed[entry.id] = true
	}
	for option := range groups {
		if !listed[option] {
			return fmt.Errorf("%w: group option %d is absent from option list", ErrInitialCatalog, option)
		}
	}
	return nil
}

func drawInitial(roll func(uint32) (uint32, error), bound uint32, what string) (uint32, error) {
	if roll == nil || bound == 0 {
		return 0, fmt.Errorf("%w: invalid %s draw", ErrInitialCatalog, what)
	}
	draw, err := roll(bound)
	if err != nil {
		return 0, errors.Join(ErrInitialCatalog, fmt.Errorf("%s draw: %w", what, err))
	}
	if draw >= bound {
		return 0, fmt.Errorf("%w: %s draw %d is outside bound %d", ErrInitialCatalog, what, draw, bound)
	}
	return draw, nil
}

func (c *InitialCatalog) gradeForWeight(value uint32) (byte, bool) {
	if c == nil || value >= c.gradeWeightTotal {
		return 0, false
	}
	var cumulative uint32
	for grade, weight := range c.gradeWeights {
		if weight == 0 {
			continue
		}
		next := cumulative + weight
		if next < cumulative {
			return 0, false
		}
		if value < next {
			return byte(grade), true
		}
		cumulative = next
	}
	return 0, false
}

func initialSectionName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func initialTextToken(token pvf.Token) bool {
	return token.Kind == pvf.TokenString || token.Kind == pvf.TokenIdent
}

func initialByteValue(token pvf.Token) (byte, bool) {
	if token.Kind == pvf.TokenInt {
		if token.Int < 1 || token.Int > 255 {
			return 0, false
		}
		return byte(token.Int), true
	}
	if !initialTextToken(token) {
		return 0, false
	}
	value := strings.TrimSpace(token.Value)
	if value == "" || value != token.Value {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 || parsed > 255 {
		return 0, false
	}
	return byte(parsed), true
}

func initialGrade(value string) (byte, bool) {
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
