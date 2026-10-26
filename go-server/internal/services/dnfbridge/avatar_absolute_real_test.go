package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestRealPVFAbsoluteAvatarLiveRequestReplay(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	// 按已观测原生144字节布局构造语义夹具，不包含私人报文头、原槽号或内存快照。
	b := absoluteAvatarReplayBody(39643, 4, 39544, 401510016, 401540010, 101530639, 101520685, 401500030, 101530637, 101570589)
	r, err := parseCurrentAbsoluteAvatarRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]dnfrepo.ItemStack{"0:120": {ItemID: 10007850, Count: 1}}
	for _, material := range r.Materials {
		m[fmt.Sprintf("1:%d", material.Slot)] = dnfrepo.ItemStack{ItemID: int64(material.ItemID), Count: 1}
	}
	repos, cat := mustAvatarCompoundLiveAssets(t, real.source, "0", m)
	svc := currentAvatarCompoundTestService(repos, cat)
	conn := &bufferConn{}
	if err = svc.handleCurrentAbsoluteAvatar(&gameSession{conn: conn, selectedCharacterID: 19}, b); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(ack.Body) != 55 || ack.Body[0] != 1 {
		t.Fatalf("真实梦想未成功: %x", ack.Body)
	}
	after, _, _ := repos.Inventory.Load(context.Background(), "19")
	if len(after.Slots) != 1 || after.Slots["1:0"].ItemID != 39643 || after.Slots["1:0"].Extra["ext_data0"] != "4" {
		t.Fatalf("真实梦想资产不符: %+v", after.Slots)
	}
}

// 业务字段来自真实PVF案例，其余字节与槽号使用固定合成数据。
func absoluteAvatarReplayBody(target uint32, ability uint16, materials ...uint32) []byte {
	body := make([]byte, currentAbsoluteAvatarRequestSize)
	for i := range body {
		body[i] = byte(i*37 + 11)
	}
	binary.LittleEndian.PutUint16(body[13:], 120)
	binary.LittleEndian.PutUint32(body[16:], target)
	binary.LittleEndian.PutUint16(body[20:], ability)
	binary.LittleEndian.PutUint16(body[22:], uint16(len(materials)))
	for i, item := range materials {
		binary.LittleEndian.PutUint16(body[24+6*i:], uint16(i))
		binary.LittleEndian.PutUint32(body[26+6*i:], item)
	}
	return body
}
