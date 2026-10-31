# 徽章合成 Shift＋右键快捷加入

状态：用户已确认V198的Shift＋右键加入及实际合成正常。本次修正操作说明的相对坐标定位，其实际绘制仍待游戏确认。一键重铸不属于本项。早期Shift＋左键候选触发聊天链接；首个组合候选按错背包来源类型，右键仍走镶嵌。此次按实际窗口来源1/背包子类型1修正，并按用户新要求改为Shift＋右键。从已验收累计基线重新生成，不叠加失败候选。

## 操作

- 徽章合成窗口打开时，Shift＋右键点击徽章加入首个空位；原拖动继续可用。合成槽下方常驻一行短说明：“Shift+右键加入，拖动也可”，选择后显示原生计算的数量上限。点击“合成”才提交。
- 原生同品级检查和配置上限保留：非白金最多5个、白金最多2个，满格不替换最后一项。不按Shift、窗口关闭、其他来源、无效槽或非徽章保留原右键分派；忙碌时不插入。仅检查Shift键，不挂接左键聊天链接，不修改合成概率。

## 构建

本项独立于一键重铸；仅运行 `scripts/client/emblem-right-select/build_candidate.py` 即可生成合成快捷加入候选。默认精确官方V198输入SHA256为 `54303584115679b9e6964c8c43c77ddf4005eb5fd2548746c6eb95a0ba23f65d`，112123904字节。累计交付须使用经审查的独立精确profile，保留原有通知及累计修复。

右键生成器只挂接背包原生右键使用事件（Shift按下且来源与实际背包匹配时）、合成提示文字和提示尺寸；保留左键拖动调用、聊天物品链接、原合成确认及原生网络发送。通过原生 action=0x24 校验后调用既有插入和刷新函数，不制造拖动对象。原生合成报文仍是 `u8数量 + N*(u32物品ID + u16槽位)`，服务器按原事务校验并结算。

```sh
python3 scripts/client/emblem-right-select/build_candidate.py --input /absolute/path/DNF.exe --profile /private/exact-profile.json
python3 scripts/client/emblem-right-select/build_candidate.py --input /absolute/path/DNF.exe --profile /private/exact-profile.json --output /absolute/path/DNF.candidate.exe
python3 -m unittest discover -s scripts/client/emblem-right-select -p test_candidate.py -v
```

profile 使用 `inputSha256`、`inputSize`、`outputSha256`、`outputSize`；写出须钉住两端身份，输出须为新文件。拒绝未知原指令、非法PE、重复补丁及已挂接旧Shift/确认回调的输入。只追加RX节并修改列明位置，保留其它字节、来源通知及累计修复。私人 profile、EXE、进程与反编译证据不入库。

## 验证范围

合成PE测试核对布局/校验和、精确改动范围和拒绝覆盖；实际累计EXE的隔离x86检查执行右键分派、原生物品类型校验、插入品级/上限、提示布局及完整合成发送。UI对象、RTTI、STL、资源编码及传输为显式适配，不代表真实游戏交互；原始规则指令不以测试实现代替。


游戏内验收：Shift＋右键加入/普通右键镶嵌/拖动/同品级/白金数量/点击合成的消耗产出；窗口关闭时正常物品右键及聊天输入保持。验收前不合入新仓库主线，不对外分发；客户端安装与正式发布按各自授权执行。

提示定位使用原生相对位置入口，保留父容器坐标；旧绝对入口把目标(8,99)解释为屏幕坐标，从而移到窗口外。原生坐标变换回归使用不同窗口原点检查实际相对/绝对坐标，不再用简单写字段的适配器证明可见性。实际绘制仍需游戏验收。
