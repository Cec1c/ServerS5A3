package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

const currentSkillQuickSlotCount = 12

// 领域和既有数据库使用0..5、198..203两段快捷栏，其间是四组48格。
// 当前固定90 EXE的295cc10/295ccd0将0..11作为快捷栏，295e560将12..203
// 作为普通技能区。仅在协议边界做可逆排列，两页都保留原加点、位置和冷却。
func currentSkillSlotToWire(slot int) (int, error) {
	switch {
	case slot < 0 || slot >= 204:
		return 0, fmt.Errorf("技能槽位超出已核对范围: %d", slot)
	case slot < 6:
		return slot, nil
	case slot >= 198:
		return slot - 192, nil
	default:
		return slot + 6, nil
	}
}

func currentSkillSlotFromWire(slot int) (int, error) {
	switch {
	case slot < 0 || slot >= 204:
		return 0, fmt.Errorf("客户端技能槽位超出已核对范围: %d", slot)
	case slot < 6:
		return slot, nil
	case slot < currentSkillQuickSlotCount:
		return slot + 192, nil
	default:
		return slot - 6, nil
	}
}

// 仅转换已满足op28长度和槽位范围的请求；畸形请求仍由原handler拒绝。
// 两种传输在进入同一个领域路由前调用，避免重复转换或遗漏upper入口。
func currentSkillSlotRequestBody(opcode uint16, body []byte) []byte {
	if opcode != uint16(dnfenum.CmdPacketChangeSkillslot) || len(body) != 8 {
		return body
	}
	from, fromErr := currentSkillSlotFromWire(int(body[1]))
	to, toErr := currentSkillSlotFromWire(int(body[2]))
	if fromErr != nil || toErr != nil {
		return body
	}
	next := append([]byte(nil), body...)
	next[1], next[2] = byte(from), byte(to)
	return next
}

func currentSkillSlotResponseBody(response alignedcmd.UpperResponse) ([]byte, error) {
	body := response.Body
	if response.Classification != 1 || len(body) == 0 || body[0] == 0 {
		return body, nil
	}
	switch dnfenum.CmdPacket(response.MsgID) {
	case dnfenum.CmdPacketChangeSkillslot:
		if len(body) != 4 {
			return nil, fmt.Errorf("技能拖动回包长度无效: %d", len(body))
		}
		from, err := currentSkillSlotToWire(int(body[2]))
		if err != nil {
			return nil, err
		}
		to, err := currentSkillSlotToWire(int(body[3]))
		if err != nil {
			return nil, err
		}
		next := append([]byte(nil), body...)
		next[2], next[3] = byte(from), byte(to)
		return next, nil
	case dnfenum.CmdPacketBuySkill:
		if len(body) < 8 {
			return nil, fmt.Errorf("技能学习回包长度无效: %d", len(body))
		}
		next := append([]byte(nil), body...)
		offset := 7
		for i := 0; i < int(body[6]); i++ {
			if offset+5 > len(body)-1 {
				return nil, fmt.Errorf("技能学习回包条目不完整: %d", i)
			}
			// 0xff是原生特殊技能路径的哨兵，不是普通布局位置。
			if body[offset] != 0xff {
				slot, err := currentSkillSlotToWire(int(body[offset]))
				if err != nil {
					return nil, err
				}
				next[offset] = byte(slot)
			}
			hasCommand := body[offset+4] != 0
			offset += 5
			if hasCommand {
				if offset >= len(body)-1 {
					return nil, fmt.Errorf("技能学习回包缺少指令长度: %d", i)
				}
				offset += 1 + int(body[offset])
				if offset > len(body)-1 {
					return nil, fmt.Errorf("技能学习回包指令数据不完整: %d", i)
				}
			}
		}
		if offset != len(body)-1 {
			return nil, fmt.Errorf("技能学习回包尾部长度无效: %d", len(body)-offset)
		}
		return next, nil
	default:
		return body, nil
	}
}
