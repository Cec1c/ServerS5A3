# Luke二阶段结果页移除再次挑战

固定输入为已发布v155 SHA256 `0762198fdc0c56a35305325c52ef876a05cc75c8fa849bb6a816deebba352bff`，输出SHA256 `3fdc7c17584731cae240ca5e54122a4180485aab05b7fff8114a14d7822bf2ad`。

当前合法Luke团本、当前结果面板state10和当前二阶段地图共同限定修改：隐藏并禁用retry，阻止鼠标/快捷键及默认入口最终index0提交；保留特殊模式默认index2回城及显式其他按钮。原生选择与UI清理继续执行。普通副本和Luke一阶段沿用原行为，已有v155团队牌就绪补丁字节保持。

地图集合由配套PVF `94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7` 的 `etc/raid/luke/luke.etc`只读提取。`phase_maps.json`仅保留地图编号和摘要，不包含PVF原文。构建器不修改或加载运行资源。

Windows Python，验证依赖Unicorn；先安装依赖，再在本目录执行：

```text
python build_candidate.py --input DNF-v155.exe --output build
python verify_candidate.py --input DNF-v155.exe --candidate-dir build
```

原生验证69项覆盖身份、12图、显示/启用、默认回城、真实鼠标与快捷键调用点、普通副本恢复、栈及寄存器；部分UI、STL、网络和特殊模式查询叶函数使用夹具。它不替代GUI实机验收。构建增加一页RX节并核验四处hook、PE校验和及允许差异范围；候选验证器独立复建逐字节一致。
