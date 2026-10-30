package dnfbridge

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/qqbinding"
	"longheng.io/server/internal/modules/dnf/repository"
)

// Only runtime-owned facts and operations belong here. GM pages, credentials,
// campaigns and batch jobs live in the separately deployed GM process.
func (a *accountAPIServer) handleGameAdmin(w http.ResponseWriter, r *http.Request) {
	secret := a.service.options.playerAdminSecret
	if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req adminapi.Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || req.Version != adminapi.Version {
		http.Error(w, "invalid management request", http.StatusBadRequest)
		return
	}
	out := adminapi.Response{Version: adminapi.Version, OK: true}
	s := a.service
	switch req.Action {
	case "reset_account_password":
		var err error
		if req.PasswordReset == nil {
			err = securityError(400, "invalid_reset", "重置参数无效")
		} else {
			out.PasswordReset, err = a.resetAccountPassword(r.Context(), req.AccountID, *req.PasswordReset)
		}
		if err != nil {
			out.OK = false
			out.ErrorCode, out.Error = "reset_unavailable", "密码重置服务暂不可用"
			var failure *accountSecurityError
			if errors.As(err, &failure) {
				out.ErrorCode, out.Error = failure.code, failure.message
			}
		}
	case "qq_settings", "qq_settings_save", "qq_bindings", "qq_complete", "qq_jobs", "qq_ack", "qq_blocked", "ban_targets":
		var err error
		switch req.Action {
		case "ban_targets":
			out.BanTargets, err = a.banTargets(r.Context(), a.db, req.AccountID)
		case "qq_jobs":
			out.QQActions, err = a.claimQQActions(r.Context())
		case "qq_ack":
			if req.QQAck == nil {
				err = errors.New("missing acknowledgement")
			} else {
				err = a.ackQQAction(r.Context(), *req.QQAck)
			}
		case "qq_blocked":
			out.QQBlocked, err = a.qqBlacklisted(r.Context(), req.QQ)
		case "qq_settings":
			out.QQSettings, err = a.qqSettings(r.Context())
		case "qq_settings_save":
			if req.QQSettings == nil {
				err = errors.New("missing settings")
			} else {
				out.QQSettings, err = a.saveQQSettings(r.Context(), *req.QQSettings)
			}
		case "qq_bindings":
			q := qqbinding.Query{}
			if req.QQQuery != nil {
				q = *req.QQQuery
			}
			out.QQBindings, err = a.queryQQBindings(r.Context(), q)
		case "qq_complete":
			if req.QQComplete == nil {
				err = errors.New("missing binding")
			} else {
				out.QQBinding, err = a.completeQQBinding(r.Context(), *req.QQComplete)
			}
		}
		if err != nil {
			out.OK = false
			out.Error = "绑定服务暂不可用"
			if !strings.Contains(err.Error(), "Error ") && !strings.Contains(err.Error(), "sql:") {
				out.Error = err.Error()
			}
		}

	case "clear_stats", "moderation_accounts", "bans", "change_ban":
		var err error
		if req.Action == "change_ban" {
			if req.BanChange == nil {
				err = errors.New("invalid ban request")
			} else {
				out.Ban, err = a.changeBan(r.Context(), *req.BanChange)
			}
		} else {
			q := moderation.Query{}
			if req.ModerationQuery != nil {
				q = *req.ModerationQuery
			}
			switch req.Action {
			case "bans":
				out.Bans, err = a.moderationBans(r.Context(), q)
			case "moderation_accounts":
				out.ModerationAccounts, err = a.moderationAccounts(r.Context(), q)
			case "clear_stats":
				group, _ := s.repositoryGroup()
				reader, ok := group.Character.(repository.ClearHistoryReader)
				if !ok {
					err = errors.New("clear history unavailable")
				} else {
					var page moderation.ClearPage
					page, err = reader.QueryClears(r.Context(), q)
					out.Clears = &page
				}
			}
		}
		if err != nil {
			out.OK = false
			out.Error = moderationError(err)
		}

	case "online":
		if req.CharacterID == 0 {
			out.OK = false
			out.Error = "character required"
			break
		}
		_, out.Online = s.onlineGameSession(req.CharacterID)
	case "catalog":
		var err error
		out.PremiumTypes, err = s.currentGMAdministrativePremiumTypes()
		if err != nil {
			out.OK = false
			out.Error = "premium catalog unavailable"
			break
		}
		catalog, err := s.currentPremiumCatalog()
		if err != nil {
			out.OK = false
			out.Error = "crystal catalog unavailable"
			break
		}
		out.CrystalCubeItemIDs = catalog.crystalCubeIDs
	case "activities", "save_activities":
		if s.activityControls == nil {
			out.OK = false
			out.Error = "activities unavailable"
			break
		}
		snapshot := s.activityControls.Snapshot()
		if req.Action == "save_activities" {
			var err error
			snapshot, err = s.activityControls.Save(r.Context(), req.Revision, req.Changes)
			if err != nil {
				out.OK = false
				out.Error = activitySaveErrorPrompt(err)
				break
			}
			s.logInfo("game-admin-activities-saved", "revision", snapshot.Revision)
		}
		out.Activities = &snapshot
	case "encode_stack":
		if req.Stack == nil || req.Slot < 0 || req.Stack.ItemID <= 0 || req.Stack.Count <= 0 {
			out.OK = false
			out.Error = "invalid stack"
			break
		}
		entry := currentItemListEntryFromStack(req.ListType, req.Slot, *req.Stack)
		out.StackRow = append([]byte(nil), entry.data[:]...)
	default:
		out.OK = false
		out.Error = "unknown operation"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// Storage failures remain generic even when they wrap an operator sentinel.
func activitySaveErrorPrompt(err error) string {
	switch {
	case errors.Is(err, onlineevent.ErrControlPersist):
		return "保存活动设置失败，请稍后重试或查看服务端日志"
	case errors.Is(err, onlineevent.ErrControlRevisionStale):
		return "活动设置已更新，请刷新后重试"
	case errors.Is(err, onlineevent.ErrControlSelectionInvalid):
		return "请选择要修改的活动后重试"
	case errors.Is(err, onlineevent.ErrControlUnsupported):
		return "所选活动当前服务端不支持，请刷新后重试"
	case errors.Is(err, onlineevent.ErrControlDuplicate):
		return "同一活动重复提交，请刷新后重试"
	case errors.Is(err, onlineevent.ErrControlRevisionExhausted):
		return "活动设置版本号已用尽，无法继续保存，请联系管理员检查服务端配置"
	default:
		return "保存活动设置失败，请稍后重试或查看服务端日志"
	}
}
