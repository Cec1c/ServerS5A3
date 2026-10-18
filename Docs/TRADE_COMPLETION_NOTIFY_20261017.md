# 交易完成通知逐收件人独立发送

原 MR !72 本轮同步在线 master `5942720`。用户19:12验收失败保持；本轮只处理 commitOnlineItemTrade 在原子交换成功后逐收件人发送时的一个缺陷：第一个收件人的完成包（op18）、金币刷新（op14）或列表刷新任一写失败就 return，导致第二个收件人完全没有通知。资产交换本身此前已经成功，缺陷只在提交后的通知循环。

修复把单个收件人的三段发送收进 sendOnlineItemTradeCompletion，外层按 first/second 顺序各自调用。每个收件人的包序仍是完成包→金币（仅角色变更）→当前容器列表；该收件人自身在某一步失败就按原序停在失败点，不再补发后续包。另一收件人不受影响，继续自己的完整序列。所有收件人错误用 errors.Join 连同角色号保留并返回，双方同阶段同时失败时两个注入错误都能被 errors.Is 命中。离线收件人继续按原逻辑跳过。

事务顺序保持资产先提交、trade 后清理：owner.Exchange 失败仍走原取消路径且不发完成通知；成功后先 clearOnlineItemTrade，再逐收件人通知。因此通知失败不会回滚已提交资产，也不会留下可再次结算的 trade；之后重复 ready 因 trade 已清理而走既有无活动交易拒绝，不重复给金币或道具。测试在每次通知失败后重放双方 ready，确认双方无完成包且资产只结算一次。

旧源码红测 trade-old-red.jsonl 复现首收件人 completion/gold/list 三个子场景失败，健康收件人消息为空（want [18 14 13]）；同版本的第二收件人三个子场景通过，证明原缺陷是“先失败者阻断后者”。新 trade-green.jsonl 的 TestOnlineItemTradeCommitNotifiesEveryRecipientWhenIndividualStageFails 覆盖 first/second/both × completion/gold/list 共 9 个子场景，全部通过：失败收件人只出现失败点之前的包，健康收件人收到 [18 14 13]，双方同时失败时 errors.Is 同时保留两个错误，重复 ready 无完成通知且资产只提交一次。trade-old-red 的旧快照没有 both 分组，不能把 9 组红测都算作已复现。

trade-green.jsonl 初次运行另有一个与本通知修复无关的失败：旧 TestOnlineAccountCrystalTradeRealPVFSixFreeItems 把 PVF 哈希写死为 ad7d05…，拒绝当前 a56… 资源，报 item_trade_crystal_commit_test.go:151 PVF hash mismatch。没有改窄哈希门，也没有用当前资源冒充旧资源。随后按资源分组重跑 trade-targeted-results.json：current a56 为 45 通过/0 失败/0 跳过，historical ad7 真实旧六晶体专项为 7 通过/0 失败/0 跳过，domain itemtrade 为 33 通过/0 失败/0 跳过。完整 validation-trade 仍在运行，最终完整结果由父回执补充，本文件不宣称完整检查已通过。

本修复不代表数量上限、对方显示或闪退问题都已修好，这些问题仍按各自证据和验收状态处理。NOTI8（class0/op8 交易应答提示）短包尾部时间字段仍无权威来源；当前只按已验证的 responder:u16、mode:u8、value:u32 发送，未猜测或填造生产时间字段。通知路径其它未验证字段保持原样。

本轮源码候选未部署，未访问生产，R52 服务、客户端 v125 与 PVF 保持；用户19:12验收失败保留，原 MR !72 不合并。本分支以原 `cc17ed9` 接续并整合 master `5942720`，完整检查与最终提交回执单独保存。
