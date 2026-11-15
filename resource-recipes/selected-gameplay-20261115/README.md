# 所选玩法资源与正式客户端配方

配套说明见 `Docs/SELECTED_PUBLICATION_20261115.md`。本目录提供完整可复建的所选增量，不包含原始完整 PVF。

## PVF

先执行上一批公开配方 `resource-recipes/selected-features-20261113`，得到 SHA256 `5d7eb550238e6b9a9fc18b4dde6fc510c2050a809aaa44b757a5beb79ece03ed` 的输入。使用已公开模块 `resource-recipes/seria-lucky-boxes/source/private-additions` 编译工具，再执行：

```text
private-additions -pvf <matching-input.pvf> -plan selected.pvf-plan.json -out <fresh-output.pvf> -report <preservation.json>
```

固定输出 SHA256 为 `2b5453f0ad56aad5a1768cb15881fdb746975a83fbc21d7fc987a5b19b226995`。计划改 3 条现有路径、增加 3 个黑钻定义；完整读回核验了所有目标 token，1126598 条其他文件保持。商城现售/价格、卢瑟期限和 secretshop 原始规则保持输入原值。卢克与超时空奖励容器和 10% 权重已存在于精确输入；服务端补齐对应代币分支的原生奖励。此基础包含既有累计规则，不能把新计划当任意 PVF 的通用覆盖。

## 正式 EXE

先获得公开 V214 的正式 EXE，摘要 `6402835223a23f9e4300c57c175d1b990b78ab0eecc46bd03fd373d0893e73f4`，112812032 字节。Python 依赖见 `client/requirements.txt`，运行：

```text
python client/build_client.py --input <absolute-V214-DNF.exe> --output-dir <absolute-fresh-directory>
python client/verify_native.py <base-exe> <candidate-exe> <report.json>
```

输出 EXE 为 `28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84`，112816128 字节。严格核对 PE 节、头、导入/资源/重定位、受保护指令、完整原有数据和新消息位；只改变经审核货架 hook、位图和新增节/对应头。构建器不启动游戏。

同包必须保留 `CombatPower.dll`（140ff544…，706048 字节）、`DpsMeter.dll`（6e907abd…，493056 字节）和 `ImagePacks2/sprite_interface_private_features20261113.NPK`（fc8d96cc…，125034 字节）；完整摘要见上一批 `client-features-native/manifest.json` 和本次 Release 清单。INI 仍使用玩家原有设置。

客户端成品的正式 Release 和 GitGud 镜像在合入后从该公开提交重建并单独核对。PVF 修改与 EXE 更新均应和同批服务端配套，公共发布不触发任何运行中服务变更。
