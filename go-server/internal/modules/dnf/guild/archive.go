package guild

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const (
	guildTeleportArchiveAge       = 24 * time.Hour
	guildHongbaoArchiveGrace      = 24 * time.Hour
	guildInlineArchiveBatch       = 16
	guildStartupArchiveBatch      = 128
	guildStartupArchiveMaxBatches = 64

	guildArchiveReceiptEntryKey     = "receipt"
	guildArchiveContractDayEntryKey = "contract_day"
	guildArchiveHongbaoEntryKey     = "hongbao"

	ArchivedReceiptKindContractClaim = "contract_claim"
	ArchivedReceiptKindTeleport      = "teleport"
)

// ArchivedReceipt 保存已从主注册表迁出的历史公会回执，保留永久幂等回放与跨表 requestID 冲突校验能力。
type ArchivedReceipt struct {
	Version       int                   `json:"version"`
	Kind          string                `json:"kind"`
	RequestID     string                `json:"request_id"`
	ContractClaim *ContractClaimReceipt `json:"contract_claim,omitempty"`
	Teleport      *TeleportReceipt      `json:"teleport,omitempty"`
}

type archivedContractDayClaim struct {
	Version   int          `json:"version"`
	Owner     r.GuildOwner `json:"owner"`
	DayEnd    time.Time    `json:"day_end"`
	RequestID string       `json:"request_id"`
}

type archivedHongbaoRecord struct {
	Version int           `json:"version"`
	Record  HongbaoRecord `json:"record"`
}

// ArchiveHistoryStats 记录启动或维护期公会历史归档的裁剪统计。
type ArchiveHistoryStats struct {
	ContractClaims int
	Teleports      int
	Hongbao        int
	Departures     int
	BeforeBytes    int
	AfterBytes     int
}

func (s ArchiveHistoryStats) TotalArchived() int {
	return s.ContractClaims + s.Teleports + s.Hongbao + s.Departures
}

func validateArchivedReceipt(item ArchivedReceipt, expectedID string) error {
	if item.Version != 1 || item.RequestID != expectedID || !validPurchaseID(expectedID) {
		return ErrInvalidRegistry
	}
	switch item.Kind {
	case ArchivedReceiptKindContractClaim:
		if item.ContractClaim == nil || item.Teleport != nil {
			return ErrInvalidRegistry
		}
		return validateContractClaims(Registry{
			ContractClaims: map[string]ContractClaimReceipt{expectedID: *item.ContractClaim},
		})
	case ArchivedReceiptKindTeleport:
		if item.Teleport == nil || item.ContractClaim != nil {
			return ErrInvalidRegistry
		}
		return validateTeleports(Registry{
			Teleports: map[string]TeleportReceipt{expectedID: *item.Teleport},
		})
	default:
		return ErrInvalidRegistry
	}
}

func contractClaimReceiptEqual(a, b ContractClaimReceipt) bool {
	if a.Owner != b.Owner || a.GuildID != b.GuildID || a.ItemID != b.ItemID || a.Count != b.Count ||
		!a.At.Equal(b.At) || !a.DayEnd.Equal(b.DayEnd) || len(a.Slots) != len(b.Slots) {
		return false
	}
	for i := range a.Slots {
		if a.Slots[i] != b.Slots[i] {
			return false
		}
	}
	return true
}

func teleportReceiptEqual(a, b TeleportReceipt) bool {
	if a.GuildID != b.GuildID || a.Initiator != b.Initiator || a.Destination != b.Destination ||
		!a.At.Equal(b.At) || !a.JoinedAt.Equal(b.JoinedAt) || len(a.Participants) != len(b.Participants) {
		return false
	}
	for i := range a.Participants {
		if a.Participants[i] != b.Participants[i] {
			return false
		}
	}
	return true
}

func archivedReceiptEqual(a, b ArchivedReceipt) bool {
	if a.Version != b.Version || a.Kind != b.Kind || a.RequestID != b.RequestID {
		return false
	}
	switch a.Kind {
	case ArchivedReceiptKindContractClaim:
		return a.ContractClaim != nil && b.ContractClaim != nil && contractClaimReceiptEqual(*a.ContractClaim, *b.ContractClaim)
	case ArchivedReceiptKindTeleport:
		return a.Teleport != nil && b.Teleport != nil && teleportReceiptEqual(*a.Teleport, *b.Teleport)
	default:
		return false
	}
}

func hongbaoRecordEqual(a, b HongbaoRecord) bool {
	if a.ID != b.ID || a.GuildID != b.GuildID || a.Sender != b.Sender || a.SenderName != b.SenderName ||
		!a.JoinedAt.Equal(b.JoinedAt) || a.Product != b.Product ||
		!a.CreatedAt.Equal(b.CreatedAt) || !a.ExpiresAt.Equal(b.ExpiresAt) ||
		!a.SettledAt.Equal(b.SettledAt) || len(a.Shares) != len(b.Shares) || len(a.Claims) != len(b.Claims) {
		return false
	}
	for i := range a.Shares {
		if a.Shares[i] != b.Shares[i] {
			return false
		}
	}
	for i := range a.Claims {
		ca, cb := a.Claims[i], b.Claims[i]
		if ca.Owner != cb.Owner || ca.Name != cb.Name || ca.Level != cb.Level || ca.Amount != cb.Amount || !ca.At.Equal(cb.At) {
			return false
		}
	}
	if (a.Settlement == nil) != (b.Settlement == nil) {
		return false
	}
	if a.Settlement != nil {
		sa, sb := a.Settlement, b.Settlement
		if sa.Sender != sb.Sender || sa.RecordID != sb.RecordID || sa.Points != sb.Points ||
			sa.Coins != sb.Coins || sa.RefundTokenCera != sb.RefundTokenCera ||
			sa.Replayed != sb.Replayed || len(sa.MailIDs) != len(sb.MailIDs) {
			return false
		}
		for i := range sa.MailIDs {
			if sa.MailIDs[i] != sb.MailIDs[i] {
				return false
			}
		}
	}
	return true
}

// LoadArchivedReceipt 按 requestID 查验已归档的历史公会回执（契约领取 / 传送）。
func LoadArchivedReceipt(ctx context.Context, settings r.SettingsRepository, requestID string) (ArchivedReceipt, bool, error) {
	if settings == nil {
		return ArchivedReceipt{}, false, ErrRepositoryUnavailable
	}
	scope := r.GuildReceiptArchiveScope(requestID)
	if scope == "" {
		return ArchivedReceipt{}, false, nil
	}
	record, found, err := settings.Load(ctx, scope)
	if err != nil || !found {
		return ArchivedReceipt{}, false, err
	}
	var item ArchivedReceipt
	if err := json.Unmarshal([]byte(record.Values[guildArchiveReceiptEntryKey]), &item); err != nil {
		return ArchivedReceipt{}, false, ErrInvalidRegistry
	}
	if err := validateArchivedReceipt(item, requestID); err != nil {
		return ArchivedReceipt{}, false, err
	}
	return item, true, nil
}

func saveArchivedReceipt(ctx context.Context, settings r.SettingsRepository, item ArchivedReceipt, now time.Time) error {
	if settings == nil {
		return ErrRepositoryUnavailable
	}
	if err := validateArchivedReceipt(item, item.RequestID); err != nil {
		return err
	}
	scope := r.GuildReceiptArchiveScope(item.RequestID)
	if scope == "" {
		return ErrInvalidRegistry
	}
	existing, found, err := LoadArchivedReceipt(ctx, settings, item.RequestID)
	if err != nil {
		return err
	}
	if found {
		if !archivedReceiptEqual(existing, item) {
			return ErrInvalidRegistry
		}
		return nil
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return settings.Save(ctx, r.SettingsRecord{
		Scope:     scope,
		Values:    map[string]string{guildArchiveReceiptEntryKey: string(encoded)},
		UpdatedAt: now,
	})
}

func loadArchivedContractDayClaim(ctx context.Context, settings r.SettingsRepository, owner r.GuildOwner, dayEnd time.Time) (archivedContractDayClaim, bool, error) {
	if settings == nil {
		return archivedContractDayClaim{}, false, ErrRepositoryUnavailable
	}
	scope := r.GuildContractDayArchiveScope(owner, dayEnd)
	if scope == "" {
		return archivedContractDayClaim{}, false, nil
	}
	record, found, err := settings.Load(ctx, scope)
	if err != nil || !found {
		return archivedContractDayClaim{}, false, err
	}
	var marker archivedContractDayClaim
	if err := json.Unmarshal([]byte(record.Values[guildArchiveContractDayEntryKey]), &marker); err != nil {
		return archivedContractDayClaim{}, false, ErrInvalidRegistry
	}
	if marker.Version != 1 || marker.Owner != owner || !marker.DayEnd.Equal(dayEnd) || !validPurchaseID(marker.RequestID) {
		return archivedContractDayClaim{}, false, ErrInvalidRegistry
	}
	return marker, true, nil
}

// HasArchivedContractDayClaim 检查 owner 在指定服务日（dayEnd）是否已有已归档的契约领取记录，
// 防止旧服务日回执迁出主表后被携带旧时间的新 requestID 绕过每日限领。
func HasArchivedContractDayClaim(ctx context.Context, settings r.SettingsRepository, owner r.GuildOwner, dayEnd time.Time) (bool, error) {
	_, found, err := loadArchivedContractDayClaim(ctx, settings, owner, dayEnd)
	return found, err
}

func saveArchivedContractDayClaim(ctx context.Context, settings r.SettingsRepository, owner r.GuildOwner, dayEnd time.Time, requestID string, now time.Time) error {
	if settings == nil {
		return ErrRepositoryUnavailable
	}
	if owner.AccountID == "" || owner.CharacterID == "" || dayEnd.IsZero() || !validPurchaseID(requestID) {
		return ErrInvalidRegistry
	}
	scope := r.GuildContractDayArchiveScope(owner, dayEnd)
	if scope == "" {
		return ErrInvalidRegistry
	}
	existing, found, err := loadArchivedContractDayClaim(ctx, settings, owner, dayEnd)
	if err != nil {
		return err
	}
	if found {
		if existing.Owner != owner || !existing.DayEnd.Equal(dayEnd) || existing.RequestID != requestID {
			return ErrInvalidRegistry
		}
		return nil
	}
	marker := archivedContractDayClaim{
		Version:   1,
		Owner:     owner,
		DayEnd:    dayEnd.UTC(),
		RequestID: requestID,
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return settings.Save(ctx, r.SettingsRecord{
		Scope:     scope,
		Values:    map[string]string{guildArchiveContractDayEntryKey: string(encoded)},
		UpdatedAt: now,
	})
}

func loadArchivedHongbaoRecord(ctx context.Context, settings r.SettingsRepository, id uint32) (HongbaoRecord, bool, error) {
	if settings == nil {
		return HongbaoRecord{}, false, ErrRepositoryUnavailable
	}
	scope := r.GuildHongbaoArchiveScope(id)
	if scope == "" {
		return HongbaoRecord{}, false, nil
	}
	record, found, err := settings.Load(ctx, scope)
	if err != nil || !found {
		return HongbaoRecord{}, false, err
	}
	var envelope archivedHongbaoRecord
	if err := json.Unmarshal([]byte(record.Values[guildArchiveHongbaoEntryKey]), &envelope); err != nil {
		return HongbaoRecord{}, false, ErrHongbaoState
	}
	if envelope.Version != 1 || envelope.Record.ID != id || envelope.Record.Settlement == nil || envelope.Record.SettledAt.IsZero() {
		return HongbaoRecord{}, false, ErrHongbaoState
	}
	if err := validateHongbao(&HongbaoState{NextID: id + 1, Records: map[uint32]HongbaoRecord{id: envelope.Record}}); err != nil {
		return HongbaoRecord{}, false, err
	}
	return envelope.Record.copy(), true, nil
}

func saveArchivedHongbaoRecord(ctx context.Context, settings r.SettingsRepository, record HongbaoRecord, now time.Time) error {
	if settings == nil {
		return ErrRepositoryUnavailable
	}
	if record.ID == 0 || record.Settlement == nil || record.SettledAt.IsZero() {
		return ErrHongbaoState
	}
	if err := validateHongbao(&HongbaoState{NextID: record.ID + 1, Records: map[uint32]HongbaoRecord{record.ID: record}}); err != nil {
		return err
	}
	scope := r.GuildHongbaoArchiveScope(record.ID)
	if scope == "" {
		return ErrHongbaoState
	}
	existing, found, err := loadArchivedHongbaoRecord(ctx, settings, record.ID)
	if err != nil {
		return err
	}
	if found {
		if !hongbaoRecordEqual(existing, record) {
			return ErrHongbaoState
		}
		return nil
	}
	encoded, err := json.Marshal(archivedHongbaoRecord{Version: 1, Record: record.copy()})
	if err != nil {
		return err
	}
	return settings.Save(ctx, r.SettingsRecord{
		Scope:     scope,
		Values:    map[string]string{guildArchiveHongbaoEntryKey: string(encoded)},
		UpdatedAt: now,
	})
}

func expiredContractClaimIDs(reg *Registry, now time.Time, limit int) []string {
	if reg == nil || now.IsZero() || len(reg.ContractClaims) == 0 || limit <= 0 {
		return nil
	}
	var ids []string
	for id, claim := range reg.ContractClaims {
		if !claim.DayEnd.IsZero() && !now.Before(claim.DayEnd) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids
}

func expiredTeleportIDs(reg *Registry, now time.Time, limit int) []string {
	if reg == nil || now.IsZero() || len(reg.Teleports) == 0 || limit <= 0 {
		return nil
	}
	var ids []string
	for id, receipt := range reg.Teleports {
		if !receipt.At.IsZero() && !now.Before(receipt.At.Add(guildTeleportArchiveAge)) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids
}

func hongbaoArchivable(h HongbaoRecord, now time.Time) bool {
	if h.Settlement == nil || h.SettledAt.IsZero() || h.visible(now) {
		return false
	}
	// 在 24 小时展示期结束且结算满 24 小时宽限后再归档，避免刚满 24h 边界的即刻观测受影响。
	return !now.Before(h.closedAt().Add(24*time.Hour+guildHongbaoArchiveGrace)) &&
		!now.Before(h.SettledAt.Add(guildHongbaoArchiveGrace))
}

func archivableHongbaoIDs(reg *Registry, now time.Time, limit int) []uint32 {
	if reg == nil || reg.Hongbao == nil || now.IsZero() || len(reg.Hongbao.Records) == 0 || limit <= 0 {
		return nil
	}
	var ids []uint32
	for id, h := range reg.Hongbao.Records {
		if hongbaoArchivable(h, now) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids
}

func hasExpiredDepartures(reg *Registry, now time.Time) bool {
	if reg == nil || now.IsZero() {
		return false
	}
	for _, d := range reg.Departures {
		if !d.Until.IsZero() && !now.Before(d.Until) {
			return true
		}
	}
	return false
}

func pruneExpiredDepartures(reg *Registry, now time.Time) int {
	if reg == nil || now.IsZero() || len(reg.Departures) == 0 {
		return 0
	}
	removed := 0
	for id, d := range reg.Departures {
		if !d.Until.IsZero() && !now.Before(d.Until) {
			delete(reg.Departures, id)
			removed++
		}
	}
	if len(reg.Departures) == 0 {
		reg.Departures = nil
	}
	return removed
}

func hasArchivableGuildHistory(reg *Registry, now time.Time) bool {
	return len(expiredContractClaimIDs(reg, now, 1)) > 0 ||
		len(expiredTeleportIDs(reg, now, 1)) > 0 ||
		len(archivableHongbaoIDs(reg, now, 1)) > 0 ||
		hasExpiredDepartures(reg, now)
}

// ArchiveExpiredContractClaims 将已过服务日（DayEnd <= now）的契约领取回执与日限领标记分批归档到独立 scope。
// 必须在已持有 GuildRegistryScope 事务锁的同一事务内调用；当日有效回执保留在主注册表中。
func ArchiveExpiredContractClaims(ctx context.Context, settings r.SettingsRepository, reg *Registry, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = guildInlineArchiveBatch
	}
	ids := expiredContractClaimIDs(reg, now, limit)
	for _, id := range ids {
		claim := reg.ContractClaims[id]
		item := ArchivedReceipt{
			Version:       1,
			Kind:          ArchivedReceiptKindContractClaim,
			RequestID:     id,
			ContractClaim: &claim,
		}
		if err := saveArchivedReceipt(ctx, settings, item, now); err != nil {
			return 0, err
		}
		if err := saveArchivedContractDayClaim(ctx, settings, claim.Owner, claim.DayEnd, id, now); err != nil {
			return 0, err
		}
		delete(reg.ContractClaims, id)
	}
	if len(reg.ContractClaims) == 0 {
		reg.ContractClaims = nil
	}
	return len(ids), nil
}

func archiveExpiredTeleports(ctx context.Context, settings r.SettingsRepository, reg *Registry, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = guildInlineArchiveBatch
	}
	ids := expiredTeleportIDs(reg, now, limit)
	for _, id := range ids {
		receipt := reg.Teleports[id]
		item := ArchivedReceipt{
			Version:   1,
			Kind:      ArchivedReceiptKindTeleport,
			RequestID: id,
			Teleport:  &receipt,
		}
		if err := saveArchivedReceipt(ctx, settings, item, now); err != nil {
			return 0, err
		}
		delete(reg.Teleports, id)
	}
	if len(reg.Teleports) == 0 {
		reg.Teleports = nil
	}
	return len(ids), nil
}

func archiveExpiredHongbao(ctx context.Context, settings r.SettingsRepository, reg *Registry, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = guildInlineArchiveBatch
	}
	ids := archivableHongbaoIDs(reg, now, limit)
	for _, id := range ids {
		h := reg.Hongbao.Records[id]
		if err := saveArchivedHongbaoRecord(ctx, settings, h, now); err != nil {
			return 0, err
		}
		delete(reg.Hongbao.Records, id)
	}
	return len(ids), nil
}

// ArchiveExpiredHistory 在启动或维护期通过 GuildArchiveUnitOfWork 分批原子归档已过期的公会历史记录：
//   - 已过服务日的 ContractClaims（保留当日有效回执，历史回执迁入 guild-archive:v1:receipt:<id> 并同步写入 guild-archive:v1:contract-day:<hash>）
//   - 超过 24 小时的 Teleports（历史回执迁入 guild-archive:v1:receipt:<id>）
//   - 已结算且超过 24 小时展示期与 24 小时宽限期的 Hongbao 记录（保留 NextID，迁入 guild-archive:v1:hongbao:<id>）
//   - 已过冷却截止时间的 Departures
//
// 每个批次均在主库写事务（先锁 GuildRegistryScope，读路由指向写库并启用 FOR UPDATE / sharedMu）内原子完成
// 归档冲突核验、归档写入与主注册表裁剪，消除读延迟覆盖与并发回滚丢数风险。
func ArchiveExpiredHistory(ctx context.Context, uow r.GuildArchiveUnitOfWork, now time.Time) (ArchiveHistoryStats, error) {
	var stats ArchiveHistoryStats
	if uow == nil {
		return stats, ErrRepositoryUnavailable
	}
	if now.IsZero() {
		return stats, ErrInvalidRegistry
	}
	// 无可归档项时先通过只读事务快速返回，避免无谓获取全局写锁。
	if reader, ok := uow.(r.GuildReadUnitOfWork); ok {
		needsWork := false
		if err := reader.WithinGuildRead(ctx, func(view r.GuildReadView) error {
			record, found, err := view.Settings.Load(ctx, RegistryScope)
			if err != nil || !found || strings.TrimSpace(record.Values[registryStateKey]) == "" {
				return err
			}
			reg, err := Load(ctx, view.Settings)
			if err != nil {
				return err
			}
			needsWork = hasArchivableGuildHistory(&reg, now)
			return nil
		}); err != nil {
			return stats, err
		}
		if !needsWork {
			return stats, nil
		}
	}
	for batch := 0; batch < guildStartupArchiveMaxBatches; batch++ {
		var batchStats ArchiveHistoryStats
		var progressed bool
		err := uow.WithinGuildArchive(ctx, func(txSettings r.SettingsRepository) error {
			before, found, err := txSettings.Load(ctx, RegistryScope)
			if err != nil || !found || strings.TrimSpace(before.Values[registryStateKey]) == "" {
				return err
			}
			reg, err := Load(ctx, txSettings)
			if err != nil {
				return err
			}
			remaining := guildStartupArchiveBatch
			contractIDs := expiredContractClaimIDs(&reg, now, remaining)
			remaining -= len(contractIDs)
			teleportIDs := expiredTeleportIDs(&reg, now, remaining)
			remaining -= len(teleportIDs)
			hongbaoIDs := archivableHongbaoIDs(&reg, now, remaining)
			departuresPruned := pruneExpiredDepartures(&reg, now)
			if len(contractIDs) == 0 && len(teleportIDs) == 0 && len(hongbaoIDs) == 0 && departuresPruned == 0 {
				return nil
			}
			for _, id := range contractIDs {
				claim := reg.ContractClaims[id]
				item := ArchivedReceipt{
					Version:       1,
					Kind:          ArchivedReceiptKindContractClaim,
					RequestID:     id,
					ContractClaim: &claim,
				}
				if err := saveArchivedReceipt(ctx, txSettings, item, now); err != nil {
					return err
				}
				if err := saveArchivedContractDayClaim(ctx, txSettings, claim.Owner, claim.DayEnd, id, now); err != nil {
					return err
				}
				delete(reg.ContractClaims, id)
			}
			if len(reg.ContractClaims) == 0 {
				reg.ContractClaims = nil
			}
			for _, id := range teleportIDs {
				receipt := reg.Teleports[id]
				item := ArchivedReceipt{
					Version:   1,
					Kind:      ArchivedReceiptKindTeleport,
					RequestID: id,
					Teleport:  &receipt,
				}
				if err := saveArchivedReceipt(ctx, txSettings, item, now); err != nil {
					return err
				}
				delete(reg.Teleports, id)
			}
			if len(reg.Teleports) == 0 {
				reg.Teleports = nil
			}
			for _, id := range hongbaoIDs {
				h := reg.Hongbao.Records[id]
				if err := saveArchivedHongbaoRecord(ctx, txSettings, h, now); err != nil {
					return err
				}
				delete(reg.Hongbao.Records, id)
			}
			if err := validateRegistry(reg); err != nil {
				return err
			}
			normalizeRegistry(&reg)
			encoded, err := json.Marshal(reg)
			if err != nil {
				return err
			}
			after := r.CloneSettings(before)
			if after.Values == nil {
				after.Values = make(map[string]string, 1)
			}
			after.Values[registryStateKey] = string(encoded)
			after.UpdatedAt = now
			if err := txSettings.Save(ctx, after); err != nil {
				return err
			}
			batchStats = ArchiveHistoryStats{
				ContractClaims: len(contractIDs),
				Teleports:      len(teleportIDs),
				Hongbao:        len(hongbaoIDs),
				Departures:     departuresPruned,
				BeforeBytes:    len(before.Values[registryStateKey]),
				AfterBytes:     len(encoded),
			}
			progressed = true
			return nil
		})
		if err != nil {
			return stats, err
		}
		if !progressed {
			return stats, nil
		}
		if stats.BeforeBytes == 0 {
			stats.BeforeBytes = batchStats.BeforeBytes
		}
		stats.AfterBytes = batchStats.AfterBytes
		stats.ContractClaims += batchStats.ContractClaims
		stats.Teleports += batchStats.Teleports
		stats.Hongbao += batchStats.Hongbao
		stats.Departures += batchStats.Departures
	}
	return stats, nil
}
