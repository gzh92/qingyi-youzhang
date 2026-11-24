# 情谊有账（qingyi-youzhang）v1.0.7

飞牛 fnOS 人情往来记账应用。纯本地、免费、无会员，数据保存在 NAS home 目录。

> 目录结构遵循飞牛官方 FPK 应用规范（developer.fnnas.com/docs/guide）。

---

## 更新历史

### v1.0.7（2026-11-24）

- 新增「年度人情报告」：统计页顶部入口，按年（可前后切换）汇总收到 / 送出 / 结余、往来亲友数、平均单笔、最热闹的月份
- 报告含月度往来柱状图、往来最多的人 TOP5（按往来总额排名）、最重的一笔、事由分布
- 一键把报告保存为长图（Canvas 本地绘制，1080×动态高度，跟随当前主题配色），可直接分享
- 金额显示精确到分并带千分位；超长人名自动省略，极端金额自动缩放字号
- 应用介绍补充年度报告入口；升级向导同步 1.0.7 说明
- 测试：年度报告专项 19/19，其余 9 套件回归全过

### v1.0.6（2026-10-28）

- 新增四套外观主题：暖纸（默认）/ 素雅白 / 青瓷 / 墨夜（深色）
- 设置页新增「外观主题」选择器，选择记忆在本地，刷新与重开保持
- 图表、图例、统计数字、主横幅、按钮全部改为主题变量驱动（含 SVG 硬编码色收敛），切换主题即时生效
- 深色主题：独立暗底横幅、提亮收入/支出功能色，`color-scheme:dark` 使原生控件与主题一致
- 测试：4 主题 × 5 视图 × 2 视口走查通过，其余 7 套件回归全过

### v1.0.5（2026-10-26）

- 修复：往来页事由筛选中自定义事由的「自定义」标签重复显示
- 修复：Safari 隐私模式等禁用存储环境下启动白屏（localStorage 安全封装）
- 修复：保存失败后的本地暂存此前从未被读取——现在应用启动时会自动补传上次未保存的数据
- 修复：空日期记录产生「年undefined月」筛选选项；空账本趋势图显示全零空图而非空态提示
- 修复：切换账本时对象页筛选状态残留
- 修复（后端）：Excel 导入临时文件泄漏（/tmp 累积）；静态服务关闭目录列举；3 个 GET 接口补方法校验
- 清理：移除死代码与冗余 CSS（ddSeq、__selValue、.help-text 等）
- 内部：全部 6 套件测试 + 生命周期 + UI 走查通过

### v1.0.4（2026-10-25）

- 对象页：新增人名搜索、状态筛选（待回礼 / 待收礼 / 两清）与卡片⇄表格视图切换（记忆选择）
- 往来页：分页条数可选（15 / 30 / 50 / 100），记忆选择，切页不跳空
- 记一笔：选中对象自动回填关系；「TA 上次」提示条可一键沿用上次事由与日期
- 修复：回填时收起联想候选面板，避免遮挡提示条按钮

### v1.0.3（2026-10-21）

- 应用介绍（manifest desc）全面更新：完整功能清单 + HTML 排版
- 升级向导文案同步精简；无功能与数据结构变更

### v1.0.1

### 修复的缺陷

| 编号 | 问题 | 修复 |
|---|---|---|
| B1 | 删除账本时不同步清理记录，产生孤儿数据（导出 Excel 账本列变空、备份带毒） | 前端删除时同步清理；后端写入时二次兜底剔除孤儿记录 |
| B2 | 保存失败被 `.catch(()=>{})` 静默吞掉，用户误以为已存 | 改为可见的保存状态指示器 + 顶部告警横幅 + 失败本地暂存 |
| B3 | `/api/state` 只校验 JSON 合法性，脏数据可整体覆写账本 | 增加 schema 校验，结构不合法直接 400 拒绝 |
| B4 | 未设口令时任何人可调 `auth/set` 锁死应用（权限提升） | 按"纯依赖网关"定位移除整套自研口令端点 |
| B5 | 导入 `accounts:[]` 的备份后 `cur().id` 抛错导致白屏 | `sanitizeState()` 统一校验（非空/孤儿清理/active 修正）+ `cur()` 兜底 |
| B6 | `colLetterIdx` 只认单字母列，AA 列解析错位 | 改为多字母列解析（A→0、Z→25、AA→26） |
| B7 | 数据损坏时静默当作空账本，用户可能重录覆盖可抢救数据 | 服务端明确返回 `data corrupted`；前端弹出常驻告警且不覆盖写入 |
| B8 | 保存无节流，逐字符输入触发大量 PUT | 加入 400ms 防抖 + `beforeunload`/`visibilitychange` 强制 flush |
| B9 | 访问 token 永久有效、无法吊销 | 随自研口令一并移除，改用网关登录态 |
| B10 | 导入金额无校验，极端值拉爆统计比例尺 | 校验有限正数 + 上限 1e9 + 保留 2 位小数 |
| B11 | sheet 匹配含死代码，多 sheet 时行为不确定 | 显式只取第一个 worksheet |
| B12 | 导出 xlsx 无样式、无列宽、金额无格式 | 表头加粗底色、金额千分位、列宽、冻结首行、合计行 |
| B13 | `api()` 依赖 `location.pathname`，子路径下拼接错位 | 改用固定网关前缀 |
| B14 | 导入无行数上限 | 上限 50000 行，超出截断并提示 |
| B15 | `curRecs()` 在空账本时索引异常 | `cur()` 兜底返回空对象 |

### 新增能力

- **导出到 NAS**：新增 `/api/export/to-nas`，配合 `config/resource` 声明的共享目录 `qingyi-youzhang/exports`，把 Excel 直接落到 NAS，用户可在文件管理器访问。
- **自动备份轮转**：写入前保留最近 3 份 `data.json.bak.N`，新增 `/api/backups` 查询。写入采用"临时文件 + rename"原子操作，避免断电留下半截文件。
- **数据版本号**：`data.json` 增加 `version` 字段（当前为 1），便于后续结构迁移。
- **撤销删除**：删除记录后 5 秒内可撤销。

### 合规与体验

- `manifest` 补齐 `os_min_version=1.2.0`、显式 `ctl_stop=true`，字段格式对齐官方示例。
- 统一"端口 / 网关"口径：明确为统一网关 + Unix Socket 模型，清除 `install_callback` 中过时的"固定端口 6189"注释。
- 移除飞书 CDN 外链字体，改用系统字体栈 —— 内网/离线环境排版稳定，且与"数据不外流"定位一致。
- 安装/配置/升级/卸载向导文案如实反映数据落点与备份处置。

---

## 一、目录结构（开发源码）

```
qingyi-youzhang/
├── 情谊有账.html         # 前端单一源（构建时同步到 app/www/index.html）
├── x86/                  # x86 平台包源
├── arm/                  # ARM 平台包源
│   ├── manifest          # 应用身份信息（key=value，必需）
│   ├── cmd/              # 生命周期脚本（必需，安装后带执行权限）
│   │   ├── main                  # 启停与状态检查（start/stop/status）
│   │   ├── install_init         # 安装前
│   │   ├── install_callback     # 安装后
│   │   ├── uninstall_init       # 卸载前
│   │   ├── uninstall_callback   # 卸载后
│   │   ├── upgrade_init         # 升级前
│   │   ├── upgrade_callback     # 升级后
│   │   ├── config_init          # 配置前
│   │   └── config_callback      # 配置后
│   ├── config/           # 权限与资源声明
│   │   ├── privilege       # 运行身份（run-as=package）
│   │   └── resource        # 共享目录 data-share
│   ├── wizard/           # 安装/卸载/升级/配置 向导（JSON）
│   ├── app/              # 应用本体 → 安装后映射到 target（TRIM_APPDEST）
│   │   ├── server/         # 后端（Go 静态服务 + API）
│   │   │   ├── main.go           # 后端源码
│   │   │   └── qingyi-youzhang   # 编译产物（静态二进制）
│   │   ├── www/            # 前端静态资源（index.html + favicon）
│   │   └── ui/             # 桌面入口配置（desktop_uidir=ui）
│   │       ├── config            # 入口 JSON（.url 下的 key 与 desktop_applaunchname 一致）
│   │       └── images/           # icon-64.png / icon-256.png
│   ├── ICON.PNG          # 应用商店小图标 64x64
│   └── ICON_256.PNG      # 应用商店大图标 256x256
├── build.sh              # 一键编译 + 打包脚本
└── README.md
```

---

## 二、目录 → 安装后环境变量映射

| 开发目录 | 安装后路径 | 环境变量 | 说明 |
|---|---|---|---|
| `app/` | `/vol[x]/@appcenter/<appname>` | `TRIM_APPDEST` | 可执行文件与静态资源根目录 |
| `app/server/` | `<app>/server/` | — | 后端二进制位置（cmd/main 探测） |
| `app/www/` | `<app>/www/` | — | 前端页面根目录（后端 WWW 指向） |
| `app/ui/` | `<app>/ui/` | — | 桌面入口配置（desktop_uidir） |
| `cmd/` | `/var/apps/<appname>/cmd/` | — | 生命周期脚本 |
| `home/`（数据） | `/vol[x]/@apphome/<appname>` | `TRIM_PKGHOME` | 用户数据：`data.json` 及 `data.json.bak.N` |
| `var/`（运行） | `/vol[x]/@appdata/<appname>` | `TRIM_PKGVAR` | 运行时日志 `info.log`、`app.pid` |
| `etc/`（配置） | `/vol[x]/@appconf/<appname>` | `TRIM_PKGETC` | 静态配置 |
| `shares/`（共享） | 共享目录 | `TRIM_DATA_SHARE_PATHS` | 导出目标：`qingyi-youzhang/exports` |
| `manifest` | `/var/apps/<appname>/manifest` | — | 应用元信息 |

### 关键逻辑点

- **网络入口**：**统一网关模式**。`cmd/main` 以后端监听 Unix Socket `${TRIM_APPDEST}/app.sock` 启动；飞牛统一网关校验 NAS 登录态后，把 `/app/qingyi-youzhang` 路径请求转发到该 Socket。`protocol`/`port` 由网关忽略。TCP 6189 仅作为 `SOCKET` 为空时的兜底。
- **鉴权**：**完全由网关负责**。网关校验登录态并转发 `X-Trim-Userid` / `X-Trim-Username` / `X-Trim-Isadmin`。本应用不再实现自研口令，避免与网关职责重叠及越权设置风险。
- **数据安全**：所有写入先经 schema 校验（拒绝脏数据、自动剔除孤儿记录），再以"临时文件 + rename"原子落盘，并轮转保留最近 3 份备份。
- **桌面入口**：`manifest.desktop_applaunchname`（`qingyi-youzhang.Application`）与 `app/ui/config` 中 `.url` 下的 key 完全一致。
- **前端 API**：使用固定网关前缀 `/app/qingyi-youzhang/`，不依赖当前页面路径。
- **数据存储**：纯服务端写入 `$TRIM_PKGHOME/data.json`，不依赖浏览器 localStorage。
- **安装确认**：`wizard/install` 含「我已了解，同意安装」开关，`install_callback` 校验 `wizard_ack`。

---

## 三、生命周期脚本约定

- 遵循 fnOS 规范：成功 `exit 0`，失败 `exit 1`，未运行 `status` 返回 `exit 3`。
- 统一用 `$TRIM_APPDEST` / `$TRIM_PKGHOME` 等变量拼路径，不硬编码 `/var/apps/...`。
- 日志统一写入 `$TRIM_PKGVAR/info.log`；失败提示写入 `$TRIM_TEMP_LOGFILE`。
- 安装/卸载/升级/配置向导文案与真实行为保持一致。

---

## 四、一键打包

```
./build.sh x86     # 编译 x86 后端并打 qingyi-youzhang-x86-1.0.4.fpk
./build.sh arm     # 编译 arm 后端并打 qingyi-youzhang-arm-1.0.4.fpk
```

构建流程含 5 步校验：前端单一源同步 → manifest 必需字段断言（含架构一致性）→ JSON 配置合法性 → 编译 → 打包与结构自检（嵌套残留、`main.go` 混入、外链 CDN 残留）。

产物：`qingyi-youzhang-<arch>-1.0.4.fpk`（可直接导入飞牛应用中心安装）。

> 注意：x86 与 arm 为两个独立包，分别发到对应架构设备。`manifest.platform` 已按包标注，安装时应用中心会做架构校验。

---

## 五、主要功能

多账本、人情收支（收/送）、对象往来、按事由/时间/姓名查找、统计、JSON 备份导出/导入、Excel(.xlsx) 导入/导出、导出到 NAS 共享目录、自动备份轮转、删除撤销。
