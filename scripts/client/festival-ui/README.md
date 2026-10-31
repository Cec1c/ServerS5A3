# 六期礼包窗口源码组件

提供六期礼包计数、赠礼状态、每页六档、拖动与日历贴靠、超时重试和关闭后重开逻辑，以及原生 XUI、奖励标签和配套装饰资源。服务端使用已有 `FEQ1/FES1`、opcode `0x7f02` 协议；只统计同账号成功付费购买，奖励经事务出站后自动发往购买角色邮件。

这是从 V201 累计客户端提取的静态组件。`native-contract.json` 记录原始成品身份；本目录可以独立编译检查，不能凭此重建完整 V201，也没有修改或安装现有 EXE 的入口。绝对地址适用于该记录的 x86 客户端布局；其它版本须重新核对函数、对象及调用约定。保留既有来源通知及所有其它客户端功能。

在 x86 Visual Studio Developer Command Prompt 中运行：

```bat
build.cmd C:\external-artifacts\festival-component
```

输出 `festival.obj`、`festival.payload` 与 `festival.map`。payload 是用于静态整合和离线验证的 PE 容器，没有入口，也不是运行时 DLL 插件。它不会修改游戏或连接服务器。

安装 `pefile` 和 `unicorn` 2.x 后，可用 `python verify_component.py --payload <output>/festival.payload --map <output>/festival.map --output <new-report.json>` 执行编译产物的60次点击/12次关闭重开、自动打开抑制、请求编码、响应序号及异常期次检查。原生窗口管理器、控件和网络末端是明确适配夹具，验证器不会启动游戏。

宿主整合接口：

| 接口 | 约束 |
| --- | --- |
| `FestivalBindUIThread()` | 经宿主确认的原生 UI 线程调用；核对本组件原生函数指纹后绑定，不能从网络线程调用。 |
| `FestivalTick()` | 同一 UI 线程周期调用；处理收到的状态、8秒请求超时、可见5秒/隐藏30秒查询。 |
| `ReceiveFestival()` | 只路由 S2C class0/op0x7f02，且完整正文1568字节；内部锁只搬运数据，UI线程应用。其它类别和消息保持原分派。 |
| `CalendarFromCalendar(row, calendar, caller)` | 日历点击分派；只接60001–60006，保留原始返回地址以识别自动打开路径。 |
| `FestivalFactory(id, manager)` | 仅本组件同步创建期间接管窗口工厂；其它调用回落原工厂。已有其它窗口扩展的宿主先保留各自分派。 |
| `FestivalLayout(path, document)` | 仅对应创建路径加载内嵌XUI，其它路径回落原XML加载器。 |

窗口属于 active 或 closed 集合任意一个时继续复用；只有两个集合均不持有它时清除缓存，避免反复点击占用新窗口槽位。日历重建的自动调用返回原生无弹窗哨兵；用户点击才贴靠窗口，后续拖动不被周期查询重置。

`assets/sprite_interface_privatefestival005.NPK` 仅含11帧：窗口底图、六期海报、三种档位卡片和分隔线。对应 PNG/SVG 是原装饰源，文字由游戏原生字体绘制。翻页按钮从原共享资源精确提取三帧到独立 `sprite_interface_festivalbuttons.NPK`，XUI同步改用新路径和0/1/2编号；不依赖其它活动页面资源。窗口使用原生 TileType=0外框，装饰为2倍像素、0.5绘制倍率。

将这两个NPK放入匹配客户端的ImagePacks2时，仍须通过正常累计资源发布流程。源码贡献不代表资源已安装、EXE已重新发布或游戏画面已验收。六期商品及赠礼配置见 `resources/festival`；道具本体、商品定义和其它资源由目标PVF提供。
