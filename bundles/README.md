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
| role | ✅ 启动扫描 + 包 | ✅ `currentRoles` → 活配置快照（`internal/handler/live_config.go`） | ⬜ 待接（任务 #22） |
| skill | ✅ | ✅ `internal/einoskill` 用能力表实现 Eino 的 `skill.Backend` | ⬜ |
| agent | ✅ | ⬜ 仍按目录重扫（`agents.LoadMarkdownAgentsDir`） | ⬜ |
| tool | ✅ | ⬜ 仅 `POST /config/apply` 生效 | ⬜ |
| mcp | ⬜ 外部 MCP 本来就是热增删，缺的是逐工具授权 | — | ⬜（任务 #20） |

角色这一行是本轮改掉的：`roles/*.yaml` 以前只在 `config.Load` 里解析一次，之后由角色 API
**无锁原地改**那张 map（连 GET 里都会 `h.config.Roles = make(...)`），八个文件在没同步的情况下读它。
现在写的一侧是「写文件 → 进表 → 发布新快照」，读的一侧统一走 `currentRoles(h.config)`；
装配若忘了装活配置快照，`make wiring-check` 会直接红
（`TestAssemblyInstallsTheLiveConfigStoreAndPublishesRoles`，且这种漏接**编译得过**）。

内置的 `roles/`、`agents/`、`skills/`、`tools/` 四个目录同样被扫成单元进表，
所以"内置能力"和"后装能力"走的是同一套身份与同一张表；两侧身份一致性由
`internal/app/plugin_parity_test.go` 钉住（实测 142 个内置单元：roles 13 / agents 16 /
skills 23 / tools 90）。
