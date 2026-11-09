# 发放勋章的穿戴与守护珠镶嵌

邮件等发放路径可能保存勋章的物品类别、PVF 路径及品质种子，却没有保存不透明的 119 字节实例行。普通装备、装扮和三色宠装已有带证据的实例重建；勋章位于 list38，该入口却没有接纳它，合法穿戴会返回 `equipment raw entry evidence is missing`。没有真正穿到槽32的勋章也无法成为守护珠的合法目标。

补丁仅把 list38 的单件 `[flag]` 到槽32纳入既有重建。勋章来源页仍限0～48，守护珠页49～97不能作为勋章穿戴；品质种子、PVF 装备身份和本次请求的非零实例证据仍必须齐备。保存前继续检查实际 PVF 类型、角色归属、等级和既有物品限制。已有实例行沿用原路径，不重随机品质、发放物品或修补玩家数据库。

回归覆盖首末槽穿脱、缺证据/错误类别/错误目标/守护珠页的无损拒绝，以及实际穿戴后镶嵌、消耗重放拒绝、重新创建 owner 后穿脱和四孔持久化。可选的真实资源检查使用原版物品定义及内存仓储：

```text
DNFBRIDGE_REAL_PVF_SMOKE=/path/to/Script.pvf go -C go-server test -buildvcs=false -count=1 -run '^TestRealPVFGrantedMedalEquipThenGuardianGemSurvivesReload$' ./internal/services/dnfbridge
```

没有指定资源会跳过，跳过不计为通过。该专项不执行客户端渲染、真实数据库或生产部署。此修复不改变守护珠四孔及同属性互斥规则，也不声称覆盖所有原因导致的勋章/守护珠操作失败。
