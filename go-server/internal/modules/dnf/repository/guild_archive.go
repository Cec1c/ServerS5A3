package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	guildReceiptArchivePrefix     = "guild-archive:v1:receipt:"
	guildContractDayArchivePrefix = "guild-archive:v1:contract-day:"
	guildHongbaoArchivePrefix     = "guild-archive:v1:hongbao:"
)

// GuildArchiveUnitOfWork 在同一主库写事务（先锁 GuildRegistryScope）内原子执行
// 历史归档写入与主注册表裁剪，消除读路由滞后、非原子覆盖与内存并发回滚竞态。
type GuildArchiveUnitOfWork interface {
	WithinGuildArchive(context.Context, func(SettingsRepository) error) error
}

// GuildArchiveSettings 仅允许在已持有 GuildRegistryScope 锁的事务内访问主注册表与受控归档 scope。
func GuildArchiveSettings(base SettingsRepository) SettingsRepository {
	return &guildSettings{base: base}
}

func validGuildArchiveHex64(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// GuildReceiptArchiveScope 返回归档公会回执（契约领取 / 传送）的独立 Settings scope。
func GuildReceiptArchiveScope(requestID string) string {
	if !validGuildArchiveHex64(requestID) {
		return ""
	}
	return guildReceiptArchivePrefix + requestID
}

// ValidGuildReceiptArchiveScope 校验 scope 是否为合法的公会回执归档键。
func ValidGuildReceiptArchiveScope(scope string) bool {
	if len(scope) != len(guildReceiptArchivePrefix)+64 || !strings.HasPrefix(scope, guildReceiptArchivePrefix) {
		return false
	}
	return validGuildArchiveHex64(strings.TrimPrefix(scope, guildReceiptArchivePrefix))
}

// GuildContractDayArchiveScope 返回按 (AccountID, CharacterID, DayEndUnix) 绑定的
// 历史契约日限领标记 scope，确保历史回执迁出主表后仍无法用旧服务日时间的新 requestID 重复领取。
func GuildContractDayArchiveScope(owner GuildOwner, dayEnd time.Time) string {
	if owner.AccountID == "" || owner.CharacterID == "" || dayEnd.IsZero() {
		return ""
	}
	key := fmt.Sprintf("%q:%q:%d", owner.AccountID, owner.CharacterID, dayEnd.UTC().Unix())
	return fmt.Sprintf("%s%x", guildContractDayArchivePrefix, sha256.Sum256([]byte(key)))
}

// ValidGuildContractDayArchiveScope 校验 scope 是否为合法的公会契约日限领归档键。
func ValidGuildContractDayArchiveScope(scope string) bool {
	if len(scope) != len(guildContractDayArchivePrefix)+64 || !strings.HasPrefix(scope, guildContractDayArchivePrefix) {
		return false
	}
	return validGuildArchiveHex64(strings.TrimPrefix(scope, guildContractDayArchivePrefix))
}

// GuildHongbaoArchiveScope 返回已结算且超过展示期与宽限期公会红包归档记录的独立 Settings scope。
func GuildHongbaoArchiveScope(id uint32) string {
	if id == 0 {
		return ""
	}
	return guildHongbaoArchivePrefix + strconv.FormatUint(uint64(id), 10)
}

// ValidGuildHongbaoArchiveScope 校验 scope 是否为合法的公会红包归档键（无前导零正 uint32）。
func ValidGuildHongbaoArchiveScope(scope string) bool {
	if !strings.HasPrefix(scope, guildHongbaoArchivePrefix) {
		return false
	}
	raw := strings.TrimPrefix(scope, guildHongbaoArchivePrefix)
	if raw == "" {
		return false
	}
	id, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || id == 0 {
		return false
	}
	return strconv.FormatUint(id, 10) == raw
}

// ValidGuildArchiveScope 校验 scope 是否属于受控的公会历史归档命名空间。
func ValidGuildArchiveScope(scope string) bool {
	return ValidGuildReceiptArchiveScope(scope) ||
		ValidGuildContractDayArchiveScope(scope) ||
		ValidGuildHongbaoArchiveScope(scope)
}
