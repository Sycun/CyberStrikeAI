# 仓库与分支说明 / Repository and branch guide

## 仓库职责

| 仓库 | 职责 |
| --- | --- |
| [Sycun/CyberStrikeAI-personal](https://github.com/Sycun/CyberStrikeAI-personal) | 个人维护主线。日常修复、优化和文档先在这里保存、使用与验证。 |
| [Sycun/csai-decouple-refactor](https://github.com/Sycun/csai-decouple-refactor) | 独立架构实验线，探索分层解耦与插件化；完成度以该仓库代码和验证记录为准。 |
| [Sycun/CyberStrikeAI](https://github.com/Sycun/CyberStrikeAI) | 官方贡献 fork。仅将选定改动送往上游，保留已有 PR 的来源分支。 |
| [AIPentest/CyberStrikeAI](https://github.com/AIPentest/CyberStrikeAI) | 官方上游。个人维护线与重构线均保留原始历史、许可证与版权说明。 |

个人主线与架构实验线分别演进，不把全部分支自动合入一个版本。向官方贡献时，应从官方基线组织可独立审查的改动。

## 分支状态（2026-11-03 整理时核对）

| 分支 | 当前定位 | 上游状态与后续处理 |
| --- | --- | --- |
| `main` | 个人维护默认分支 | 初始快照来自 `codex/deep-assessment`，后续日常改动以本分支为基线。 |
| `codex/deep-assessment` | 深度测评功能与官方 PR 来源 | 功能已在个人主线；[上游 PR #383](https://github.com/AIPentest/CyberStrikeAI/pull/383) 待合并，保留来源分支。 |
| `codex/issue-365-word-reports` | 单条漏洞 Markdown / Word 下载的历史分支 | [上游 PR #378](https://github.com/AIPentest/CyberStrikeAI/pull/378) 已合并，分支尖端已包含在个人主线历史中；保留存档。 |
| `codex/issue-357-proxy-pool` | 代理池功能开发分支 | 与个人主线分叉，尚未整合；[上游 PR #381](https://github.com/AIPentest/CyberStrikeAI/pull/381) 待合并，整理时存在合并冲突。继续维护时单独解决冲突并验证。 |
| `feat/local-model-context-profile` | 本地模型上下文优化分支 | 与个人主线分叉，尚未整合；[上游 PR #367](https://github.com/AIPentest/CyberStrikeAI/pull/367) 待合并，整理时存在合并冲突。继续维护时单独解决冲突并验证。 |
| `archive/local-main-20261103` | 调整个人维护流程前的本地 `main` | 历史存档，不作为日常开发或部署来源。 |

上表是带日期的状态快照，PR 后续状态以对应 GitHub 页面为准。保留分支与标签用于追溯，不据此宣称功能已集成或验收通过。

## 验证与安装

- 深度测评的使用、预算、审批、证据和恢复行为见 [DEEP_ASSESSMENTS.md](DEEP_ASSESSMENTS.md)，安全边界与回归入口见 [DEEP_ASSESSMENT_SECURITY_REVIEW.md](DEEP_ASSESSMENT_SECURITY_REVIEW.md)。
- 现有自动化与夹具验证不代表完成真实模型、真实目标的长时验收，也不构成漏洞发现率或成本优势的证明。
- 安装个人维护版时使用本仓库 README 的克隆地址。官方仓库与个人仓库的更新来源应明确选择；采用个人仓库默认分支更新时，应先评估对应提交与运行数据迁移。
- 仓库整理与默认分支调整不等于部署。已有安装的更新配置和运行状态由各自环境管理。

## Development workflow (English)

`CyberStrikeAI-personal/main` is the personal maintenance baseline. `csai-decouple-refactor` is a separate architecture experiment. `Sycun/CyberStrikeAI` remains the contribution fork for selected upstream PRs.

The dated table above distinguishes integrated work, pending branches, and historical snapshots. Proxy-pool and local-model changes require separate integration and validation. Keep existing PR branches and archival refs. Automated checks and fixtures do not establish real-model or real-target acceptance; installation and deployment sources must be selected explicitly.
