# 装备条目保存的闭合参数绑定增量

`binding.patch`仅公开已授权的`equipmentSaveScope`增量源码及专项测试。旧发布器基座不随本次公开；此目录不提供可直接运行的完整发布器，也不要求把旧私有历史导入公开仓库。

补丁把可选字段限制为`collection`或`entry`，拒绝其它类型、大小写变体、空值以及任意环境变量字段。请求值贯穿资源清单、目标实例、生成环境文件、容器环境和进程读回；缺省时保持原配置，按原备份路径回退。环境文件出现重复键或不明确换行时仍拒绝，既有其它参数与资源验证保持。

使用者须自行具备兼容的原发布器，并逐文件核对`inputs.json`中的原件SHA256。补丁采用零行上下文，只能用于这些精确输入；不能忽略摘要、使用模糊匹配或强制套到不同版本。

```text
git -c core.autocrlf=false apply --check --unidiff-zero /path/to/binding.patch
git -c core.autocrlf=false apply --unidiff-zero /path/to/binding.patch
```

应用后逐文件核对结果摘要，再在原目录运行补丁内的`EquipmentSaveScopeInbox`、`EquipmentSaveScopeContract`及原有候选生成器检查。所需的旧`game_adapter`、原发布器和其它支持模块均是外部依赖，未随该增量重新授权公开。本次在隔离副本对精确原件应用补丁并执行相关契约，不调用生产入口。

公开主干已有`deploy/docker/manage.py`的`DNFBRIDGE_EQUIPMENT_SAVE_SCOPE`闭合参数支持及服务端按条目保存能力，可单独使用。该补丁补充资源发布时的参数绑定；代码公开不表示某台机器已安装、环境已切换或游戏体验已验收。
