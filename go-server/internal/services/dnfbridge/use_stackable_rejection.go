package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// op44（使用消耗品）被拒绝时必须回一个失败 ACK。
//
// 背景：op44 的失败路径此前是 `ownerBlockedResult`（ResponseAllowed=false），bridge
// 一个包都不发。客户端发出"使用"之后一直等结果，玩家看到的就是"使用无效，而且之后
// 任何操作都点不动"（春节欢庆爆竹等 [random reward item] 道具的报障正是这个现象，
// 与服务端是否真的认识该道具无关）。
//
// 这里回一个当前客户端已验证过的 op44 错误体，形状与专家职业配方学习失败一致
// （byte 0 + errorCode + listType + u32 instance + u32 itemCode）。只回失败，
// 不伪造成功：物品不会被消费，也不写任何库。
const currentUseStackableRejectionCode byte = 13

func (s *Service) answerRejectedUseStackable(session *gameSession, body []byte, reason string) error {
	return s.answerRejectedUseStackableOpcode(session, dnfenum.CmdPacketUseStackable, body, reason)
}

// answerRejectedUseStackableOpcode 与 answerRejectedUseStackable 相同，但按传入的
// 使用类 opcode 回包。op251（共享特效道具，爆竹族）同样是"点一下就等结果"的路径，
// 被拒时没有回包就会再次出现"背包假死"，所以两条 opcode 共用这里的失败回包。
func (s *Service) answerRejectedUseStackableOpcode(
	session *gameSession,
	opcode dnfenum.CmdPacket,
	body []byte,
	reason string,
) error {
	if s == nil || session == nil {
		return nil
	}
	request, err := decodeUseRequestForOpcode(opcode, body)
	if err != nil {
		// 连请求体都解不出来时没有可回填的 listType/instance/itemCode，只能留日志。
		s.logInfo("dnfbridge use-stackable rejected before parse; no ack echoed",
			"conn_id", session.connID,
			"character_id", session.selectedCharacterID,
			"opcode", uint16(opcode),
			"reason", reason,
			"error", err)
		return nil
	}
	ack := buildCurrentExpertJobRecipeLearningErrorBody(
		currentUseStackableRejectionCode,
		request.ListType,
		request.InstanceValue,
		request.ItemCode,
	)
	if err := s.sendGameUpperRawClass(
		session,
		uint16(opcode),
		ack,
		dnfproto.DefaultChannelClassification,
	); err != nil {
		return err
	}
	s.logInfo("dnfbridge use-stackable rejected; sent failure ack",
		"conn_id", session.connID,
		"account_id", s.accountIDForSession(session),
		"character_id", session.selectedCharacterID,
		"opcode", uint16(opcode),
		"list_type", request.ListType,
		"slot", request.SlotIndex,
		"item_code", request.ItemCode,
		"instance", request.InstanceValue,
		"error_code", currentUseStackableRejectionCode,
		"reason", reason)
	return nil
}

// decodeUseRequestForOpcode 把两个"使用"opcode 的请求体统一成失败回包需要的三元组。
func decodeUseRequestForOpcode(opcode dnfenum.CmdPacket, body []byte) (dnfinventory.UseStackableRequest, error) {
	if opcode == dnfenum.CmdPacketUseSharedEffectItem {
		shared, err := dnfinventory.DecodeUseSharedEffectItemRequest(body)
		if err != nil {
			return dnfinventory.UseStackableRequest{}, err
		}
		return dnfinventory.UseStackableRequest{
			SlotIndex: shared.SlotIndex,
			ListType:  shared.ListType,
			ItemCode:  shared.ItemCode,
		}, nil
	}
	return dnfinventory.DecodeUseStackableRequest(body)
}
