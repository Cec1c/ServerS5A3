package gmtool

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 物品索引：A21 的发放页依赖一个可搜索的物品表（id/名称/类型/品级/等级/期限）。
// 90CN 侧没有现成索引，这里从运行时 PVF 的 stackable.lst / equipment.lst
// 逐条解析生成，并缓存到磁盘，避免每次重启都重新解析。
const (
	itemListStackable  = "stackable/stackable.lst"
	itemListEquipment  = "equipment/equipment.lst"
	itemIndexCacheFile = "gm-item-index.json"
	itemIndexVersion   = 4
)

var (
	itemTagPattern = regexp.MustCompile(`\[([^\]\[]+)\]`)
	itemIndexDate  = time.FixedZone("CST", 8*60*60)
)

type itemEntry struct {
	ID                 uint32   `json:"itemId"`
	Name               string   `json:"name"`
	Kind               string   `json:"kind"`
	Tag                string   `json:"tag"`
	Segment            string   `json:"segment,omitempty"`
	Special            string   `json:"special,omitempty"`
	Rarity             int      `json:"rarity"`
	MinLevel           int      `json:"minLevel"`
	Grade              int      `json:"grade"`
	StackLimit         int64    `json:"stackLimit"`
	SlotStart          int16    `json:"-"`
	SlotEnd            int16    `json:"-"`
	PVFPath            string   `json:"pvfPath"`
	UsableJob          string   `json:"usableJob"`
	UsableJobLabels    []string `json:"usableJobLabels"`
	UsablePeriodDays   int64    `json:"usablePeriodDays"`
	AbsoluteExpireUnix int64    `json:"absoluteExpireTime"`
	Invalid            bool     `json:"invalid,omitempty"`
}

// documentPath 返回该条目在 PVF 归档里的完整读取路径：`.lst` 里记的通常是相对
// 该种类根的路径（如 stackable.lst 里的 `490007001/chn_490007493.stk`），
// 这里补上种类根前缀。parseItemEntry 与物品详情共用这一处实现，避免两套规则漂移。
func (e itemEntry) documentPath() string {
	if e.Kind == "" {
		return e.PVFPath
	}
	if strings.HasPrefix(strings.ToLower(e.PVFPath), strings.ToLower(e.Kind)+"/") {
		return e.PVFPath
	}
	return e.Kind + "/" + strings.TrimPrefix(e.PVFPath, "/")
}

type itemIndexFile struct {
	Version     int         `json:"version"`
	BuiltAt     string      `json:"builtAt"`
	PVFChecksum string      `json:"pvfChecksum"`
	Entries     []itemEntry `json:"entries"`
}

// Bind disk caches to the loaded archive, not its path or modification time.
// Sources without an archive SHA256 are still searchable, but never reuse disk caches.
func itemIndexSourceChecksum(source dnfpvf.Source) string {
	snapshotter, ok := source.(envPVFSnapshotter)
	if !ok {
		return ""
	}
	checksum := strings.ToLower(strings.TrimSpace(snapshotter.Snapshot().Checksum))
	digest, err := hex.DecodeString(checksum)
	if err != nil || len(digest) != 32 {
		return ""
	}
	return checksum
}

type itemIndex struct {
	mu      sync.RWMutex
	ready   bool
	buildEr error

	entries []itemEntry
	byID    map[uint32]int

	cachePath string
	srv       *Server

	startOnce sync.Once
}

func newItemIndex(cachePath string, srv *Server, logger func(string, ...any)) *itemIndex {
	if strings.TrimSpace(cachePath) == "" {
		cachePath = filepath.Join(os.TempDir(), itemIndexCacheFile)
	}
	return &itemIndex{cachePath: cachePath, srv: srv, byID: make(map[uint32]int)}
}

func (ix *itemIndex) start(ctx context.Context) {
	ix.startOnce.Do(func() {
		if err := ix.loadCache(); err != nil {
			ix.srv.cfg.Logger("dnf90 gm item index cache unusable", "error", err)
		}
		if ix.isReady() {
			return
		}
		started := time.Now()
		if err := ix.build(ctx); err != nil {
			ix.mu.Lock()
			ix.buildEr = err
			ix.mu.Unlock()
			ix.srv.cfg.Logger("dnf90 gm item index build failed", "error", err)
			return
		}
		ix.mu.RLock()
		count := len(ix.entries)
		ix.mu.RUnlock()
		ix.srv.cfg.Logger("dnf90 gm item index ready",
			"items", count, "elapsed_ms", time.Since(started).Milliseconds())
	})
}

// 下面几个读取方法都带 nil 守卫：Server 在测试里可以只装配需要的部分，
// 缓存未注入时应当「查不到」而不是 panic。

func (ix *itemIndex) isReady() bool {
	if ix == nil {
		return false
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.ready
}

func (ix *itemIndex) failure() error {
	if ix == nil {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.buildEr
}

func (ix *itemIndex) snapshot() []itemEntry {
	if ix == nil {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.entries
}

func (ix *itemIndex) lookup(itemID uint32) (itemEntry, bool) {
	if ix == nil {
		return itemEntry{}, false
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	pos, ok := ix.byID[itemID]
	if !ok {
		return itemEntry{}, false
	}
	return ix.entries[pos], true
}

func (ix *itemIndex) name(itemID uint32) string {
	if ix == nil {
		return ""
	}
	if entry, ok := ix.lookup(itemID); ok {
		return entry.Name
	}
	return ""
}

func (ix *itemIndex) loadCache() error {
	raw, err := os.ReadFile(ix.cachePath)
	if err != nil {
		return err
	}
	var payload itemIndexFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if payload.Version != itemIndexVersion || len(payload.Entries) == 0 {
		return fmt.Errorf("item index cache version %d is stale", payload.Version)
	}
	source, err := ix.srv.source()
	if err != nil {
		return err
	}
	checksum := itemIndexSourceChecksum(source)
	if checksum == "" || payload.PVFChecksum != checksum {
		return fmt.Errorf("item index cache does not match the loaded PVF")
	}
	ix.mu.Lock()
	ix.setEntriesLocked(payload.Entries)
	ix.mu.Unlock()
	return nil
}

func (ix *itemIndex) setEntriesLocked(entries []itemEntry) {
	ix.entries = entries
	ix.byID = make(map[uint32]int, len(entries))
	for pos := range entries {
		ix.byID[entries[pos].ID] = pos
	}
	ix.ready = true
	ix.buildEr = nil
}

func (ix *itemIndex) build(ctx context.Context) error {
	source, err := ix.srv.source()
	if err != nil {
		return err
	}
	refs := make(map[uint32]itemEntry, 4096)
	if err := collectItemRefs(source, itemListStackable, "stackable", refs); err != nil {
		return err
	}
	if err := collectItemRefs(source, itemListEquipment, "equipment", refs); err != nil {
		return err
	}
	ids := make([]uint32, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	entries := make([]itemEntry, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := refs[id]
		parseItemEntry(source, &entry)
		if strings.TrimSpace(entry.Name) == "" {
			// 没有名字的条目对发放页没有意义，且会让搜索出现空白行。
			continue
		}
		entries = append(entries, entry)
	}

	ix.mu.Lock()
	ix.setEntriesLocked(entries)
	ix.mu.Unlock()
	ix.writeCache(entries, itemIndexSourceChecksum(source))
	return nil
}

func (ix *itemIndex) writeCache(entries []itemEntry, checksum string) {
	if checksum == "" {
		return
	}
	payload := itemIndexFile{Version: itemIndexVersion, BuiltAt: time.Now().UTC().Format(time.RFC3339), PVFChecksum: checksum, Entries: entries}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	tmp := ix.cachePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		ix.srv.cfg.Logger("dnf90 gm item index cache write failed", "error", err)
		return
	}
	if err := os.Rename(tmp, ix.cachePath); err != nil {
		ix.srv.cfg.Logger("dnf90 gm item index cache rename failed", "error", err)
	}
}

func collectItemRefs(source dnfpvf.Source, listPath, kind string, out map[uint32]itemEntry) error {
	text, err := source.ReadText(listPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", listPath, err)
	}
	document, err := dnfpvf.Parse(listPath, text)
	if err != nil {
		return fmt.Errorf("parse %s: %w", listPath, err)
	}
	for _, entry := range dnfpvf.ParseList(document) {
		if entry.ID <= 0 || entry.ID > int64(^uint32(0)) {
			continue
		}
		id := uint32(entry.ID)
		if kind == "equipment" {
			if _, exists := out[id]; exists {
				continue
			}
		}
		out[id] = itemEntry{ID: id, Kind: kind, PVFPath: entry.Path}
	}
	return nil
}

func parseItemEntry(source dnfpvf.Source, entry *itemEntry) {
	if source == nil || entry == nil {
		return
	}
	path := entry.documentPath()
	text, err := source.ReadText(path)
	if err != nil {
		entry.Invalid = true
		return
	}
	document, err := dnfpvf.Parse(path, text)
	if err != nil {
		entry.Invalid = true
		return
	}
	entry.Name, _ = document.Text("name")
	entry.Name = strings.TrimSpace(entry.Name)
	if rarity, ok := document.Int("rarity"); ok {
		entry.Rarity = int(rarity)
	} else {
		entry.Rarity = -1
	}
	if grade, ok := document.Int("grade"); ok {
		entry.Grade = int(grade)
	}
	if limit, ok := document.Int("stack limit"); ok && limit > 0 {
		entry.StackLimit = limit
	}
	if minLevel, ok := document.Int("minimum level"); ok && minLevel > 0 {
		entry.MinLevel = int(minLevel)
	}
	if period, ok := document.Int("usable period"); ok && period > 0 {
		entry.UsablePeriodDays = period
	}
	if raw, ok := document.Text("expiration date"); ok {
		if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(raw), itemIndexDate); err == nil {
			entry.AbsoluteExpireUnix = parsed.Unix()
		}
	}
	if usableJob, ok := document.Text("usable job"); ok {
		entry.UsableJob = strings.TrimSpace(usableJob)
		entry.UsableJobLabels = usableJobLabels(entry.UsableJob)
	}
	if entry.Kind == "stackable" {
		stackableType, _ := document.Text("stackable type")
		entry.Tag = firstItemTag(stackableType)
		entry.Segment = stackSegment(entry.Tag)
	} else {
		equipmentType, _ := document.Text("equipment type")
		entry.Tag = firstItemTag(equipmentType)
	}
	entry.Special = itemSpecial(text)
}

// firstItemTag 返回类型串里的第一个 [tag]，lowercase，与 A21 的 FirstTag 同义。
func firstItemTag(value string) string {
	match := itemTagPattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return strings.ToLower(strings.Join(strings.Fields(match[1]), " "))
}

func stackSegment(tag string) string {
	switch {
	case tag == "":
		return "消耗品"
	case strings.HasPrefix(tag, "creature"), strings.HasPrefix(tag, "feed"):
		return "宠物消耗品"
	case strings.HasPrefix(tag, "material expert job"):
		return "副职业材料"
	case strings.HasPrefix(tag, "material"):
		return "材料"
	case strings.HasPrefix(tag, "quest"):
		return "任务品"
	case strings.HasPrefix(tag, "avatar emblem"):
		return "徽章"
	default:
		return "消耗品"
	}
}

func itemSpecial(text string) string {
	lower := strings.ToLower(text)
	if idx := strings.Index(lower, "[item category]"); idx >= 0 {
		value := strings.TrimLeft(lower[idx+len("[item category]"):], " `\t")
		switch {
		case strings.HasPrefix(value, "legacy"):
			return "legacy"
		case strings.HasPrefix(value, "boss drop"):
			return "boss"
		}
	}
	if strings.Contains(lower, "[random option]") {
		return "sealed"
	}
	return ""
}

var usableJobTokenLabels = map[string]string{
	"swordman":   "鬼剑士",
	"fighter":    "格斗家",
	"gunner":     "神枪手",
	"mage":       "魔法师",
	"priest":     "圣职者",
	"thief":      "暗夜使者",
	"at gunner":  "女神枪手",
	"at fighter": "男格斗家",
	"at mage":    "男魔法师",
	"at priest":  "圣职者(女)",
	"any":        "所有职业",
	"all":        "所有职业",
}

func usableJobLabels(usableJob string) []string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(usableJob, "`", ""), "_", " "))
	normalized = strings.Join(strings.Fields(normalized), " ")
	if normalized == "" {
		return nil
	}
	tokens := itemTagPattern.FindAllStringSubmatch(usableJob, -1)
	labels := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		value := strings.ToLower(strings.Join(strings.Fields(token[1]), " "))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		if label, ok := usableJobTokenLabels[value]; ok {
			labels = append(labels, label)
			continue
		}
		labels = append(labels, value)
	}
	if len(labels) == 0 {
		labels = append(labels, normalized)
	}
	return labels
}

// searchItems 复刻 A21 的 /api/items/browse 过滤语义。
type itemSearchQuery struct {
	Q          string
	Kind       string
	Tags       []string
	Segments   []string
	Special    string
	MinLevel   int
	MaxLevel   int
	Rarity     int
	Limit      int
	Offset     int
	UsableJob  int
	Expiration string
	// Group 是上游 GMSearchItemPage 的分组过滤（stackable/pet/avatar/equipment/other）。
	// 空串 = 不过滤（本仓既有行为不变）；"all" 是上游的显式逃生口，同样视为不过滤。
	Group string
}

func (ix *itemIndex) search(query itemSearchQuery) ([]itemEntry, int) {
	entries := ix.snapshot()
	now := time.Now().Unix()
	matched := make([]itemEntry, 0, 64)
	for _, entry := range entries {
		if !matchItemEntry(entry, query, now) {
			continue
		}
		matched = append(matched, entry)
	}
	total := len(matched)
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(matched) {
		return nil, total
	}
	end := offset + query.Limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], total
}

func matchItemEntry(entry itemEntry, query itemSearchQuery, now int64) bool {
	if query.Kind != "" && entry.Kind != query.Kind {
		return false
	}
	if query.Group != "" && !strings.EqualFold(query.Group, "all") && itemGroup(entry) != strings.ToLower(query.Group) {
		return false
	}
	if len(query.Tags) > 0 && !containsFold(query.Tags, entry.Tag) {
		return false
	}
	if len(query.Segments) > 0 && !containsFold(query.Segments, entry.Segment) {
		return false
	}
	if query.Special != "" && !strings.EqualFold(entry.Special, query.Special) {
		return false
	}
	if query.Rarity >= 0 && entry.Rarity != query.Rarity {
		return false
	}
	if query.MinLevel > 0 && entry.MinLevel < query.MinLevel {
		return false
	}
	if query.MaxLevel > 0 && entry.MinLevel > query.MaxLevel {
		return false
	}
	if query.Q != "" {
		if numeric, err := strconv.ParseUint(query.Q, 10, 32); err == nil {
			if uint32(numeric) != entry.ID {
				return false
			}
		} else if !strings.Contains(strings.ToLower(entry.Name), strings.ToLower(query.Q)) {
			return false
		}
	}
	if query.UsableJob != -1 && entry.Kind == "equipment" {
		switch {
		case query.UsableJob == -2:
			if !isUnrestrictedUsableJob(entry.UsableJob) {
				return false
			}
		case query.UsableJob >= 0:
			if !usableByJob(entry.UsableJob, query.UsableJob) {
				return false
			}
		}
	}
	if !matchItemExpiration(entry, query.Expiration, now) {
		return false
	}
	return true
}

// itemGroup 是上游 gmItemGroup（gm_extended.go:756）在本仓 itemEntry 上的等价实现：
// 先按 kind 把 stackable 归为 "stackable"，其余按装备类型的第一个 [tag] 分到
// pet / avatar / equipment，都不匹配则落 "other"。
//
// 与上游的差异只有一处：上游对整串 `equipment type` 做首尾方括号裁剪
// （gmItemTypeTag，gm_extended.go:732），复合类型串会被裁歪；本仓用
// itemEntry.Tag（PVF 里的第一个 [tag]，见 firstItemTag）——单 tag 的常见形态
// 两者结果一致，复合串本仓更稳。
func itemGroup(entry itemEntry) string {
	if entry.Kind == "stackable" {
		return "stackable"
	}
	switch entry.Tag {
	case "creature", "artifact red", "artifact blue", "artifact green":
		return "pet"
	case "hat avatar", "hair avatar", "face avatar", "coat avatar", "breast avatar",
		"waist avatar", "pants avatar", "shoes avatar", "skin avatar", "aurora avatar",
		"weapon avatar":
		return "avatar"
	case "weapon", "coat", "shoulder", "pants", "shoes", "waist", "amulet", "wrist",
		"ring", "support", "magic stone", "support weapon", "title name", "name tag":
		return "equipment"
	default:
		return "other"
	}
}

func containsFold(values []string, candidate string) bool {
	for _, value := range values {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}

func isUnrestrictedUsableJob(usableJob string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(usableJob, "`", ""))
	normalized = strings.Join(strings.Fields(normalized), " ")
	return normalized == "" || normalized == "[any]" || normalized == "[all]"
}

// usableByJob 用职业 token 做包含匹配，无法判断时按“可用”处理，避免漏发。
func usableByJob(usableJob string, job int) bool {
	normalized := strings.ToLower(usableJob)
	if normalized == "" {
		return true
	}
	tokens := itemTagPattern.FindAllStringSubmatch(normalized, -1)
	if len(tokens) == 0 {
		return true
	}
	jobTokens := jobUsableTokens(job)
	for _, token := range tokens {
		value := strings.Join(strings.Fields(token[1]), " ")
		for _, jobToken := range jobTokens {
			if value == jobToken {
				return true
			}
		}
	}
	return false
}

// jobUsableTokens 返回某个职业家族在 PVF [usable job] 里可能出现的全部 token。
func jobUsableTokens(job int) []string {
	switch {
	case job >= 0 && job < 10:
		return []string{"swordman", "at swordman"}
	case job >= 10 && job < 20:
		return []string{"fighter", "at fighter"}
	case job >= 20 && job < 30:
		return []string{"gunner", "at gunner"}
	case job >= 30 && job < 40:
		return []string{"mage", "at mage"}
	case job >= 40 && job < 50:
		return []string{"priest", "at priest"}
	case job >= 50 && job < 60:
		return []string{"thief"}
	default:
		return nil
	}
}

func matchItemExpiration(entry itemEntry, filter string, now int64) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" || filter == "all" {
		return true
	}
	hasAbsolute := entry.AbsoluteExpireUnix > 0
	hasPeriod := entry.UsablePeriodDays > 0
	switch filter {
	case "limited":
		return hasAbsolute || hasPeriod
	case "none":
		return !hasAbsolute && !hasPeriod
	case "relative":
		return hasPeriod
	case "absolute":
		return hasAbsolute
	case "expired":
		return hasAbsolute && entry.AbsoluteExpireUnix <= now
	default:
		return true
	}
}
