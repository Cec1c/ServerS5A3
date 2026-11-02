# 已授权公开的三条目文本修正配方

本目录公开三份既有物品文档的四段文本修正：490701733 的名称与说明、10151892 的说明、990031101 的艾肯专用说明。计划不改变价格、掉落、强化或增幅规则。`classification` 保留原配方来源标记，当前目录已获授权公开。

输入 `Script.pvf` 的 SHA256 为 `95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c`；目标为 `2e416c0b5464b7aa9ddbd3949f7294a9f971d2546a1ed3df354f2815dc43b29d`。完整输入已在 `resources-selected-20261101` 资源发行中提供；目标及同代六项 GM 索引见本次资源发行和 `../../selected-20261102/`。原生成工具的版本与文件摘要见 `manifest.json`，本目录不捆绑该工具或私有发布控制器。

先用匹配的 PVF-CLI 将以下三条目的原始 tokens 导出至一个目录，分别命名为 `base-490701733.tokens.json`、`base-10151892.tokens.json`、`base-990031101.tokens.json`：

- `stackable/cash/chn_490701733.stk`
- `stackable/10151001/10151892.stk`
- `stackable/private_aiken_exchange/990031101.stk`

创建新的空证据目录后执行：

```text
python build_item_text.py --base <基线PVF> --pvfcli <固定pvf.exe> --tokens-dir <基线tokens目录> --output-dir <新证据目录>
python -m unittest discover -s . -p test_build_item_text_receipts.py -v
```

构建器校验输入、工具及计划摘要，保留全部非目标 token 位型和未修改条目；先 patch 再独立 verify，最后读回 tokens 与文本。已有候选或回执拒绝覆盖。测试默认使用公开的脱敏回执夹具；设置 `ITEM_TEXT_RECEIPT_DIR` 可以针对实际 patch/verify 回执执行同一校验。

本次资源发布复用此前生成并读回验证的六项索引，未再次全量生成。索引输入、格式、文件摘要见资源清单；专用索引缓存／部署工具不在此次源码公开范围。公开资源不会修改玩家更新通道或重启服务。
