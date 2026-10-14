// Package guild owns guild membership and lifecycle rules. Wire projection is
// handled by dnfbridge; persistent mutations require the repository Guild UoW.
package guild

import (
	"context"
	"encoding/json"
	"errors"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	RegistryScope       = dnfrepo.GuildRegistryScope
	registryStateKey    = "state"
	DefaultCreateGold   = int64(300000)
	DefaultMemberLimit  = 1000
	DefaultMasterGrade  = byte(1)
	DefaultMemberGrade  = byte(4)
	GradePermissionMask = uint32(1<<18 - 1)
)

var (
	ErrRepositoryUnavailable = errors.New("guild repository is unavailable")
	ErrCharacterNotFound     = errors.New("guild character was not found")
	ErrInvalidName           = errors.New("guild name is invalid")
	ErrInvalidPromotion      = errors.New("guild promotion is invalid")
	ErrNameExists            = errors.New("guild name already exists")
	ErrAlreadyMember         = errors.New("character is already a guild member")
	ErrGuildNotFound         = errors.New("guild was not found")
	ErrNotMember             = errors.New("character is not a guild member")
	ErrPermissionDenied      = errors.New("guild permission denied")
	ErrApplicationExists     = errors.New("guild application already exists")
	ErrApplicationNotFound   = errors.New("guild application was not found")
	ErrMemberLimit           = errors.New("guild member limit reached")
	ErrInsufficientGold      = errors.New("insufficient gold for guild creation")
	ErrInvalidRegistry       = errors.New("invalid guild registry")
)

type Registry struct {
	Version   int                        `json:"version"`
	NextID    uint32                     `json:"next_id"`
	Guilds    map[uint32]*Guild          `json:"guilds,omitempty"`
	Creations map[string]CreationReceipt `json:"creations,omitempty"`
}

type Guild struct {
	ID                  uint32                 `json:"id"`
	Name                string                 `json:"name"`
	MasterCharacterID   string                 `json:"master_character_id"`
	MasterName          string                 `json:"master_name"`
	Level               uint16                 `json:"level"`
	Experience          uint32                 `json:"experience"`
	Mileage             uint32                 `json:"mileage"`
	Notice              string                 `json:"notice,omitempty"`
	Promotion           string                 `json:"promotion,omitempty"`
	Mark                byte                   `json:"mark,omitempty"`
	AuraMark            uint32                 `json:"aura_mark,omitempty"`
	AuraFrame           uint32                 `json:"aura_frame,omitempty"`
	RecommendedRegion   uint32                 `json:"recommended_region,omitempty"`
	RecommendedChannel  uint32                 `json:"recommended_channel,omitempty"`
	AllianceRecruitment bool                   `json:"alliance_recruitment,omitempty"`
	Tags                []byte                 `json:"tags,omitempty"`
	JoinMode            byte                   `json:"join_mode"`
	MemberLimit         int                    `json:"member_limit"`
	GradeNames          map[byte]string        `json:"grade_names,omitempty"`
	GradePermissions    map[byte]uint32        `json:"grade_permissions,omitempty"`
	Members             map[string]Member      `json:"members,omitempty"`
	Applications        map[string]Application `json:"applications,omitempty"`
	Invitations         map[string]Invitation  `json:"invitations,omitempty"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

type Member struct {
	ServerGroup byte   `json:"server_group"`
	CharacterID string `json:"character_id"`
	AccountID   string `json:"account_id"`
	Name        string `json:"name"`
	Job         string `json:"job,omitempty"`
	// Packed job advancement byte used by the native roster.
	GrowType string `json:"grow_type,omitempty"`
	Level    int    `json:"level"`
	Grade    byte   `json:"grade"`
	Memo     string `json:"memo,omitempty"`
	Mileage  uint32 `json:"mileage"`
	// ContributionPoints is the 公会贡献度 total the guild keeps for this
	// member (红包 / 贡献币发放 read it). It is separate from Mileage, which is
	// the guild's own 里程 pool.
	ContributionPoints uint32    `json:"contribution_points,omitempty"`
	JoinedAt           time.Time `json:"joined_at"`
	AttendedAt         time.Time `json:"attended_at,omitempty"`
	// ChatAt is the last 公会聊天 contribution grant; the rule is once per
	// local day (help text 76515 进行公会聊天， 可以获得2点公会贡献。).
	ChatAt time.Time `json:"chat_at,omitempty"`
	// LastOnlineAt is refreshed whenever the member is seen with a live game
	// session. The member row carries the elapsed seconds since then, which is
	// how the client tells an offline member from an online one -- AttendedAt
	// cannot serve here because it only advances once per local day.
	LastOnlineAt time.Time `json:"last_online_at,omitempty"`
}

type Application struct {
	ServerGroup byte      `json:"server_group"`
	CharacterID string    `json:"character_id"`
	AccountID   string    `json:"account_id"`
	Name        string    `json:"name"`
	Job         string    `json:"job,omitempty"`
	GrowType    string    `json:"grow_type,omitempty"`
	Level       int       `json:"level"`
	Message     string    `json:"message,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type Invitation struct {
	CharacterID string    `json:"character_id"`
	InviterID   string    `json:"inviter_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func NewRegistry() Registry {
	return Registry{Version: 1, NextID: 1, Guilds: make(map[uint32]*Guild)}
}

func Load(ctx context.Context, repo dnfrepo.SettingsRepository) (Registry, error) {
	if repo == nil {
		return Registry{}, ErrRepositoryUnavailable
	}
	record, found, err := repo.Load(ctx, RegistryScope)
	if err != nil {
		return Registry{}, err
	}
	if !found || strings.TrimSpace(record.Values[registryStateKey]) == "" {
		return NewRegistry(), nil
	}
	var registry Registry
	if err := json.Unmarshal([]byte(record.Values[registryStateKey]), &registry); err != nil {
		return Registry{}, err
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, err
	}
	normalizeRegistry(&registry)
	return registry, nil
}

func Save(ctx context.Context, repo dnfrepo.SettingsRepository, registry Registry, now time.Time) error {
	if repo == nil {
		return ErrRepositoryUnavailable
	}
	if err := validateRegistry(registry); err != nil {
		return err
	}
	normalizeRegistry(&registry)
	encoded, err := json.Marshal(registry)
	if err != nil {
		return err
	}
	return repo.Save(ctx, dnfrepo.SettingsRecord{
		Scope:     RegistryScope,
		Values:    map[string]string{registryStateKey: string(encoded)},
		UpdatedAt: now,
	})
}

func normalizeRegistry(registry *Registry) {
	if registry.Version == 0 {
		registry.Version = 1
	}
	if registry.NextID == 0 {
		registry.NextID = 1
	}
	if registry.Guilds == nil {
		registry.Guilds = make(map[uint32]*Guild)
	}
	for id, guild := range registry.Guilds {
		if guild == nil {
			delete(registry.Guilds, id)
			continue
		}
		guild.ID = id
		if guild.Level == 0 {
			guild.Level = 1
		}
		if guild.MemberLimit <= 0 {
			guild.MemberLimit = DefaultMemberLimit
		}
		if guild.Members == nil {
			guild.Members = make(map[string]Member)
		}
		if guild.Applications == nil {
			guild.Applications = make(map[string]Application)
		}
		if guild.Invitations == nil {
			guild.Invitations = make(map[string]Invitation)
		}
		if guild.GradeNames == nil {
			guild.GradeNames = defaultGradeNames()
		}
		if id >= registry.NextID {
			if id == ^uint32(0) {
				registry.NextID = id
			} else {
				registry.NextID = id + 1
			}
		}
	}
}

func memberGrowType(character dnfrepo.CharacterRecord) string {
	if character.Stats == nil {
		return ""
	}
	value, found := character.Stats["grow_type"]
	if !found {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func defaultGradeNames() map[byte]string {
	return map[byte]string{1: "会长", 2: "副会长", 3: "优秀会员", 4: "普通会员"}
}

// Social identity is stored on the character, independently of the directory
// advertisement. Changing the directory must not migrate character identity.
func CharacterServerGroup(character dnfrepo.CharacterRecord) (byte, error) {
	group := character.Stats["server_group_id"]
	if group < 0 || group >= 250 {
		return 0, ErrCharacterNotFound
	}
	return byte(group), nil
}

func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	count := utf8.RuneCountInString(name)
	if count < 2 || count > 12 || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ErrInvalidName
	}
	return nil
}

func ValidatePromotion(promotion string) error {
	promotion = strings.TrimSpace(promotion)
	if !utf8.ValidString(promotion) || utf8.RuneCountInString(promotion) > 80 || strings.ContainsRune(promotion, '\x00') {
		return ErrInvalidPromotion
	}
	return nil
}

func (r *Registry) NameAvailable(name string) bool {
	name = strings.TrimSpace(name)
	if ValidateName(name) != nil {
		return false
	}
	for _, guild := range r.Guilds {
		if strings.EqualFold(strings.TrimSpace(guild.Name), name) {
			return false
		}
	}
	return true
}

func (r *Registry) GuildForCharacter(characterID string) (*Guild, Member, bool) {
	characterID = strings.TrimSpace(characterID)
	for _, guild := range r.Guilds {
		if member, ok := guild.Members[characterID]; ok {
			return guild, member, true
		}
	}
	return nil, Member{}, false
}

func (r *Registry) removeApplicationsForCharacter(characterID string, now time.Time) {
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return
	}
	for _, guild := range r.Guilds {
		if guild == nil || guild.Applications == nil {
			continue
		}
		if _, found := guild.Applications[characterID]; !found {
			continue
		}
		delete(guild.Applications, characterID)
		guild.UpdatedAt = now
	}
}

func (r *Registry) GuildByID(id uint32) (*Guild, bool) {
	guild, ok := r.Guilds[id]
	return guild, ok && guild != nil
}

func (r *Registry) GuildByName(name string) (*Guild, bool) {
	name = strings.TrimSpace(name)
	for _, value := range r.Guilds {
		if value != nil && strings.EqualFold(strings.TrimSpace(value.Name), name) {
			return value, true
		}
	}
	return nil, false
}

func (r *Registry) Create(character dnfrepo.CharacterRecord, name string, now time.Time) (*Guild, error) {
	group, err := CharacterServerGroup(character)
	if err != nil {
		return nil, err
	}
	if character.CharacterID == "" || character.AccountID == "" || character.Name == "" {
		return nil, ErrCharacterNotFound
	}
	name = strings.TrimSpace(name)
	if ValidateName(name) != nil {
		return nil, ErrInvalidName
	}
	if !r.NameAvailable(name) {
		return nil, ErrNameExists
	}
	if _, _, found := r.GuildForCharacter(character.CharacterID); found {
		return nil, ErrAlreadyMember
	}
	id := r.NextID
	if id == 0 {
		id = 1
	}
	if id == ^uint32(0) || r.Guilds[id] != nil {
		return nil, ErrInvalidRegistry
	}
	r.NextID = id + 1
	member := Member{
		ServerGroup: group,
		CharacterID: character.CharacterID,
		AccountID:   character.AccountID,
		Name:        character.Name,
		Job:         character.Job,
		GrowType:    memberGrowType(character),
		Level:       character.Level,
		Grade:       DefaultMasterGrade,
		JoinedAt:    now,
		AttendedAt:  now,
	}
	guild := &Guild{
		ID:                id,
		Name:              name,
		MasterCharacterID: character.CharacterID,
		MasterName:        character.Name,
		Level:             1,
		JoinMode:          1,

		MemberLimit:  DefaultMemberLimit,
		GradeNames:   defaultGradeNames(),
		Members:      map[string]Member{character.CharacterID: member},
		Applications: make(map[string]Application),
		Invitations:  make(map[string]Invitation),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	r.removeApplicationsForCharacter(character.CharacterID, now)
	r.Guilds[id] = guild
	return guild, nil
}

func (r *Registry) Search(query string, page, pageSize int) []*Guild {
	query = strings.TrimSpace(query)
	if page < 0 {
		page = 0
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	values := make([]*Guild, 0, len(r.Guilds))
	for _, guild := range r.Guilds {
		if query == "" || strings.Contains(strings.ToLower(guild.Name), strings.ToLower(query)) ||
			strings.Contains(strings.ToLower(guild.MasterName), strings.ToLower(query)) {
			values = append(values, guild)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Level != values[j].Level {
			return values[i].Level > values[j].Level
		}
		return values[i].ID < values[j].ID
	})
	if len(values) == 0 || page > (len(values)-1)/pageSize {
		return nil
	}
	start := page * pageSize
	if start >= len(values) {
		return nil
	}
	end := start + min(pageSize, len(values)-start)
	return values[start:end]
}

func (r *Registry) Apply(guildID uint32, character dnfrepo.CharacterRecord, message string, now time.Time) error {
	group, err := CharacterServerGroup(character)
	if err != nil {
		return err
	}
	if character.CharacterID == "" || character.AccountID == "" || character.Name == "" {
		return ErrCharacterNotFound
	}
	if err := ValidatePromotion(message); err != nil {
		return err
	}
	if _, _, found := r.GuildForCharacter(character.CharacterID); found {
		return ErrAlreadyMember
	}
	guild, found := r.GuildByID(guildID)
	if !found {
		return ErrGuildNotFound
	}
	if _, exists := guild.Applications[character.CharacterID]; exists {
		return ErrApplicationExists
	}
	guild.Applications[character.CharacterID] = Application{
		ServerGroup: group,
		CharacterID: character.CharacterID,
		AccountID:   character.AccountID,
		Name:        character.Name,
		Job:         character.Job,
		GrowType:    memberGrowType(character),
		Level:       character.Level,
		Message:     strings.TrimSpace(message),
		CreatedAt:   now,
	}
	guild.UpdatedAt = now
	return nil
}

func (r *Registry) JoinDirect(guildID uint32, character dnfrepo.CharacterRecord, now time.Time) (Member, error) {
	group, err := CharacterServerGroup(character)
	if err != nil {
		return Member{}, err
	}
	if character.CharacterID == "" || character.AccountID == "" || character.Name == "" {
		return Member{}, ErrCharacterNotFound
	}
	if _, _, found := r.GuildForCharacter(character.CharacterID); found {
		return Member{}, ErrAlreadyMember
	}
	guild, found := r.GuildByID(guildID)
	if !found {
		return Member{}, ErrGuildNotFound
	}
	if len(guild.Members) >= guild.MemberLimit {
		return Member{}, ErrMemberLimit
	}
	joined := Member{
		ServerGroup: group,
		CharacterID: character.CharacterID,
		AccountID:   character.AccountID,
		Name:        character.Name,
		Job:         character.Job,
		GrowType:    memberGrowType(character),
		Level:       character.Level,
		Grade:       DefaultMemberGrade,
		JoinedAt:    now,
		AttendedAt:  now,
	}
	guild.Members[character.CharacterID] = joined
	r.removeApplicationsForCharacter(character.CharacterID, now)
	guild.UpdatedAt = now
	return joined, nil
}

func (r *Registry) CancelApplication(guildID uint32, characterID string, now time.Time) error {
	guild, found := r.GuildByID(guildID)
	if !found {
		return ErrGuildNotFound
	}
	if _, exists := guild.Applications[characterID]; !exists {
		return ErrApplicationNotFound
	}
	delete(guild.Applications, characterID)
	guild.UpdatedAt = now
	return nil
}

func (r *Registry) DecideApplication(guildID uint32, actorID, characterID string, approve bool, now time.Time) (Member, error) {
	guild, found := r.GuildByID(guildID)
	if !found {
		return Member{}, ErrGuildNotFound
	}
	actor, member := guild.Members[actorID]
	if !member || actor.Grade > 2 {
		return Member{}, ErrPermissionDenied
	}
	application, found := guild.Applications[characterID]
	if !found {
		return Member{}, ErrApplicationNotFound
	}
	delete(guild.Applications, characterID)
	if !approve {
		guild.UpdatedAt = now
		return Member{}, nil
	}
	if len(guild.Members) >= guild.MemberLimit {
		return Member{}, ErrMemberLimit
	}
	if _, _, exists := r.GuildForCharacter(characterID); exists {
		return Member{}, ErrAlreadyMember
	}
	joined := Member{
		ServerGroup: application.ServerGroup,
		CharacterID: application.CharacterID,
		AccountID:   application.AccountID,
		Name:        application.Name,
		Job:         application.Job,
		// Carry the advancement byte over from the application; without it a
		// member who joined this way renders as their base class until the
		// next member-list read reconciles it from the character repository.
		GrowType:   application.GrowType,
		Level:      application.Level,
		Grade:      DefaultMemberGrade,
		JoinedAt:   now,
		AttendedAt: now,
	}
	guild.Members[characterID] = joined
	r.removeApplicationsForCharacter(characterID, now)
	guild.UpdatedAt = now
	return joined, nil
}

func DefaultGradePermissions(grade byte) uint32 {
	switch grade {
	case DefaultMasterGrade:
		return GradePermissionMask
	case 2:
		return 0x4400 | 1<<4
	default:
		return 0
	}
}

func GradePermissionsFor(guild *Guild, grade byte) uint32 {
	if guild != nil {
		if mask, found := guild.GradePermissions[grade]; found {
			return mask & GradePermissionMask
		}
	}
	return DefaultGradePermissions(grade)
}

func SortedMembers(guild *Guild) []Member {
	if guild == nil {
		return nil
	}
	values := make([]Member, 0, len(guild.Members))
	for _, member := range guild.Members {
		values = append(values, member)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Grade != values[j].Grade {
			return values[i].Grade < values[j].Grade
		}
		return values[i].CharacterID < values[j].CharacterID
	})
	return values
}

func SortedApplications(guild *Guild) []Application {
	if guild == nil {
		return nil
	}
	values := make([]Application, 0, len(guild.Applications))
	for _, application := range guild.Applications {
		values = append(values, application)
	}
	sort.Slice(values, func(i, j int) bool {
		if !values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].CreatedAt.Before(values[j].CreatedAt)
		}
		return values[i].CharacterID < values[j].CharacterID
	})
	return values
}

// Reject corrupt/unknown state rather than silently discarding members or
// changing IDs. This registry is new and has no legacy version to migrate.
func validateRegistry(r Registry) error {
	if r.Version != 1 {
		return ErrInvalidRegistry
	}
	names := map[string]bool{}
	members := map[string]bool{}
	for id, g := range r.Guilds {
		if id == 0 || g == nil || g.ID != id || ValidateName(g.Name) != nil {
			return ErrInvalidRegistry
		}
		name := strings.ToLower(strings.TrimSpace(g.Name))
		if names[name] {
			return ErrInvalidRegistry
		}
		names[name] = true
		master, ok := g.Members[g.MasterCharacterID]
		if !ok || master.Grade != DefaultMasterGrade {
			return ErrInvalidRegistry
		}
		for key, m := range g.Members {
			if key == "" || m.CharacterID != key || m.AccountID == "" || members[key] || m.ServerGroup >= 250 {
				return ErrInvalidRegistry
			}
			if m.Grade < 1 || m.Grade > 4 || (m.Grade == DefaultMasterGrade && key != g.MasterCharacterID) {
				return ErrInvalidRegistry
			}
			members[key] = true
		}
	}
	return nil
}
