// Package secretshop parses the data tables used by the private black-diamond
// secret shop. It deliberately stops at catalog loading: selection rolls,
// inventory transactions, persistence, and wire responses belong to callers.
package secretshop

import (
	"errors"
	"fmt"
	"math"
	"sort"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const (
	// PVFPath is the archive path parsed by Load.
	PVFPath = "etc/secretshop.etc"

	// DefaultNPCID is the ordinary Gabriel shop. It has no secret-shop pool.
	DefaultNPCID uint32 = 1000

	maxSourceBytes      = 8 << 20
	maxDocumentTokens   = 1 << 20
	maxSectionIntegers  = 1 << 15
	maxCandidatesInPool = 4096
	maxTotalCandidates  = 1 << 16
	maxPools            = 4096
	maxLevelRanges      = 4096
	maxNPCWeightRecords = 1 << 15
)

var (
	ErrInvalidPVF         = errors.New("invalid secret shop PVF")
	ErrSourceRequired     = errors.New("secret shop PVF source is required")
	ErrCatalogUnavailable = errors.New("secret shop catalog is unavailable")
	ErrUnsupportedNPC     = errors.New("unsupported secret shop NPC")
	ErrLevelNotFound      = errors.New("secret shop level range not found")
	ErrWeightsNotFound    = errors.New("secret shop NPC weights not found")
	ErrInvalidColumn      = errors.New("secret shop NPC weight column is invalid")
)

// Candidate is one item row from a normal or cash pool.
type Candidate struct {
	ItemID     uint32
	CostMode   byte
	Price      int64
	CostItemID uint32
	Count      uint32
	Weight     uint32
}

// Pool is one selector entry. SelectionCount is the number of candidates to
// draw; it is not the number of rows in Candidates.
type Pool struct {
	SelectionCount int
	Candidates     []Candidate
}

// NPCWeight contains the five neutral weighting columns from one NPC-weight
// table row. Callers map the columns to their own confirmed population policy.
type NPCWeight struct {
	NPCID   uint32
	Weights [5]uint32
}

// WeightPair is one NPC and one selected column from the neutral weight table.
type WeightPair struct {
	NPCID  uint32
	Weight uint32
}

// Catalog is immutable after Load. Every accessor returns copied slices so a
// caller cannot alter the shared catalog.
type Catalog struct {
	WaitSeconds           int
	PriceRandomPercent    int
	StackablePercent      int
	ExtraStackablePercent int
	levelRanges           []levelRange
	dungeonWeights        map[uint32][]NPCWeight
	levelWeights          map[uint32][]NPCWeight
	pools                 map[uint32]*npcPools
}

type levelRange struct {
	key uint32
	min uint32
	max uint32
}

type poolKind uint8

const (
	levelPool poolKind = iota
	cashPool
	dungeonPool
	dungeonCashPool
)

type npcPools struct {
	level       map[uint32]Pool
	cash        map[uint32]Pool
	dungeon     map[uint32]Pool
	dungeonCash map[uint32]Pool
}

// Load reads and completely validates etc/secretshop.etc. It returns no
// partially populated catalog on error.
func Load(source dnfpvf.Source) (*Catalog, error) {
	if source == nil {
		return nil, ErrSourceRequired
	}
	text, err := source.ReadText(PVFPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrInvalidPVF, PVFPath, err)
	}
	if len(text) > maxSourceBytes {
		return nil, fmt.Errorf("%w: source has %d bytes, maximum is %d", ErrInvalidPVF, len(text), maxSourceBytes)
	}
	doc, err := dnfpvf.Parse(PVFPath, text)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPVF, err)
	}
	return parseDocument(doc)
}

// SupportedNPCIDs returns the three NPC blocks required by this PVF format.
func (c *Catalog) SupportedNPCIDs() []uint32 {
	return []uint32{1002, 1003, 1004}
}

// NPCWeights returns dungeon weights when available, otherwise level weights
// selected by the inclusive level range containing basisLevel.
func (c *Catalog) NPCWeights(dungeonID uint32, basisLevel int) ([]NPCWeight, error) {
	if c == nil {
		return nil, ErrCatalogUnavailable
	}
	if basisLevel < 0 {
		return nil, fmt.Errorf("%w: basis level %d", ErrInvalidPVF, basisLevel)
	}
	if dungeonID != 0 {
		if rows, ok := c.dungeonWeights[dungeonID]; ok {
			return cloneNPCWeights(rows), nil
		}
	}
	key, ok := c.levelKey(basisLevel)
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrLevelNotFound, basisLevel)
	}
	rows, ok := c.levelWeights[key]
	if !ok {
		return nil, fmt.Errorf("%w: dungeon=%d level=%d", ErrWeightsNotFound, dungeonID, basisLevel)
	}
	return cloneNPCWeights(rows), nil
}

// WeightPairs selects one of the five neutral columns from NPCWeights.
func (c *Catalog) WeightPairs(dungeonID uint32, basisLevel, column int) ([]WeightPair, error) {
	if column < 0 || column >= 5 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidColumn, column)
	}
	rows, err := c.NPCWeights(dungeonID, basisLevel)
	if err != nil {
		return nil, err
	}
	out := make([]WeightPair, len(rows))
	for i, row := range rows {
		out[i] = WeightPair{NPCID: row.NPCID, Weight: row.Weights[column]}
	}
	return out, nil
}

// Pools returns the normal and extra pools for an NPC. A configured dungeon
// selector takes precedence over the level selector, independently for the
// normal and dungeon-cash tables. Missing selectors produce an empty pool.
// NPC 1000 is the ordinary default shop and therefore always has empty pools.
func (c *Catalog) Pools(npcID, dungeonID uint32, basisLevel int) (normal, extra Pool, err error) {
	if c == nil {
		return Pool{}, Pool{}, ErrCatalogUnavailable
	}
	if basisLevel < 0 {
		return Pool{}, Pool{}, fmt.Errorf("%w: basis level %d", ErrInvalidPVF, basisLevel)
	}
	if npcID == DefaultNPCID {
		return Pool{}, Pool{}, nil
	}
	if npcID != 1002 && npcID != 1003 && npcID != 1004 {
		return Pool{}, Pool{}, fmt.Errorf("%w: %d", ErrUnsupportedNPC, npcID)
	}
	selected := c.pools[npcID]
	if selected == nil {
		return Pool{}, Pool{}, fmt.Errorf("%w: missing block %d", ErrCatalogUnavailable, npcID)
	}
	normal, err = c.selectPool(selected.dungeon, selected.level, dungeonID, basisLevel)
	if err != nil {
		return Pool{}, Pool{}, err
	}
	extra, err = c.selectPool(selected.dungeonCash, selected.cash, dungeonID, basisLevel)
	if err != nil {
		return Pool{}, Pool{}, err
	}
	return normal, extra, nil
}

func (c *Catalog) selectPool(dungeonPools, levelPools map[uint32]Pool, dungeonID uint32, basisLevel int) (Pool, error) {
	if dungeonID != 0 {
		if pool, ok := dungeonPools[dungeonID]; ok {
			return clonePool(pool), nil
		}
	}
	key, ok := c.levelKey(basisLevel)
	if !ok {
		return Pool{}, fmt.Errorf("%w: %d", ErrLevelNotFound, basisLevel)
	}
	if pool, ok := levelPools[key]; ok {
		return clonePool(pool), nil
	}
	return Pool{}, nil
}

func (c *Catalog) levelKey(level int) (uint32, bool) {
	if level < 0 || uint64(level) > math.MaxUint32 {
		return 0, false
	}
	value := uint32(level)
	for _, item := range c.levelRanges {
		if value >= item.min && value <= item.max {
			return item.key, true
		}
	}
	return 0, false
}

func clonePool(pool Pool) Pool {
	out := Pool{SelectionCount: pool.SelectionCount}
	if len(pool.Candidates) != 0 {
		out.Candidates = append([]Candidate(nil), pool.Candidates...)
	}
	return out
}

func cloneNPCWeights(rows []NPCWeight) []NPCWeight {
	if len(rows) == 0 {
		return nil
	}
	out := make([]NPCWeight, len(rows))
	copy(out, rows)
	return out
}

type parser struct {
	doc             *dnfpvf.Document
	pos             int
	cat             *Catalog
	seenLevelKeys   map[uint32]struct{}
	seenWeightKeys  map[weightKey]struct{}
	weightRows      int
	poolCount       int
	totalCandidates int
}

type weightKey struct {
	kind byte
	key  uint32
	npc  uint32
}

func parseDocument(doc *dnfpvf.Document) (*Catalog, error) {
	if doc == nil {
		return nil, fmt.Errorf("%w: document is nil", ErrInvalidPVF)
	}
	if len(doc.Tokens) > maxDocumentTokens {
		return nil, fmt.Errorf("%w: %d tokens, maximum is %d", ErrInvalidPVF, len(doc.Tokens), maxDocumentTokens)
	}
	p := &parser{
		doc: doc,
		cat: &Catalog{
			dungeonWeights: make(map[uint32][]NPCWeight),
			levelWeights:   make(map[uint32][]NPCWeight),
			pools:          make(map[uint32]*npcPools),
		},
		seenLevelKeys:  make(map[uint32]struct{}),
		seenWeightKeys: make(map[weightKey]struct{}),
	}
	if err := p.parse(); err != nil {
		return nil, err
	}
	return p.cat, nil
}

func (p *parser) parse() error {
	var err error
	if p.cat.WaitSeconds, err = p.scalarSection("eplp wait time"); err != nil {
		return err
	}
	if p.cat.PriceRandomPercent, err = p.scalarSection("price random"); err != nil {
		return err
	}
	if p.cat.StackablePercent, err = p.scalarSection("cash user stackable count"); err != nil {
		return err
	}
	if p.cat.ExtraStackablePercent, err = p.scalarSection("add slot cash user stackable count"); err != nil {
		return err
	}
	if err = p.parseWeightTable("dungeon npc", "/dungeon npc", 1, p.cat.dungeonWeights); err != nil {
		return err
	}
	if err = p.parseLevelRanges(); err != nil {
		return err
	}
	if err = p.parseWeightTable("level npc", "/level npc", 2, p.cat.levelWeights); err != nil {
		return err
	}
	for _, npcID := range []uint32{1002, 1003, 1004} {
		if err = p.parseNPC(npcID); err != nil {
			return err
		}
	}
	if p.peekSection("event item") {
		if err = p.parseEventItem(); err != nil {
			return err
		}
	}
	if _, err = p.expectSection("secret shop end"); err != nil {
		return err
	}
	if p.pos != len(p.doc.Tokens) {
		return p.invalidToken(p.doc.Tokens[p.pos], "tokens after secret shop end")
	}
	return nil
}

func (p *parser) scalarSection(name string) (int, error) {
	if _, err := p.expectSection(name); err != nil {
		return 0, err
	}
	token, err := p.takeInt(name)
	if err != nil {
		return 0, err
	}
	if token.Int < 0 || int64(int(token.Int)) != token.Int {
		return 0, p.invalidToken(token, "%s value %d is out of range", name, token.Int)
	}
	if p.pos < len(p.doc.Tokens) && p.doc.Tokens[p.pos].Kind != dnfpvf.TokenSection {
		return 0, p.invalidToken(p.doc.Tokens[p.pos], "%s must contain exactly one integer", name)
	}
	return int(token.Int), nil
}

func (p *parser) parseWeightTable(open, close string, kind byte, target map[uint32][]NPCWeight) error {
	if _, err := p.expectSection(open); err != nil {
		return err
	}
	body, err := p.bodyUntil(close)
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body)%7 != 0 {
		return fmt.Errorf("%w: %s has %d integers, expected nonzero multiple of 7", ErrInvalidPVF, open, len(body))
	}
	for offset := 0; offset < len(body); offset += 7 {
		key, err := p.uint32Token(body[offset], open+" key")
		if err != nil {
			return err
		}
		npcID, err := p.uint32Token(body[offset+1], open+" NPC")
		if err != nil {
			return err
		}
		if npcID == 0 {
			return p.invalidToken(body[offset+1], "%s has NPC 0", open)
		}
		dedupe := weightKey{kind: kind, key: key, npc: npcID}
		if _, exists := p.seenWeightKeys[dedupe]; exists {
			return p.invalidToken(body[offset], "duplicate %s key (%d,%d)", open, key, npcID)
		}
		p.seenWeightKeys[dedupe] = struct{}{}
		row := NPCWeight{NPCID: npcID}
		for column := 0; column < 5; column++ {
			row.Weights[column], err = p.uint32Token(body[offset+2+column], open+" weight")
			if err != nil {
				return err
			}
		}
		p.weightRows++
		if p.weightRows > maxNPCWeightRecords {
			return p.invalidToken(body[offset], "too many NPC weight rows")
		}
		target[key] = append(target[key], row)
	}
	return nil
}

func (p *parser) parseLevelRanges() error {
	if _, err := p.expectSection("level section"); err != nil {
		return err
	}
	body, err := p.bodyUntil("/level section")
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body)%3 != 0 {
		return fmt.Errorf("%w: level section has %d integers, expected nonzero multiple of 3", ErrInvalidPVF, len(body))
	}
	if len(body)/3 > maxLevelRanges {
		return p.invalidToken(body[0], "too many level ranges")
	}
	for offset := 0; offset < len(body); offset += 3 {
		key, err := p.uint32Token(body[offset], "level section key")
		if err != nil {
			return err
		}
		minimum, err := p.uint32Token(body[offset+1], "level section minimum")
		if err != nil {
			return err
		}
		maximum, err := p.uint32Token(body[offset+2], "level section maximum")
		if err != nil {
			return err
		}
		if minimum > maximum {
			return p.invalidToken(body[offset], "level range %d has minimum %d above maximum %d", key, minimum, maximum)
		}
		if _, exists := p.seenLevelKeys[key]; exists {
			return p.invalidToken(body[offset], "duplicate level section key %d", key)
		}
		p.seenLevelKeys[key] = struct{}{}
		p.cat.levelRanges = append(p.cat.levelRanges, levelRange{key: key, min: minimum, max: maximum})
	}
	sort.SliceStable(p.cat.levelRanges, func(i, j int) bool {
		if p.cat.levelRanges[i].min != p.cat.levelRanges[j].min {
			return p.cat.levelRanges[i].min < p.cat.levelRanges[j].min
		}
		return p.cat.levelRanges[i].key < p.cat.levelRanges[j].key
	})
	for i := 1; i < len(p.cat.levelRanges); i++ {
		previous := p.cat.levelRanges[i-1]
		current := p.cat.levelRanges[i]
		if current.min <= previous.max {
			return fmt.Errorf("%w: overlapping level ranges %d..%d and %d..%d", ErrInvalidPVF, previous.min, previous.max, current.min, current.max)
		}
	}
	return nil
}

func (p *parser) parseNPC(npcID uint32) error {
	if _, err := p.expectSection("npc"); err != nil {
		return err
	}
	idToken, err := p.takeInt("npc id")
	if err != nil {
		return err
	}
	id, err := p.uint32Token(idToken, "npc id")
	if err != nil {
		return err
	}
	if id != npcID {
		return p.invalidToken(idToken, "NPC block %d found where %d was expected", id, npcID)
	}
	selected := &npcPools{
		level:       make(map[uint32]Pool),
		cash:        make(map[uint32]Pool),
		dungeon:     make(map[uint32]Pool),
		dungeonCash: make(map[uint32]Pool),
	}
	for {
		token, ok := p.current()
		if !ok {
			return fmt.Errorf("%w: NPC %d has no closing [/npc]", ErrInvalidPVF, npcID)
		}
		if token.Kind != dnfpvf.TokenSection {
			return p.invalidToken(token, "expected a pool section or [/npc]")
		}
		switch token.Value {
		case "/npc":
			p.pos++
			p.cat.pools[npcID] = selected
			return nil
		case "level":
			err = p.parsePool(npcID, selected, levelPool, "level", "/level")
		case "cash item":
			err = p.parsePool(npcID, selected, cashPool, "cash item", "/cash item")
		case "dungeon":
			err = p.parsePool(npcID, selected, dungeonPool, "dungeon", "/dungeon", "/dungeon item")
		case "dungeon cash item":
			err = p.parsePool(npcID, selected, dungeonCashPool, "dungeon cash item", "/dungeon cash item")
		default:
			return p.invalidToken(token, "unknown NPC %d subsection [%s]", npcID, token.Value)
		}
		if err != nil {
			return err
		}
	}
}

func (p *parser) parsePool(npcID uint32, selected *npcPools, kind poolKind, open string, closes ...string) error {
	if _, err := p.expectSection(open); err != nil {
		return err
	}
	body, err := p.bodyUntil(closes...)
	if err != nil {
		return err
	}
	if len(body) < 2 {
		return fmt.Errorf("%w: NPC %d [%s] has %d integers, expected selector and selection count", ErrInvalidPVF, npcID, open, len(body))
	}
	if (len(body)-2)%6 != 0 {
		return fmt.Errorf("%w: NPC %d [%s] has %d body integers, expected 2 plus multiples of 6", ErrInvalidPVF, npcID, open, len(body))
	}
	selector, err := p.uint32Token(body[0], open+" selector")
	if err != nil {
		return err
	}
	selectionCount, err := p.uint32Token(body[1], open+" selection count")
	if err != nil {
		return err
	}
	if selectionCount > 8 {
		return p.invalidToken(body[1], "NPC %d [%s] selection count %d exceeds 8", npcID, open, selectionCount)
	}
	count := (len(body) - 2) / 6
	if count > maxCandidatesInPool {
		return p.invalidToken(body[0], "NPC %d [%s] has %d candidates, maximum is %d", npcID, open, count, maxCandidatesInPool)
	}
	p.totalCandidates += count
	if p.totalCandidates > maxTotalCandidates {
		return p.invalidToken(body[0], "catalog exceeds %d candidates", maxTotalCandidates)
	}
	p.poolCount++
	if p.poolCount > maxPools {
		return p.invalidToken(body[0], "catalog exceeds %d pools", maxPools)
	}
	target := selected.poolMap(kind)
	if _, exists := target[selector]; exists {
		return p.invalidToken(body[0], "duplicate NPC %d [%s] selector %d", npcID, open, selector)
	}
	pool := Pool{SelectionCount: int(selectionCount)}
	if count != 0 {
		pool.Candidates = make([]Candidate, 0, count)
	}
	for offset := 2; offset < len(body); offset += 6 {
		itemID, err := p.uint32Token(body[offset], open+" item id")
		if err != nil {
			return err
		}
		if itemID == 0 {
			return p.invalidToken(body[offset], "NPC %d [%s] selector %d has item 0", npcID, open, selector)
		}
		costMode, err := p.uint32Token(body[offset+1], open+" cost mode")
		if err != nil {
			return err
		}
		if costMode > 1 {
			return p.invalidToken(body[offset+1], "NPC %d [%s] selector %d has cost mode %d", npcID, open, selector, costMode)
		}
		if body[offset+2].Int < 0 {
			return p.invalidToken(body[offset+2], "NPC %d [%s] selector %d has negative price", npcID, open, selector)
		}
		costItemID, err := p.uint32Token(body[offset+3], open+" cost item id")
		if err != nil {
			return err
		}
		itemCount, err := p.uint32Token(body[offset+4], open+" item count")
		if err != nil {
			return err
		}
		weight, err := p.uint32Token(body[offset+5], open+" item weight")
		if err != nil {
			return err
		}
		pool.Candidates = append(pool.Candidates, Candidate{
			ItemID:     itemID,
			CostMode:   byte(costMode),
			Price:      body[offset+2].Int,
			CostItemID: costItemID,
			Count:      itemCount,
			Weight:     weight,
		})
	}
	target[selector] = pool
	return nil
}

func (n *npcPools) poolMap(kind poolKind) map[uint32]Pool {
	switch kind {
	case levelPool:
		return n.level
	case cashPool:
		return n.cash
	case dungeonPool:
		return n.dungeon
	case dungeonCashPool:
		return n.dungeonCash
	default:
		return nil
	}
}

func (p *parser) parseEventItem() error {
	if _, err := p.expectSection("event item"); err != nil {
		return err
	}
	body, err := p.bodyUntil("/event item")
	if err != nil {
		return err
	}
	if len(body) != 7 {
		return fmt.Errorf("%w: event item has %d integers, expected 7", ErrInvalidPVF, len(body))
	}
	for _, token := range body {
		if _, err := p.uint32Token(token, "event item"); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) bodyUntil(closes ...string) ([]dnfpvf.Token, error) {
	if len(closes) == 0 {
		return nil, fmt.Errorf("%w: parser has no closing section", ErrInvalidPVF)
	}
	body := make([]dnfpvf.Token, 0)
	for {
		token, ok := p.current()
		if !ok {
			return nil, fmt.Errorf("%w: expected closing section [%s]", ErrInvalidPVF, closes[0])
		}
		if token.Kind == dnfpvf.TokenSection {
			for _, close := range closes {
				if token.Value == close {
					p.pos++
					return body, nil
				}
			}
			return nil, p.invalidToken(token, "unexpected section [%s], expected [%s]", token.Value, closes[0])
		}
		if token.Kind != dnfpvf.TokenInt {
			return nil, p.invalidToken(token, "expected integer, got %s", token.Kind)
		}
		if len(body) >= maxSectionIntegers {
			return nil, p.invalidToken(token, "section exceeds %d integers", maxSectionIntegers)
		}
		body = append(body, token)
		p.pos++
	}
}

func (p *parser) current() (dnfpvf.Token, bool) {
	if p.pos < 0 || p.pos >= len(p.doc.Tokens) {
		return dnfpvf.Token{}, false
	}
	return p.doc.Tokens[p.pos], true
}

func (p *parser) peekSection(name string) bool {
	token, ok := p.current()
	return ok && token.Kind == dnfpvf.TokenSection && token.Value == name
}

func (p *parser) expectSection(name string) (dnfpvf.Token, error) {
	token, ok := p.current()
	if !ok {
		return dnfpvf.Token{}, fmt.Errorf("%w: expected section [%s] at end of document", ErrInvalidPVF, name)
	}
	if token.Kind != dnfpvf.TokenSection || token.Value != name {
		return dnfpvf.Token{}, p.invalidToken(token, "expected section [%s]", name)
	}
	p.pos++
	return token, nil
}

func (p *parser) takeInt(label string) (dnfpvf.Token, error) {
	token, ok := p.current()
	if !ok {
		return dnfpvf.Token{}, fmt.Errorf("%w: expected %s at end of document", ErrInvalidPVF, label)
	}
	if token.Kind != dnfpvf.TokenInt {
		return dnfpvf.Token{}, p.invalidToken(token, "expected integer %s", label)
	}
	p.pos++
	return token, nil
}

func (p *parser) uint32Token(token dnfpvf.Token, label string) (uint32, error) {
	if token.Kind != dnfpvf.TokenInt {
		return 0, p.invalidToken(token, "expected integer %s", label)
	}
	if token.Int < 0 || uint64(token.Int) > math.MaxUint32 {
		return 0, p.invalidToken(token, "%s value %d is outside uint32", label, token.Int)
	}
	return uint32(token.Int), nil
}

func (p *parser) invalidToken(token dnfpvf.Token, format string, args ...any) error {
	return fmt.Errorf("%w at %s:%d:%d: %s", ErrInvalidPVF, p.doc.Path, token.Line, token.Column, fmt.Sprintf(format, args...))
}
