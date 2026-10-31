// Package adminapi defines the private game-runtime management contract.
// It contains no web panel, database connection, scheduler or game transport.
package adminapi

import (
	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/qqbinding"
	"longheng.io/server/internal/modules/dnf/repository"
)

const Version = 1
const Path = "/api/game/admin"

type Request struct {
	AccountID       string                      `json:"accountId,omitempty"`
	PasswordReset   *PasswordReset              `json:"passwordReset,omitempty"`
	QQAck           *moderation.QQAck           `json:"qqAck,omitempty"`
	QQ              string                      `json:"qq,omitempty"`
	QQSettings      *qqbinding.Settings         `json:"qqSettings,omitempty"`
	QQComplete      *qqbinding.Complete         `json:"qqComplete,omitempty"`
	QQQuery         *qqbinding.Query            `json:"qqQuery,omitempty"`
	ModerationQuery *moderation.Query           `json:"moderationQuery,omitempty"`
	BanChange       *moderation.BanChange       `json:"banChange,omitempty"`
	Version         int                         `json:"version"`
	Action          string                      `json:"action"`
	CharacterID     uint16                      `json:"characterId,omitempty"`
	Revision        uint64                      `json:"revision,omitempty"`
	Changes         []onlineevent.ControlChange `json:"changes,omitempty"`
	ListType        byte                        `json:"listType,omitempty"`
	Slot            int16                       `json:"slot,omitempty"`
	Stack           *repository.ItemStack       `json:"stack,omitempty"`
}

type Response struct {
	PasswordReset      *PasswordResetResult         `json:"passwordReset,omitempty"`
	ErrorCode          string                       `json:"errorCode,omitempty"`
	BanTargets         *moderation.BanTargets       `json:"banTargets,omitempty"`
	QQActions          []moderation.QQAction        `json:"qqActions,omitempty"`
	QQBlocked          bool                         `json:"qqBlocked,omitempty"`
	QQSettings         *qqbinding.Settings          `json:"qqSettings,omitempty"`
	QQBinding          *qqbinding.Binding           `json:"qqBinding,omitempty"`
	QQBindings         *qqbinding.Page              `json:"qqBindings,omitempty"`
	Clears             *moderation.ClearPage        `json:"clears,omitempty"`
	Bans               *moderation.BanPage          `json:"bans,omitempty"`
	Ban                *moderation.Ban              `json:"ban,omitempty"`
	ModerationAccounts *moderation.AccountPage      `json:"moderationAccounts,omitempty"`
	Version            int                          `json:"version"`
	OK                 bool                         `json:"ok"`
	Error              string                       `json:"error,omitempty"`
	Online             bool                         `json:"online"`
	Activities         *onlineevent.ControlSnapshot `json:"activities,omitempty"`
	PremiumTypes       []int64                      `json:"premiumTypes,omitempty"`
	CrystalCubeItemIDs [6]int64                     `json:"crystalCubeItemIds,omitempty"`
	StackRow           []byte                       `json:"stackRow,omitempty"`
}
