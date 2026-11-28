import { api, getAuthToken } from '../../lib/http';

export function newIdempotencyKey(prefix = 'op') {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
        return `${prefix}-${crypto.randomUUID()}`;
    }
    return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

const ERROR_KEYS = {
    plugin_bearer_required: ['pluginHub.errorBearerRequired', '需要 Bearer 登录凭据才能访问插件平台'],
    plugin_invalid_request: ['pluginHub.errorInvalidRequest', '请求参数不合法，请检查插件 ID（仅限小写字母、数字和连字符）、标题与仓库地址'],
    plugin_invalid_source: ['pluginHub.errorInvalidSource', '仅支持公开的 GitHub HTTPS 仓库地址及合法分支/Tag/Commit'],
    plugin_ambiguous_git_ref: ['pluginHub.errorAmbiguousRef', '分支与 Tag 同名，请填写完整引用（如 refs/tags/xxx 或 refs/heads/xxx）'],
    plugin_source_not_found: ['pluginHub.errorSourceNotFound', '未找到目标仓库或指定版本'],
    plugin_invalid_manifest: ['pluginHub.errorInvalidManifest', '插件 Manifest 格式或字段校验未通过'],
    plugin_github_rate_limited: ['pluginHub.errorRateLimited', 'GitHub API 触发限流，建议配置 plugins.egress.github_token_env 后重试'],
    plugin_source_unavailable: ['pluginHub.errorSourceUnavailable', '无法连接上游仓库，请检查网络或插件代理配置'],
    plugin_manifest_required: ['pluginHub.errorManifestRequired', '该仓库未自带 csai-plugin.yaml，请先补全 Manifest 草案'],
    plugin_manifest_source_mismatch: ['pluginHub.errorSourceMismatch', 'Manifest 中的仓库地址或版本与探测结果不一致'],
    plugin_artifact_not_approved: ['pluginHub.errorArtifactNotApproved', '当前宿主策略未批准该制品摘要，请联系管理员或核对批准策略'],
    plugin_operation_conflict: ['pluginHub.errorOperationConflict', '该插件已有正在执行的变更任务，请等待当前操作完成'],
    plugin_plan_stale: ['pluginHub.errorPlanStale', '安装计划已过期或已被消费，请重新生成锁定计划'],
    plugin_runtime_unavailable: ['pluginHub.errorRuntimeUnavailable', '插件运行调度器尚未就绪，请稍后重试'],
    plugin_artifact_unavailable: ['pluginHub.errorArtifactUnavailable', '未找到适配当前系统架构的可安装制品或镜像'],
    plugin_llm_binding_invalid: ['pluginHub.errorLLMBindingInvalid', '所选模型通道无效或与该插件声明的协议不兼容'],
    plugin_llm_gateway_unavailable: ['pluginHub.errorLLMGatewayUnavailable', 'CSAI 插件模型网关未就绪'],
    plugin_web_unavailable: ['pluginHub.errorWebUnavailable', '插件 WebUI 代理未就绪或应用尚未健康启动'],
    plugin_embed_origin_or_permission_rejected: ['pluginHub.errorEmbedRejected', '无权访问该插件工作区或请求来源校验未通过'],
    plugin_record_not_found: ['pluginHub.errorNotFound', '未找到对应的插件记录或计划已过期']
};

export function pluginErrorText(t, error, fallback) {
    const code = error?.payload?.error || (typeof error?.message === 'string' ? error.message : '');
    const entry = ERROR_KEYS[code];
    if (entry) {
        return t(entry[0], null, entry[1]);
    }
    return error?.message || fallback || t('common.loadFailed', null, '操作失败');
}

export function stateColor(state) {
    switch (String(state || '').toLowerCase()) {
        case 'healthy': return 'green';
        case 'starting':
        case 'installing':
        case 'stopping': return 'blue';
        case 'degraded':
        case 'cleanup_pending': return 'amber';
        case 'failed': return 'red';
        case 'stopped':
        case 'removed':
        default: return 'grey';
    }
}

export function stateLabel(t, state) {
    const map = {
        healthy: ['pluginHub.stateHealthy', '运行中'],
        starting: ['pluginHub.stateStarting', '启动中'],
        installing: ['pluginHub.stateInstalling', '安装中'],
        stopping: ['pluginHub.stateStopping', '停止中'],
        stopped: ['pluginHub.stateStopped', '已停止'],
        degraded: ['pluginHub.stateDegraded', '状态异常'],
        failed: ['pluginHub.stateFailed', '失败'],
        cleanup_pending: ['pluginHub.stateCleanupPending', '待清理'],
        removed: ['pluginHub.stateRemoved', '已卸载']
    };
    const item = map[String(state || '').toLowerCase()];
    return item ? t(item[0], null, item[1]) : (state || '—');
}

export function driverLabel(t, driver) {
    const map = {
        auto: ['pluginHub.driverAuto', '自动择优（Release → Git → 容器）'],
        release: ['pluginHub.driverRelease', 'Release 预编译包'],
        git: ['pluginHub.driverGit', 'Git 源码构建'],
        container: ['pluginHub.driverContainer', 'Docker / Compose 容器'],
        compose: ['pluginHub.driverCompose', 'Docker Compose']
    };
    const item = map[String(driver || '').toLowerCase()];
    return item ? t(item[0], null, item[1]) : (driver || '—');
}

export const pluginApi = {
    listCatalog: () => api.get('/plugins/catalog'),
    saveCatalog: body => api.post('/plugins/catalog', body),
    deleteCatalog: id => api.del(`/plugins/catalog/${encodeURIComponent(id)}`),
    restoreCatalog: () => api.post('/plugins/catalog/restore', {}),
    listInstalls: () => api.get('/plugins/installs'),
    getInstall: id => api.get(`/plugins/installs/${encodeURIComponent(id)}`),
    listLLMChannels: () => api.get('/plugins/llm/channels'),
    probes: (isolation = 'standard') => api.get('/plugins/runtime/probes', { isolation }),
    inspectGit: (repository, revision) => api.post('/plugins/imports/inspect', {
        repository: repository.trim(),
        revision: (revision || '').trim()
    }),
    createPlan: payload => api.post('/plugins/install-plans', payload),
    install: ({ planId, planDigest, autoBindLLM }) => api.post('/plugins/installs', {
        kind: 'install',
        plan_id: planId,
        plan_digest: planDigest,
        idempotency_key: newIdempotencyKey('install'),
        auto_bind_llm: autoBindLLM
    }),
    operate: (id, kind, extra = {}) => api.post(`/plugins/installs/${encodeURIComponent(id)}/operations`, {
        kind,
        idempotency_key: newIdempotencyKey(kind),
        ...extra
    }),
    getOperation: id => api.get(`/plugins/operations/${encodeURIComponent(id)}`),
    readLogs: (id, after = 0, limit = 100) => api.get(`/plugins/installs/${encodeURIComponent(id)}/logs`, { after, limit }),
    issueEmbedSession: id => api.post(`/plugins/installs/${encodeURIComponent(id)}/embed-sessions`, {}),
    patchLLM: (id, body) => api.patch(`/plugins/installs/${encodeURIComponent(id)}/llm`, body)
};

/**
 * 订阅插件脱敏日志 SSE 流（使用 fetch + Bearer Token，避免 EventSource 无法带 Authorization 头）。
 */
export function streamPluginLogs(installId, { after = 0, onEntry, onError, onClose } = {}) {
    const controller = new AbortController();
    const token = getAuthToken();
    const headers = { Accept: 'text/event-stream' };
    if (token) headers.Authorization = `Bearer ${token}`;
    const url = `/api/plugins/installs/${encodeURIComponent(installId)}/logs?after=${encodeURIComponent(String(after))}`;

    (async () => {
        try {
            const response = await fetch(url, { method: 'GET', headers, signal: controller.signal });
            if (response.status === 401) {
                window.dispatchEvent(new CustomEvent('csai:unauthorized'));
            }
            if (!response.ok || !response.body) {
                throw new Error(`HTTP ${response.status}`);
            }
            const reader = response.body.getReader();
            const decoder = new TextDecoder();
            let buffer = '';
            while (true) {
                const { done, value } = await reader.read();
                if (done) break;
                buffer += decoder.decode(value, { stream: true });
                let idx;
                while ((idx = buffer.indexOf('\n')) >= 0) {
                    const line = buffer.slice(0, idx).replace(/\r$/, '');
                    buffer = buffer.slice(idx + 1);
                    if (!line.startsWith('data:')) continue;
                    const raw = line.slice(5).trim();
                    if (!raw) continue;
                    try {
                        onEntry?.(JSON.parse(raw));
                    } catch {
                        // ignore malformed keepalive payload
                    }
                }
            }
            onClose?.();
        } catch (err) {
            if (err?.name === 'AbortError') {
                onClose?.();
                return;
            }
            onError?.(err);
        }
    })();

    return () => controller.abort();
}

export function starterManifestTemplate(inspection) {
    const repo = inspection?.repository || 'https://github.com/owner/repo';
    const rawName = repo.split('/').pop() || 'my-plugin';
    const id = rawName.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '') || 'my-plugin';
    const hasContainer = (inspection?.candidates || []).includes('container');
    if (hasContainer) {
        return JSON.stringify({
            apiVersion: 'plugins.csai.io/v1',
            kind: 'WebPlugin',
            metadata: {
                id,
                title: rawName,
                summary: 'Imported container WebUI plugin',
                version: '1.0.0',
                repository: repo
            },
            distribution: [
                {
                    type: 'container',
                    mode: 'docker',
                    services: [
                        {
                            name: 'web',
                            role: 'web',
                            image: `ghcr.io/${repo.replace('https://github.com/', '').toLowerCase()}:latest`,
                            containerPort: 8080,
                            resources: { memoryMiB: 512, pids: 128 }
                        }
                    ]
                }
            ],
            web: { route: 'root', entryPath: '/', healthPath: '/', allowHealthRedirect: true },
            access: { mode: 'plugin_login' },
            permissions: { persistentData: false, modelGateway: false, egress: [] }
        }, null, 2);
    }
    return JSON.stringify({
        apiVersion: 'plugins.csai.io/v1',
        kind: 'WebPlugin',
        metadata: {
            id,
            title: rawName,
            summary: 'Imported Git WebUI plugin',
            version: '1.0.0',
            repository: repo
        },
        distribution: [
            {
                type: 'git',
                url: repo,
                revision: inspection?.resolved_ref || inspection?.commit || 'main',
                dependencies: [{ name: 'git', minVersion: '2.30' }, { name: 'go', minVersion: '1.22' }],
                build: [{ program: 'go', args: ['build', '-o', 'dist/app', '.'] }],
                outputRoot: 'dist'
            }
        ],
        launch: {
            entrypoint: 'app',
            env: { PORT: 'listen_port', HOST: 'listen_host' }
        },
        web: { route: 'root', entryPath: '/', healthPath: '/', allowHealthRedirect: true },
        access: { mode: 'platform' },
        permissions: { persistentData: false, modelGateway: false, egress: [] }
    }, null, 2);
}
