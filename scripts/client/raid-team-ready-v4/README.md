# 团本奖励切页 readiness v4：离线可复建源码候选

此目录提供固定 v154 客户端的独立离线构建与有界原生验证工具。v4 将 Anton、Luke、Fiend War 共用奖励窗口的“小队页完成→团队页”转换，延后到当前结果资格与团队数据就绪；保留原生 Luke 无团队奖励的 skip→完成路径。

这是源码候选及离线验证结果，未发布、部署或完成 GUI 验收。工具没有安装、网络同步或进程访问功能。服务端资产提交、随机奖励与自动期限不由本补丁改变。客户端小队页本地倒计时与服务端自动授奖期限不同，不应据此把服务端期限盲改为2秒或15秒。

## 固定输入和输出

| 项目 | 值 |
|---|---|
| 公开源码基线 | `49d50d3c8f09c30c19a1da573652c71030069900` |
| v4输入EXE SHA256 | `9327087d147576314dd95c6c0b0a638b1525bca48734658fe4f9b97dc33f82ea` |
| 输入长度 | 111865856字节 |
| v4输出EXE SHA256 | `0762198fdc0c56a35305325c52ef876a05cc75c8fa849bb6a816deebba352bff` |
| 输出长度 | 111874048字节 |
| 新增代码/状态 | 1017字节代码，44字节状态；两个独立4096字节RX/RW区段 |
| 本目录验证器预期 | 95项通过，精确PE修改范围检查通过 |

必须自行提供合法取得且摘要完全一致的输入EXE。输入、输出二进制和运行回执均不随源码收录。构建器拒绝不匹配图像，不支持将其他版本强制视为v154。

## 环境与复建

当前入口要求 **Windows、64位Python 3.12、可用物理内存至少3 GiB**；内存检查使用Windows API，不声称已经支持其他系统。验证依赖Capstone和Unicorn。本次复建使用Python 3.12.14、Capstone 5.0.7、Unicorn 2.1.4。可正常安装，或由环境`PYTHONPATH`提供相同依赖；源码没有机器绝对路径或运行时`sys.path`注入。

以下命令从仓库根目录执行，将固定输入置于`runtime/client-input/v154.exe`，选择一个**不存在的新输出目录**：

```text
python -m pip install capstone==5.0.7 unicorn==2.1.4
python -B scripts/client/raid-team-ready-v4/build_readiness_v4.py --input runtime/client-input/v154.exe --output runtime/client-readiness-v4
python -B scripts/client/raid-team-ready-v4/verify_readiness_v4.py --input runtime/client-input/v154.exe --candidate-dir runtime/client-readiness-v4
```

不要使用`-O`、`-OO`或启用`PYTHONOPTIMIZE`：本工具的固定输入、边界与回归检查依赖assert。构建产生`candidate.exe`和`build.json`；验证产生`verify.json`，其中应有`passed: 95`及上述输出SHA256。验证回执已存在时工具拒绝覆盖；复验使用另一新输出目录。环境可用内存读数随机器变化，JSON回执不要求逐字节一致；EXE要求精确摘要一致。

## 七个文件与依赖关系

| 文件 | v4所需用途 |
|---|---|
| `pe_support.py` | PE32布局、地址换算、SHA256和checksum |
| `build_readiness.py` | v1基础Code汇编器、内存保护、公共原生锚点 |
| `build_readiness_v2.py` | 追加601解析/资格锚点 |
| `build_readiness_v4.py` | 当前六hook及共享模式构建入口 |
| `verify_readiness.py` | 原生执行基础harness及显式UI/STL叶夹具 |
| `verify_readiness_v2.py` | 601真实parser/getter及夹具扩展 |
| `verify_readiness_v4.py` | 当前95项检查入口 |

v1/v2保留为v4导入支持，导入不会执行它们的main。它们各自的历史构建入口仍绑定旧输入`8ab4297d1e3ebe2dc9d71801ebae210cb3ec673dbcdf707b35a3d5c8faf6f790`，不是本目录推荐运行入口；不要先构建v1/v2再叠加v4。v4直接读取上表固定输入。

## v4行为边界

六个hook分别为601解析尾`0201DCBF`、600完整卡循环尾`0202FCC8`、outer state3→4调用`0192F6ED`、587 state2模型清理尾`02001517`、587 state0模型清理尾`020012D7`、原生manager退出`01A64B8D`。

- 捕获结果时核对当前manager、key、raid及native mode0/1/2；与模型key1/2/3对应。
- 当前有效团本结果未知或身份不匹配时暂留outer3；denied/invalid沿原逻辑通行；eligible需本次结果之后完整非空kind3及category2首行。
- 原生skip和state5通行；empty kind3不冒充团队ready。
- state0/state2模型清理和当前manager退出撤销资格；重复587开窗通知不自行撤销已收到结果/团队数据。未更改原生开窗调用。
- raw result phase被保存，当前匹配依赖上述生命周期清理和身份，并非独立phase比较。未声称覆盖所有断线重连或对象复用路径。

## 验证结果及夹具边界

95项检查覆盖固定PE范围、寄存器/栈恢复、真实601 parser、结果资格、mode/model身份、完整外层卡循环后的ready、新阶段清理、退出、缺首行以及skip透传。早期开窗前提、UI/STL容器和部分lookup为夹具；部分生命周期测试直接从hook运行到continuation，不能称为完整587入站或renderer测试。

另有同一固定候选上的5条外部组合执行证据：本地type1/type2奖励各经真实empty3归属判定及skip到state5；foreign owner拒绝；原版小队本地计时结束后提前初始化team；v4保持outer3直到迟到kind3到达、party收尾后才初始化，重复kind3不再次初始化。

这5条**不计入本目录95项，也不声称本目录验证器会重新执行它们**。其关键边界为：计时elapsed注入、非空item payload/D960存储及UI行绑定为夹具；empty3与本地ownership/getter、page tick/setter、outer tick/setter和v4 hook执行真实代码；团队初始化`F17C00`为计数leaf，“一次”表示入口调用一次，不是完整控件构造、图标加载或实际渲染。组合回执不包含在这份七文件源码包中。

仍需实际客户端确认首次team显示/翻牌、阶段与重连生命周期、无资格旁观者完成行为。此工具不会修复资产规则，也不保证已进入旧空团队页的窗口能被单独587 state5恢复。
