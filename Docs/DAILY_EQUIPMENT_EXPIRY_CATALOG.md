# D17 每日装备期限与全类型目录边界

## 固定范围

基底 `c1d8727205e8392c85413647c08855d6e1e7f859`。PVF SHA256 `25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`。本候选未部署，不触碰旧玩家数据或迁移旧实例。

全目录审计覆盖232662份equipment/stackable的.equ/.stk，208946条清单引用、774种header，解析错误/缺引用均0。字段存在不等于删除许可；详细矩阵及全部原始枚举留在独立运行证据中，不打包进业务补丁。

## 实证问题及修复

真实项链400300075（耀之荣光项链，租赁）声明 `[daily delete item] 1`。原catalog只读取stackable的该字段；实际grant事务产出实例没有期限。全资源中装备含该字段248份，其中247份值1且无其他绝对/相对期限冲突，另一份400330115值14，本候选保持拒绝。

仅识别精确值1、无绝对/相对期限冲突的每日装备。既有PVF grant的BuildNew记录 `runtime_pvf_daily_equipment_grant` 来源；继续使用现有下一次06:00（UTC+8）边界，不发明首次穿戴起算。清理须同时满足真实PVF路径/模板、来源、ExpireAt、daily_delete_at、expire_time、expire_unix一致，且期限是整秒06:00。加载不建立新来源；旧租赁、GM、缺失或冲突来源保持。

库存和普通穿戴actor12..25共用已有资产事务、每日时钟及提交后op14/穿戴后置通知。失败事务不发清槽；旧代次不提交。没有新增定时器、包号或自制属性更新。装备期限不能覆盖0x0B耐久：每日投影只对stackable写倒计时，通用行兜底也跳过PVF明确equipment（零耐久仍是零）。

## 有意义的验证

新测试从真实模板经过既有grant事务获得来源和期限，再执行明确玩家op19、合法项链槽19、到期回调、持久删除、16字节头下行list3 op14和重复清理。另含库存保留/错误owner/旧代次、穿戴保存失败回滚再续办及零耐久与stackable/无元数据旧行兼容控制。未从测试直接seed有效grant来源。

普通CI无真实资源时资源测试Skip；显式坏资源路径Fatal。真实目标命令：

```powershell
$env:DNFBRIDGE_DAILY_EQUIPMENT_PVF='<approved Script.pvf>'
$env:DNFBRIDGE_RELATIVE_EXPIRY_PVF=$env:DNFBRIDGE_DAILY_EQUIPMENT_PVF
$env:GOMAXPROCS='3'
$env:GOFLAGS='-mod=readonly -p=2'
# use the approved toolchain/cache in the local runner
& '<go.exe>' test -buildvcs=false -count=1 ./internal/services/dnfbridge -run '^(TestDailyEquipmentReal.*|TestExpiryEquipmentZeroDurabilityKeepsCarrier|TestRelativeTrialWornPreservationAndRollback|TestRelativeTrialOnlineWornExpiryAndProjectionRetry|TestDailyDeleteGrantPersistsBoundaryAndSeparatesNextDay|TestDailyDeleteInvalidDeadlineRollsBackAllContainerDeletion|TestDailyDeleteOnlineAtSixUsesCommittedOp14AndRejectsStaleCharacter)$'
```

相对期限补丁保留；目录扫描及报表不作为默认CI测试。

## 保持开放

- 旧实例没有可信grant来源，不能由模板名/日历反推发放日批删。
- 邮件装备缺期限、其他独立reward owner未带daily来源，需要逐入口证据；不能把本BuildNew覆盖称为全部发放入口覆盖。
- 相对装扮/宠物等穿戴槽不在12..25清理范围；各自原生对象、属性/技能回收未证明。
- 绝对活动日期沿既定续期政策，尚无通用绝对到期删除闭环；27份绝对/相对混合、28份relative加usable expired item不得统一删除。
- 解封起算26075、带期限三元礼包84份、usable datetime98份、title expiry date87份及usable expired item419份均有明确目录证据，但未知原生语义不能由字段名推断。usable period另有两份日期字符串畸形资源490007562/490008814，未猜转换。
- D156晶块槽错误准入另案。合法op19测试从已就绪测试会话开始，不代表完整游戏登录或NPC租赁购买；op14解析绿不等于GUI/套装/附魔/被动回收实机通过。
