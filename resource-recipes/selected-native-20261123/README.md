# 原生功能、拾取显示与 FPS 累计资源

推荐直接安装[已配套的累计公开包](https://github.com/Cec1c/ServerS5A3/releases/tag/client-v217-public-native-cumulative-20261123)，核对本目录 `manifest.json`。功能、服务端配置和升级步骤见[搭建入口](../../Docs/PUBLIC_FEATURE_DEPLOYMENT_20261123.md)。下列命令供重建与验证使用，输入输出须分开。

## 输入链

1. 从[公开 V216](https://github.com/Cec1c/ServerS5A3/releases/tag/client-v216-selected-feedback-20261117)取得客户端，配套公开完整 PVF `66482976…`。
2. 按 [selected-raid-shop-20261118](../selected-raid-shop-20261118/README.md)生成 EXE `a853463e…` 和 PVF `6aaca082…`。
3. EXE 先应用[兵营进度](../fiendwar-barracks-board/README.md)，再应用 [selected-gameplay-20261122](../selected-gameplay-20261122/README.md)，得到 `21a9ed0b0c86e8e3a95f7b6c84270025c91219d3df3e9143bdd6629264c9166e`；PVF 应用同目录配方，得到 `f86090d9f39dc558a397062f5a794496343c1dd7e04a2443b982733c2c6418ee`。

## 客户端与插件

Python 3.11+，安装本目录 `requirements.txt`。在本目录运行：

```text
python -m pip install -r requirements.txt
python build_client.py --input DNF-public-base.exe --out DNF.exe
python test_pickup.py DNF.exe
python test_slider.py DNF.exe
python test_sprites.py DNF-public-base.exe DNF.exe
```

输出 EXE SHA-256 为 `19233532acda5b6cea91e70662157a34dac769f6a5755222fe04fbd8cd41019f`。脚本保留原导入地址、资源及来源通知，在最后代码节扩展拾取绝对数量标记、滑块逻辑和 AtlasWorkers 静态导入；两个四顶点写入路径使用 SSE 等价实现。原生校验使用 Unicorn 执行实际指令，不启动游戏。

在 Visual Studio 的 **x86 Native Tools Command Prompt** 中运行 `build_atlas.bat`，生成 `AtlasWorkers101.dll` 和 `check_workers.exe`。源码及 zlib 许可证随目录提供。编译器版本与 PE 时间戳会影响 DLL 文件摘要，发布包使用 `manifest.json` 中固定的已验证成品；自建 DLL 须另记身份并完成同样验证。

```text
check_workers.exe C:\DNF\ImagePacks2\ 6 verify
check_workers.exe C:\nonexistent-atlas-test\ 1 missing
```

正常资源验证 896 个缓存项和每项两种非法输入；缺失或不同资源回落 zlib 原生解压，运行期还逐字节核对压缩数据，不能仅凭长度/尾部命中。工作线程按可用 CPU 选择 1/2/4/6，上限六线程；预解压数据约 38.8 MB，重复解压缓存上限 8 MiB。组件不修改基础 NPK。默认 FPS 120，滑块提供 60–300 数值上限及末端“不限”档（保存值 320）；旧的 301–319 非标准保存值重开时归为 300。解除等待限制不保证特定机器帧率。

## PVF

使用[PVF-CLI](https://github.com/Cec1c/PVF-CLI)提交 `6834395da196f6cc6fa1d37a72c281a391f5094d` 或兼容 schema 2 的版本：

```text
python pvf_plan.py --pvf-cli pvf --input public-base.pvf --out selected-plan.json
pvf patch --pvf public-base.pvf --plan selected-plan.json --out Script.pvf
pvf verify --pvf public-base.pvf --plan selected-plan.json --out Script.pvf
```

输出 SHA-256 为 `40c123c53e31009d7e0ff2568413221ccb4fdbb417f37387cbc730890fa51cb4`。只修改 `etc/raid/fiendwar/fiendwar.etc` 和 `etc/(r)guild.etc`；验证其他 1,126,603 条资源身份保持。个人武器奖励为 `36/5000 + (500/5000)*(36/4500) = 0.8%`。公会仅新增容量项 19 和契约项 27，不改其余货架。

## 完整配套核验

Windows 使用 `VERIFY_CLIENT.bat <已解压目录>`；Linux 使用 `python3 verify_bundle.py <已解压目录>`。安装全部六项组件后，服务端与玩家端均核对同一份 PVF。部署工具已登记累计 EXE 能力和 AtlasWorkers 依赖。已有服务显式关闭的开关和活动状态需按搭建说明迁移，不能用资源替换来绕过配置。
