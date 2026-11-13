# 所选资源修改配方

本配方只提取本批选择的稀有装扮八件规则、两件稀有装扮覆盖、鲁特勇者套装自选礼盒（1阶段）续期，以及现售礼包优惠券对应关系。原始 PVF 不随源码提供。

固定输入 SHA-256 为 `d0dca8fb7d1a512165f2214431e07ab47a62368affb96b94ba61f2b16d6dc57f`。`selected.pvf-plan.json` 只列六条已选路径；`manifest.json` 固定计划摘要和范围。计划保留 token 类型及已有 payload，字符串增量沿已公开工具规则追加。

使用已公开的 `resource-recipes/seria-lucky-boxes/source/private-additions` 模块构建工具后执行：

```text
private-additions -pvf <matching-input.pvf> -plan selected.pvf-plan.json -out <new-output.pvf> -report <preservation.json>
```

工具拒绝错误输入摘要或已存在输出；结果报告核对所有未修改文件及新 token。输出与保真验证不替代完整客户端画面或玩法验收。

帝国月限购与双材料扣除是服务端对既有商品定义的修正，本配方没有为其替换商店。其他未选择的道具期限、增幅失败规则、兑换价格及拍卖许可调整保留原边界。

本次离线复建输出 SHA-256 为 `5d7eb550238e6b9a9fc18b4dde6fc510c2050a809aaa44b757a5beb79ece03ed`，六条路径变化，1126595 条其他文件逐项保真，新 token 验证通过。
