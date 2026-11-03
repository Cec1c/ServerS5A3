# 金色小晶块购买价 400

物品 3262 的定义原有 [value] 400。此配方只增加显式 [price] 400，使客户端商店价格与服务端购买价格一致，保留出售价值、身份、描述、图标及其他条目。

    python build_gold_price.py --base <匹配manifest的Script.pvf> --tool <匹配manifest的PVF-CLI> --output <不存在的输出目录>

输入 PVF、工具与计划均核对 SHA256。脚本执行 patch、verify 和 token 读回，验证只新增两个 token、未修改其他条目。输出及回执留在新目录；不发布、不改生产、不重建索引。--verify-existing 可核对同目录既有候选及原始 patch 回执，保留原始操作证据。

配方绑定历史累计基线。应用到新累计资源时，应重新导出本条目的 token、审查并生成新的绑定，不能移除摘要校验，也不能用旧累计 PVF 覆盖新内容。完整 PVF 与工具需自行提供。
