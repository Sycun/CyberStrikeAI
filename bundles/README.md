# bundles/ — 按角色打包的能力包

一个 bundle 就是"一个角色所需的一切"：角色定义、它的子代理、它的技能、可选的工具配方与 MCP 声明，
放在同一个目录里，一次安装、一次卸载。实现在 `internal/plugin`。

## 目录形状

```
bundles/<id>/
  bundle.yaml                    # 清单（必需）
  roles/<name>.yaml              # kind: role
  agents/<name>.md               # kind: agent
  skills/<name>/SKILL.md         # kind: skill（目录，必须含 SKILL.md）
  tools/<name>.yaml              # kind: tool
  mcp/<name>.yaml                # kind: mcp
```

## 清单

```yaml
id: mobile-app-security          # 必需，不含路径分隔符
name: 移动端安全测试角色包
version: 1.0.0                   # 必需：没有版本就无法升级或回滚
description: ...
units:
  - kind: role                   # role | agent | skill | tool | mcp，仅此五类
    path: roles/移动端安全测试.yaml   # 相对本目录；不允许 `..`，不允许绝对路径
    name: 移动端安全测试            # 可省略：role/tool/agent 取去扩展名的文件名，skill 取目录名
```

清单里没有、也不允许有 `dest` 之类的目标路径：能力落在哪儿由 `kind` 决定，
所以一个包不可能通过写清单去覆盖它管不着的文件。

## 身份与冲突

单元身份是 `<kind>/<name>`，全局唯一。安装时的规则是**拒绝并指名道姓**，不是覆盖：

- 包 A 要装 `role/CTF`，而 `roles/CTF.yaml`（内置目录扫出来的）已经在表里 → 拒绝。
  先卸载/停用内置那条，包才能顶上；反方向（目录扫描覆盖已安装的包）同样拒绝。
- 同一个 `id` 再装一次是**升级**：这个包自己上一版声明、这一版没声明的单元会消失，
  别的包的单元一个都不动。
- 卸载只把单元从表里摘掉，**不删任何文件**（源文件本来就在 `bundles/<id>/` 里）。

每条规则都有测试，`internal/plugin/table_test.go`。

## 生效时机

装完即生效，不需要重启：读侧拿到的是一份不可变快照（原子指针切换），
写侧只有一把串行化的锁。并发的读与换不会互相撕开——这条性质不是论证出来的，
是把快照改成原地写之后 `TestConcurrentReadersNeverTear` 在 `-race` 下当场报出来的。

各 kind 离"装完就被服务"还差多远，逐个说清（不写"已全部插件化"这种话）：

| kind | 单元进表 | 运行路径读表 | 一键安装 API |
|---|---|---|---|
| role | ✅ 启动扫描 + 包 | ✅ `currentRoles` → 活配置快照（`internal/handler/live_config.go`） | ✅ |
| skill | ✅ | ✅ `internal/einoskill` 用能力表实现 Eino 的 `skill.Backend` | ✅ |
| agent | ✅ | ✅ 运行路径与管理台都走表（`agents.LoadMarkdownAgents`） | ✅ |
| tool | ✅ | ✅ 配方清单由表驱动重建（`ToolLayer.Rebuild`，与 `POST /config/apply` 同一条序列） | ✅ 装完即重建 |
| mcp | ✅ 每个远端工具一个身份（`LayerRemote`，按服务器成组装卸） | ✅ 授权按工具身份判定，判定不到再回到命名空间策略 | — |

## 外部 MCP 工具也有身份

远端服务器本来就支持热增删（`/api/external-mcp/*`），缺的是**身份**：所有远端工具过去一起过
一条 `mcp:external:execute`，规则无法点名某个工具，审批与审计也只能写到"外部 MCP"这一层。

现在 `ExternalMCPManager` 每次拿到某台服务器的真实工具清单，就在
`capability.LayerRemote` 里按服务器成组登记/替换/摘除：

- 身份 `remote.<server>.<tool>`，Name 就是执行器看到的线名 `<server>::<tool>`；
- 权限、runtime 与 **global scope 下限**沿用命名空间那条策略，所以这一步**不改变谁能调用什么**，
  只是让"某个远端工具"变成可被规则、审批与审计点名的对象；
- 一台服务器重连或下线只动它自己那一组，别的服务器与内置策略一律不变；
- 清单还没到位（服务器没连上、刷新在途）时判定回到命名空间策略——那是**一条登记在册的策略**，
  不是"名字不认识就放过"。

装配漏接观察者的话 `make wiring-check` 会红（`SetToolInventoryObserver` 必须恰好调用一次）——
漏接的表现不是报错，而是所有远端工具悄悄退回命名空间判定，所以只能靠门禁。

## 接口

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/api/plugins` | 已装包 + 独立单元 + `generation` + `drift` + 每单元的 `served` |
| POST | `/api/plugins/install` | `{"bundle":"<包名>"}` → 装入并立刻生效 |
| DELETE | `/api/plugins/bundles/{id}` | 卸载（只摘表，不删文件） |
| POST | `/api/plugins/units/{kind}/{name}/enabled` | 启停单个单元 |
| DELETE | `/api/plugins/units/{kind}/{name}` | 摘掉一个**扫描得到**的单元（包拥有的会 409） |

`identity` 里带斜杠（`role/CTF`），所以路由拆成 `:kind/:name` 两段 —— 单段会被 gin 在匹配前
就解掉转义而命中不到。

两点不装作已完成：

- 安装**只能**从 `<configDir>/bundles` 里挑，越界路径（`../`、绝对路径）一律 400。
- `served:false` 是**响应里的字段**，不是文档里的脚注。现在还写着 `false` 的只有 mcp 一类：
  远端服务器的活路径是自己的管理器，能力表只登记声明。让"装好了"读起来像"能用了"，
  就是这一层存在的理由的反面。

## tool 配方怎么接上表的

配方清单原本只在两个地方产生：`config.Load` 扫一次 `tools_dir`，`POST /config/apply` 再扫一次。
现在 `ToolLayer.Rebuild()`（`ConfigHandler.Tools` 持有的协作者，见 `internal/handler/tool_table.go`）是唯一的重建入口，它按顺序做三件事，与 apply 原本做的**完全同一套**：

1. 从表里读配方**路径**（表里没有 tool 单元时回到目录扫描——漏跑启动扫描不该把 90 个内置配方清空）；
2. 重建能力注册表的 recipe 层（没有 `capability:` 清单的配方照旧被拒，调用时 fail-closed）；
3. `ClearTools()` 后重新注册全部工具面（配方 + 每个内置 registrar）。

一键安装/卸载/启停只有**涉及 tool 单元**时才触发它——`ClearTools` 会清掉整个工具面，
纯角色包不该付这个代价。整段由 `toolLayerMu` 串行：两个重建重叠时，一方的 `ClearTools`
可能落进另一方"清空后还没重注册"的窗口。

两条不会被说清楚的规则，都用测试钉住了：

- **开关只收窄**：运行期状态是 `文件 enabled ∧ 表 enabled`。表单元的 `Enabled` 默认是 true，
  如果把它当覆盖值，`enabled: false` 的内置配方会被悄悄打开。
- **运行期开关不写回文件**：`PUT /config` 会把每个工具的 `enabled` 落到它自己的 yaml 里
  （既有行为）。若这次落盘的是由表带来的 false，配方就被永久钉死——之后再打开表开关也起不来。
  所以那条写回循环跳过"表说停用"的工具。

漏接的失败模式是**静默**的：装配若不把 `configHandler` 传给 `NewPluginHandler`，包里的配方会被登记、
被列出来、被回答"已安装"，然后永远不能执行。构造函数把它做成必填参数，装配处再传错由
`make wiring-check` 的 AST 断言兜住（第 4 个实参必须是 `configHandler.Tools`，传别的、传 nil 都算漏接）。
启动扫描若漏掉 `KindTool` 这一行，装任何一个包就会把整批内置配方换成包里那一个 ——
这条由 `TestBuiltInCapabilityScanCoversEveryServedKind` 兜住，同时要求新加的 kind 必须被扫描或显式豁免。
"表驱动 vs 目录驱动"对内置 90 个配方逐条比对（名字、顺序、启用位全等），由
`TestToolLayerFromTableMatchesDirectoryLoad` 钉住。

角色这一行是本轮改掉的：`roles/*.yaml` 以前只在 `config.Load` 里解析一次，之后由角色 API
**无锁原地改**那张 map（连 GET 里都会 `h.config.Roles = make(...)`），八个文件在没同步的情况下读它。
现在写的一侧是「写文件 → 进表 → 发布新快照」，读的一侧统一走 `currentRoles(h.config)`；
装配若忘了装活配置快照，`make wiring-check` 会直接红
（`TestAssemblyInstallsTheLiveConfigStoreAndPublishesRoles`，且这种漏接**编译得过**）。

skill 一行的"读表"含两层：运行路径（`internal/einoskill` 换掉了厂商那个只认一个 `BaseDir`
的 backend）**和管理台列表**（`GET /api/skills`、详情、文件读写都按表解析目录）。
两层必须一起改：只改运行路径会出现"包里的 skill 被 Agent 用着、列表里看不见"，
这一条是在真实跑起来的服务上实测到的（23 → 装包 24 → 卸载 23），不是推演出来的。
写路径遇到包拥有的 skill 返回 409 并指名是哪个包，**不会**在内置 skills 目录里悄悄落一份同名副本。

agent 一行的两层同样一起改了：`agents.LoadMarkdownAgentPaths` 与目录扫描共用同一个解析器
（对内置 16 个 `.md` 逐个比对，路径驱动与目录驱动**逐字节相同**），运行路径与管理台都从表里的
路径读；表里一个 agent 单元都没有时**回到目录扫描**，因为漏跑启动扫描是可修的装配问题，
而"每次运行都没有子代理"是不可修的。
这一行是**真实跑起来的服务**指出来的：`GET /api/plugins` 报
`[('role', True), ('agent', False), ('skill', True)]`——运行路径已经搬到表上了，
`served` 字段还留着搬迁时的说明。测试当时抓不到它，因为夹具包里没有 agent 单元；
现在 `reporting-pack` 带一个 `agents/report-analyst.md`，装完断言 `served=true`、
卸载后断言该单元从表里消失（先断"在"再断"不在"，否则后半句可能一直在空过）。

内置的 `roles/`、`agents/`、`skills/`、`tools/` 四个目录同样被扫成单元进表，
所以"内置能力"和"后装能力"走的是同一套身份与同一张表；两侧身份一致性由
`internal/app/plugin_parity_test.go` 钉住（实测 142 个内置单元：roles 13 / agents 16 /
skills 23 / tools 90）。
