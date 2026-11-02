# 成长胶囊和经验药剂状态

此前 op30 已处理 SP/TP 书和复活币，但没有消费 PVF 的经验胶囊定义。新增领域处理器使用已锁定的背包实例和服务器经验表，客户端提供的数量或经验值不参与授予。

## 定义与事务

支持 increase status type 的 expUp、expPercentUp、expUpByPercent、expUpByTable 和 expUpByCrackOfDimension。百分比以当前等级完整经验跨度计算后向下取整；区间表使用命中的固定数量；裂缝兼容公式使用 questParameter 零基等级行乘 56/100。未识别的类型继续进入既有处理路径，已识别但非法的定义拒绝消费。

原生 etc 0/1 变体均可以承载这些效果；允许的字段来自实际定义审计。cool time 持久记录在角色账本。交易、装备等实例不能伪装为胶囊；已过独立期限、超等级、锁定、错误归属、冷却未到和不匹配场景均不扣除物品。百分比计算和跨等级技能点遵循既有 progression 模块。

成功时先提交角色、背包与技能点事务，再发送 op30 ACK、源槽位 op14 和绝对角色状态 op37；跨级时刷新既有角色资料。副本中同步当前角色快照，避免后续战斗结算使用旧等级。任何保存或最终代次校验失败必须回滚。

## 药剂图标协议

当前客户端 class0/174 的注册处理器读取 u8 行数，每行读取 u32 物品 ID 和 i32 剩余秒数。道具效果方法乘以 1000 并加到本地时钟。值 0 用于到期；不发送会重置完整 PVF 时长的 -1。服务端对剩余时间向上取整，超过原生有符号毫秒范围时分段刷新。

只从已提交的 clear/quest 药剂账本投影图标。普通药剂 op44 和任务药剂入口都在 ACK/源槽位刷新后同步；城镇切图、选角结束及频道重连专用路由恢复剩余时长。定时回调进入同会话事件队列并复核角色代次及投影版本，每次续用具有独立计时器名称，迟到的旧重试不能替换新计时。队列满时最多重试三次；本地客户端仍按原生倒计时到期。

本改动不新增怪物经验倍率，也不改变现有通关/任务药剂互斥及冷却规则。

## 验证

执行仓库标准领域、桥接、control、vet、build 和边界检查。定向回归可使用：

```text
go test -count=1 ./internal/modules/dnf/experienceitem ./internal/modules/dnf/inventory
go test -count=1 ./internal/services/dnfbridge -run 'Capsule|ExperiencePotion|LevelChallenge'
```

设置 DNFBRIDGE_REAL_PVF_SMOKE 为匹配资源后，TestRealPVFAllExperienceCapsuleDefinitions 扫描五种实际效果，TestRealPVFActivityFiveAndFifteenCapsules 执行 5%/15% 道具真实路由；未设置时跳过不算通过。

scripts/client/test_experience_item_effect.py 要求 pefile/unicorn 与精确摘要的客户端输入，回放原生174注册、20种行数/对齐输入与5种秒数转换。物品构造、析构及调用边界使用桩，不能代替图标画面或在线稳定性验收。运行输出写入外部新文件，客户端原文件只读。

满级预约显示回归覆盖89/90/91级、重排、未注册和删除角色，验证历史计数保留，仅隐藏达到当前等级上限的活动标记。
