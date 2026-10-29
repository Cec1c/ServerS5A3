package dnfbridge

// Return only fixed categories; repository errors can contain private data.
func currentEmpireRosterRejection(err error) string {
	if err == nil {
		return "none"
	}
	switch err.Error() {
	case "帝国竞技场角色列表已变更，请重新获取后设置队员":
		return "changed_roster"
	case "帝国竞技场队员等级不足":
		return "level"
	case "帝国竞技场队员不存在、已删除或不属当前账号":
		return "ownership"
	case "帝国竞技场账号不存在":
		return "account_missing"
	case "帝国竞技场账号日账本不完整", "帝国竞技场日账本时间超前", "帝国竞技场账号账本长度无效", "帝国竞技场账号账本存在尾随内容", "帝国竞技场挑战名单缺少凭据":
		return "day_ledger"
	default:
		return "other"
	}
}

// Keep the team-window handshake observable without enabling raw packet logs.
// Only bounded routing/count metadata is retained, never account or error text.
func (s *Service) logEmpireTeamSetupDiagnostic(kind string, fields ...any) {
	switch kind {
	case "帝国竞技场队伍框资料请求已收到", "帝国竞技场队伍框资料请求已忽略",
		"帝国竞技场应组队设置请求打开队伍框", "帝国竞技场休息室角色表尚未就绪",
		"帝国竞技场选择框角色明细未就绪", "帝国竞技场已恢复保存队伍",
		"帝国竞技场队员设置已拒绝，未扣费", "帝国竞技场队员设置已保存":
	default:
		return
	}
	args := []any{"kind", kind}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		switch key {
		case "conn_id", "channel_id", "body_len", "server", "count", "other_servers", "first_selector", "second_selector", "first_server", "second_server":
			args = append(args, key, fields[i+1])
		case "stage":
			if value, ok := fields[i+1].(string); ok {
				switch value {
				case "decode", "sent_roster", "session", "selector", "current_member", "lobby_ready", "asset_reservation", "owner", "current_record", "current_town", "register_transaction":
					args = append(args, key, value)
				}
			}
		case "rejection":
			if value, ok := fields[i+1].(string); ok {
				switch value {
				case "none", "changed_roster", "level", "ownership", "account_missing", "day_ledger", "other":
					args = append(args, key, value)
				}
			}
		case "error":
			args = append(args, "failed", fields[i+1] != nil)
		case "reason":
			// These are fixed call-site reasons; arbitrary caller text is omitted.
			if value, ok := fields[i+1].(string); ok {
				switch value {
				case "角色未就绪或请求正文无效", "角色会话已失效", "休息室角色状态尚未完成":
					args = append(args, key, value)
				}
			}
		}
	}
	s.logInfo("dnfbridge empire team setup diagnostic", args...)
}
