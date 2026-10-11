package gmtool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	dnfcerashop "longheng.io/server/internal/modules/dnf/cerashop"
	dnfcharstat "longheng.io/server/internal/modules/dnf/charstat"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var (
	errRepositoriesUnavailable = errors.New("90CN 仓储尚未就绪")
	errCharacterMissing        = errors.New("角色不存在")
	errAccountMissing          = errors.New("账号不存在")
)

// pvfCache 缓存从运行时 PVF 解析出来的只读表，避免每次请求都重新解析。
type pvfCache struct {
	mu sync.Mutex

	progression     *progression.Tables
	progressionErr  error
	progressionDone bool

	profiles    *dnfprofession.Profiles
	catalog     *dnfskill.Table
	profErr     error
	profileDone bool

	quests    *dnfquest.Catalog
	questErr  error
	questDone bool

	stats     *dnfcharstat.Table
	statsErr  error
	statsDone bool

	dungeons    *worldmap.Table
	dungeonErr  error
	dungeonDone bool

	honor     *dnfhonor.Tables
	honorErr  error
	honorDone bool

	partSets    map[int64]partSetReference
	partSetErr  error
	partSetDone bool
}

// partSetReference 是 etc/equipmentpartset.etc 里一条 `[equipment part set]` 记录：
// 套装 ID、被引用的 .equ 相对路径、套装显示名。
type partSetReference struct {
	Path string
	Name string
}

func newPVFCache() *pvfCache { return &pvfCache{} }

// equipmentPartSets 解析并缓存 etc/equipmentpartset.etc（90CN 真档 939,675 字节）。
//
// 上游 gmEquipmentSetDetail（gm.go:1625）每次请求都重读重解析这个列表；GM 面板的
// 物品详情是按需查询，缓存一份明显更合适。失败同样缓存（与 pvfCache 其余条目一致，
// 保证错误可复现且不会每请求重试打满磁盘）。
//
// 解析口径与上游的差别：上游在整份 token 流里线性找「第一个等于 setID 的整数
// token」，那个整数可能是别的套装块内部的件数（真档里 0/2/3 这些值到处出现）；
// 这里改成按 `[equipment part set]` 段边界取每块的第一个整数，同一个数据、不会串块。
func (c *pvfCache) equipmentPartSets(s *Server) (map[int64]partSetReference, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.partSetDone {
		return c.partSets, c.partSetErr
	}
	c.partSetDone = true
	source, err := s.source()
	if err != nil {
		c.partSetErr = err
		return nil, err
	}
	text, err := source.ReadText(equipmentPartSetListPath)
	if err != nil {
		c.partSetErr = fmt.Errorf("读取 %s 失败: %w", equipmentPartSetListPath, err)
		return nil, c.partSetErr
	}
	document, err := dnfpvf.Parse(equipmentPartSetListPath, text)
	if err != nil {
		c.partSetErr = fmt.Errorf("解析 %s 失败: %w", equipmentPartSetListPath, err)
		return nil, c.partSetErr
	}
	references := make(map[int64]partSetReference, 512)
	for _, section := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(section.Name), "equipment part set") {
			continue
		}
		tokens := documentSectionTokens(document, section)
		if len(tokens) < 2 || tokens[0].Kind != dnfpvf.TokenInt {
			continue
		}
		setID := tokens[0].Int
		if setID <= 0 {
			continue
		}
		if _, exists := references[setID]; exists {
			continue
		}
		pathToken := tokens[1]
		if pathToken.Kind != dnfpvf.TokenString && pathToken.Kind != dnfpvf.TokenIdent {
			continue
		}
		reference := partSetReference{Path: strings.TrimSpace(strings.Trim(pathToken.Value, "`\"'"))}
		if len(tokens) > 2 {
			nameToken := tokens[2]
			if nameToken.Kind == dnfpvf.TokenString || nameToken.Kind == dnfpvf.TokenIdent {
				reference.Name = strings.Join(strings.Fields(nameToken.Value), " ")
			}
		}
		references[setID] = reference
	}
	c.partSets = references
	return references, nil
}

func (s *Server) repositories() (dnfrepo.Group, error) {
	group, ok := s.deps.Repos()
	if !ok {
		return dnfrepo.Group{}, errRepositoriesUnavailable
	}
	if group.Character == nil || group.Skill == nil || group.Inventory == nil || group.Mailbox == nil {
		return dnfrepo.Group{}, errRepositoriesUnavailable
	}
	return group, nil
}

func (s *Server) source() (dnfpvf.Source, error) {
	return s.deps.PVF()
}

func (c *pvfCache) progressionTables(s *Server) (*progression.Tables, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progressionDone {
		return c.progression, c.progressionErr
	}
	c.progressionDone = true
	source, err := s.source()
	if err != nil {
		c.progressionErr = err
		return nil, err
	}
	tables, err := progression.Load(context.Background(), source)
	if err != nil {
		c.progressionErr = fmt.Errorf("加载成长/技能点表失败: %w", err)
		return nil, c.progressionErr
	}
	c.progression = tables
	return tables, nil
}

func (c *pvfCache) professionResources(s *Server) (*dnfprofession.Profiles, *dnfskill.Table, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.profileDone {
		return c.profiles, c.catalog, c.profErr
	}
	c.profileDone = true
	source, err := s.source()
	if err != nil {
		c.profErr = err
		return nil, nil, err
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		c.profErr = fmt.Errorf("构建技能表失败: %w", err)
		return nil, nil, c.profErr
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		c.profErr = fmt.Errorf("加载技能表失败: %w", err)
		return nil, nil, c.profErr
	}
	profiles, err := dnfprofession.LoadProfiles(context.Background(), source, catalog)
	if err != nil {
		c.profErr = fmt.Errorf("加载职业成长数据失败: %w", err)
		return nil, nil, c.profErr
	}
	c.catalog = catalog
	c.profiles = profiles
	return profiles, catalog, nil
}

// questCatalog 加载运行时 PVF 的任务目录。GM 面板只在「设置成长/觉醒」时用它
// 反查该职业/成长段对应的转职与觉醒任务（见 quest.Catalog 的
// ProfessionProgressionQuestIDs），结果与 dnfbridge 登录时使用的目录同源。
func (c *pvfCache) questCatalog(s *Server) (*dnfquest.Catalog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.questDone {
		return c.quests, c.questErr
	}
	c.questDone = true
	source, err := s.source()
	if err != nil {
		c.questErr = err
		return nil, err
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		c.questErr = fmt.Errorf("构建任务表失败: %w", err)
		return nil, c.questErr
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		c.questErr = fmt.Errorf("加载任务表失败: %w", err)
		return nil, c.questErr
	}
	c.quests = catalog
	return catalog, nil
}

func (c *pvfCache) characterStatTable(s *Server) (*dnfcharstat.Table, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statsDone {
		return c.stats, c.statsErr
	}
	c.statsDone = true
	source, err := s.source()
	if err != nil {
		c.statsErr = err
		return nil, err
	}
	table, err := dnfcharstat.Load(context.Background(), source, dnfcharstat.Options{})
	if err != nil {
		c.statsErr = fmt.Errorf("加载角色成长属性表失败: %w", err)
		return nil, c.statsErr
	}
	c.stats = table
	return table, nil
}

func (c *pvfCache) honorTables(s *Server) (*dnfhonor.Tables, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.honorDone {
		return c.honor, c.honorErr
	}
	c.honorDone = true
	source, err := s.source()
	if err != nil {
		c.honorErr = err
		return nil, err
	}
	tables, err := dnfhonor.LoadTables(source)
	if err != nil {
		c.honorErr = fmt.Errorf("加载荣誉表失败: %w", err)
		return nil, c.honorErr
	}
	c.honor = tables
	return tables, nil
}

// ---------------------------------------------------------------------------
// 账号
// ---------------------------------------------------------------------------

type webAccount struct {
	PanelID     int64
	Account     string
	AccountID   string
	CreatedAt   time.Time
	LastLoginAt time.Time
	LastLoginIP string
}

func (s *Server) webAccounts(ctx context.Context) ([]webAccount, error) {
	query := "SELECT id, account, account_id, created_at, last_login_at, last_login_ip FROM " +
		quoteIdentifier(s.cfg.Table) + " ORDER BY id"
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("读取账号列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	accounts := make([]webAccount, 0, 32)
	for rows.Next() {
		var (
			item        webAccount
			createdAt   sql.NullTime
			lastLoginAt sql.NullTime
			lastLoginIP sql.NullString
		)
		if err := rows.Scan(&item.PanelID, &item.Account, &item.AccountID, &createdAt, &lastLoginAt, &lastLoginIP); err != nil {
			return nil, err
		}
		item.CreatedAt = nullTime(createdAt)
		item.LastLoginAt = nullTime(lastLoginAt)
		item.LastLoginIP = strings.TrimSpace(lastLoginIP.String)
		accounts = append(accounts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

func (s *Server) webAccountByPanelID(ctx context.Context, panelID int64) (webAccount, error) {
	accounts, err := s.webAccounts(ctx)
	if err != nil {
		return webAccount{}, err
	}
	for _, account := range accounts {
		if account.PanelID == panelID {
			return account, nil
		}
	}
	return webAccount{}, errAccountMissing
}

func (s *Server) charactersForAccount(ctx context.Context, group dnfrepo.Group, accountID string) ([]dnfrepo.CharacterRecord, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, errAccountMissing
	}
	records, err := group.Character.ListByAccount(ctx, accountID, dnfrepo.DefaultCharacterSlotLimit)
	if err != nil {
		return nil, fmt.Errorf("读取角色列表失败: %w", err)
	}
	visible := make([]dnfrepo.CharacterRecord, 0, len(records))
	for _, record := range records {
		if statInt(record, "delete_flag") != 0 {
			continue
		}
		visible = append(visible, record)
	}
	return visible, nil
}

// ---------------------------------------------------------------------------
// 角色
// ---------------------------------------------------------------------------

func (s *Server) loadCharacter(ctx context.Context, group dnfrepo.Group, panelID int) (dnfrepo.CharacterRecord, error) {
	if panelID <= 0 {
		return dnfrepo.CharacterRecord{}, errCharacterMissing
	}
	characterID := strconv.Itoa(panelID)
	record, found, err := group.Character.Load(ctx, characterID)
	if err != nil {
		return dnfrepo.CharacterRecord{}, fmt.Errorf("读取角色失败: %w", err)
	}
	if !found || strings.TrimSpace(record.CharacterID) != characterID {
		return dnfrepo.CharacterRecord{}, errCharacterMissing
	}
	return record, nil
}

func (s *Server) loadCharacterSkill(ctx context.Context, group dnfrepo.Group, characterID string) (dnfrepo.SkillRecord, error) {
	record, found, err := group.Skill.Load(ctx, characterID)
	if err != nil {
		return dnfrepo.SkillRecord{}, fmt.Errorf("读取技能数据失败: %w", err)
	}
	if !found {
		return dnfrepo.SkillRecord{CharacterID: characterID}, nil
	}
	return record, nil
}

// saveCharacterStats 只写回角色数值字段（key 必须是 dnf_character_stats 的列名）。
func (s *Server) saveCharacterStats(ctx context.Context, group dnfrepo.Group, record dnfrepo.CharacterRecord) error {
	record = dnfrepo.CloneCharacter(record)
	if record.Stats == nil {
		record.Stats = make(map[string]int64)
	}
	if err := dnfrepo.SaveCharacterFields(ctx, group.Character, record, dnfrepo.CharacterFieldBase, dnfrepo.CharacterFieldStats); err != nil {
		return fmt.Errorf("写入角色数据失败: %w", err)
	}
	return nil
}

func (s *Server) saveSkillPoints(ctx context.Context, group dnfrepo.Group, record dnfrepo.SkillRecord) error {
	record = dnfrepo.CloneSkill(record)
	if err := dnfrepo.SaveSkillFields(ctx, group.Skill, record, dnfrepo.SkillFieldPoints, dnfrepo.SkillFieldSkills, dnfrepo.SkillFieldLayouts); err != nil {
		return fmt.Errorf("写入技能数据失败: %w", err)
	}
	return nil
}

// accountCera 读取 90CN 权威的账号级点券。
// 服务端把点券存在 dnf_accounts 的 metadata["account_cera"]（见
// dnfbridge/account_wallet.go 与 modules/dnf/cerashop）；角色表的 cera 列
// 只是历史遗留的只读副本，写它不会生效。
func (s *Server) accountCera(ctx context.Context, group dnfrepo.Group, accountID string) int64 {
	accountID = strings.TrimSpace(accountID)
	if group.Account == nil || accountID == "" {
		return 0
	}
	record, found, err := group.Account.Load(ctx, accountID)
	if err != nil || !found {
		return 0
	}
	return dnfcerashop.Balance(record)
}

// setAccountCera 覆写账号级点券并持久化。
func (s *Server) setAccountCera(ctx context.Context, group dnfrepo.Group, accountID string, balance int64) error {
	accountID = strings.TrimSpace(accountID)
	if group.Account == nil || accountID == "" {
		return errAccountMissing
	}
	record, found, err := group.Account.Load(ctx, accountID)
	if err != nil {
		return fmt.Errorf("读取账号失败: %w", err)
	}
	if !found {
		return errAccountMissing
	}
	record = dnfrepo.CloneAccount(record)
	dnfcerashop.SetBalance(&record, balance)
	if err := group.Account.Save(ctx, record); err != nil {
		return fmt.Errorf("写入账号点券失败: %w", err)
	}
	return nil
}

func jobNumber(record dnfrepo.CharacterRecord) int {
	value, err := strconv.Atoi(strings.TrimSpace(record.Job))
	if err != nil {
		return 0
	}
	return value
}

// panelIDFromAccountID 把 "dnf:10001" 还原成面板里的账号主键 10001。
func panelIDFromAccountID(accountID string) int {
	trimmed := strings.TrimSpace(accountID)
	if idx := strings.LastIndex(trimmed, ":"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0
	}
	return value
}

func growTypeValue(record dnfrepo.CharacterRecord) int {
	value := statInt(record, "grow_type")
	if value < 0 {
		return 0
	}
	return int(value)
}

func statInt(record dnfrepo.CharacterRecord, key string) int64 {
	if record.Stats == nil {
		return 0
	}
	return record.Stats[key]
}

func statString(record dnfrepo.CharacterRecord, key string) string {
	if record.Stats == nil {
		return ""
	}
	value, ok := record.Stats[key]
	if !ok {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func (s *Server) jobDisplayName(job byte, growType byte) string {
	profiles, _, err := s.pvf.professionResources(s)
	if err != nil || profiles == nil {
		return strconv.Itoa(int(job))
	}
	if name, ok := profiles.DisplayName(job, growType); ok {
		return name
	}
	return strconv.Itoa(int(job))
}

func nullTime(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func quoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}
