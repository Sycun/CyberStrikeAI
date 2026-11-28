/**
 * 知识库设置（knowledge 段）：启用开关、嵌入模型（Embedding）配置与连通性测试、
 * 检索参数（Top-K / 阈值 / MultiQuery / Rerank）以及索引构建分块限流配置。
 */
import { useCallback, useEffect, useId, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Banner, Button, Collapse, Input, Select, Space, Switch, Tag, Typography } from '@douyinfe/semi-ui';
import { api } from '../../../lib/http';
import { useAuth } from '../../../lib/auth';
import { useT } from '../../../lib/i18n';
import { modelChannelName } from '../../../lib/model-channel';
import { useResource } from '../../../components/hooks';
import { ErrorBanner, KeyValueList, SectionCard, toastSuccess } from '../../../components/ui';
import { Hint, LoadingBlock, PermissionBanner } from './common';

const { Text } = Typography;

const EMPTY_PROBE = { key: '', payload: null, error: null };

function strNum(val) {
    return val != null && val !== '' && Number(val) !== 0 ? String(val) : (val === 0 ? '0' : '');
}

function knowledgeValues(knowledge = {}) {
    const emb = knowledge.embedding || {};
    const ret = knowledge.retrieval || {};
    const mq = ret.multi_query || {};
    const rerank = ret.rerank || {};
    const post = ret.post_retrieve || {};
    const idx = knowledge.indexing || {};
    return {
        enabled: Boolean(knowledge.enabled),
        base_path: knowledge.base_path || 'knowledge_base',
        embedding_provider: emb.provider || 'openai',
        embedding_model: emb.model || '',
        embedding_base_url: emb.base_url || '',
        embedding_api_key: emb.api_key || '',
        top_k: ret.top_k > 0 ? String(ret.top_k) : '5',
        similarity_threshold: ret.similarity_threshold > 0 ? String(ret.similarity_threshold) : '0.4',
        sub_index_filter: ret.sub_index_filter || '',
        multi_query_max_queries: mq.max_queries > 0 ? String(mq.max_queries) : '',
        rerank_provider: rerank.provider || '',
        rerank_model: rerank.model || '',
        rerank_base_url: rerank.base_url || '',
        rerank_api_key: rerank.api_key || '',
        prefetch_top_k: strNum(post.prefetch_top_k),
        max_context_chars: strNum(post.max_context_chars),
        max_context_tokens: strNum(post.max_context_tokens),
        chunk_strategy: idx.chunk_strategy || '',
        chunk_size: idx.chunk_size > 0 ? String(idx.chunk_size) : '',
        chunk_overlap: idx.chunk_overlap > 0 ? String(idx.chunk_overlap) : '',
        max_chunks_per_item: strNum(idx.max_chunks_per_item),
        batch_size: idx.batch_size > 0 ? String(idx.batch_size) : '',
        request_timeout_seconds: idx.request_timeout_seconds > 0 ? String(idx.request_timeout_seconds) : '',
        max_rpm: strNum(idx.max_rpm),
        rate_limit_delay_ms: strNum(idx.rate_limit_delay_ms),
        max_retries: idx.max_retries > 0 ? String(idx.max_retries) : '',
        retry_delay_ms: idx.retry_delay_ms > 0 ? String(idx.retry_delay_ms) : '',
        prefer_source_file: Boolean(idx.prefer_source_file),
        sub_indexes: Array.isArray(idx.sub_indexes) ? idx.sub_indexes.join(', ') : ''
    };
}

function parseNonNegInt(raw, labelText) {
    const trimmed = String(raw ?? '').trim();
    if (!trimmed) return 0;
    if (!/^\d+$/.test(trimmed)) {
        throw new Error(`${labelText}请输入非负整数`);
    }
    const num = Number(trimmed);
    if (!Number.isSafeInteger(num) || num < 0) {
        throw new Error(`${labelText}请输入有效的非负整数`);
    }
    return num;
}

function resolveDefaultChannel(config) {
    const channels = config?.ai?.channels || {};
    const defaultId = config?.ai?.default_channel || '';
    if (defaultId && channels[defaultId]) {
        const ch = channels[defaultId];
        return {
            channelId: defaultId,
            displayName: modelChannelName(defaultId, ch),
            openai: {
                provider: ch.provider || 'openai_compatible',
                base_url: ch.base_url || '',
                api_key: ch.api_key || '',
                model: ch.model || ''
            }
        };
    }
    const oa = config?.openai || {};
    return {
        channelId: '',
        displayName: oa.model || '',
        openai: {
            provider: oa.provider || 'openai_compatible',
            base_url: oa.base_url || '',
            api_key: oa.api_key || '',
            model: oa.model || ''
        }
    };
}

export default function KnowledgeSection() {
    const t = useT();
    const navigate = useNavigate();
    const { hasPermission } = useAuth();
    const canRead = hasPermission('config:read');
    const canWrite = hasPermission('config:write');
    const loader = useCallback(({ signal } = {}) => api.get('/config', { include_tools: 0 }, { signal }), []);
    const configRes = useResource(loader, [canRead], { skip: !canRead });

    const providerLabelId = useId();
    const rerankProviderId = useId();
    const strategyLabelId = useId();
    const pickerLabelId = useId();

    const baseline = useMemo(() => knowledgeValues(configRes.data?.knowledge), [configRes.data?.knowledge]);
    const baselineSig = JSON.stringify(baseline);
    const [draft, setDraft] = useState({});
    const form = { ...baseline, ...draft };
    const dirty = Object.keys(draft).length > 0;

    const [busy, setBusy] = useState('');
    const [saving, setSaving] = useState(false);
    const [saveError, setSaveError] = useState(null);
    const [applyPending, setApplyPending] = useState(false);
    const [probe, setProbe] = useState(EMPTY_PROBE);

    useEffect(() => {
        setDraft({});
        setProbe(EMPTY_PROBE);
    }, [baselineSig]);

    if (!canRead) {
        return <PermissionBanner permission="config:read" label={t('settings.nav.knowledge', null, '读取知识库配置')} />;
    }
    if (configRes.loading && !configRes.data) return <LoadingBlock rows={6} />;

    const locked = !canWrite || saving || Boolean(busy);
    const fallbackChannel = resolveDefaultChannel(configRes.data);
    const mainIsDeepSeekOfficial = /api\.deepseek\.com/i.test(fallbackChannel.openai.base_url || '');
    const mainIsClaude = ['claude', 'anthropic'].includes(String(fallbackChannel.openai.provider || '').toLowerCase());
    const needsSeparateEmbeddingNotice = !form.embedding_base_url.trim() && (mainIsDeepSeekOfficial || mainIsClaude);

    const update = (field, value) => {
        if (probe.key !== 'list' || ['embedding_provider', 'embedding_base_url', 'embedding_api_key'].includes(field)) {
            setProbe(EMPTY_PROBE);
        }
        if (!applyPending) setSaveError(null);
        setDraft(current => {
            const next = { ...current };
            if (value === baseline[field]) delete next[field];
            else next[field] = value;
            return next;
        });
    };

    const discard = () => {
        setDraft({});
        setProbe(EMPTY_PROBE);
        if (!applyPending) setSaveError(null);
    };

    const buildKnowledgePayload = () => {
        const topK = parseNonNegInt(form.top_k, t('settingsBasic.topK', null, 'Top-K 结果数量')) || 5;
        const rawThreshold = String(form.similarity_threshold ?? '').trim();
        const threshold = rawThreshold === '' ? 0.4 : Number(rawThreshold);
        if (!Number.isFinite(threshold) || threshold < 0 || threshold > 1) {
            throw new Error('相似度阈值请输入 0 到 1 之间的数值（例如 0.4）');
        }
        const maxQueries = parseNonNegInt(form.multi_query_max_queries, t('settingsBasic.multiQueryMaxQueries', null, 'MultiQuery 改写变体上限'));
        if (maxQueries > 8) {
            throw new Error('MultiQuery 改写变体上限最大为 8（留空使用默认 4，填 1 跳过改写）');
        }
        const subIndexes = String(form.sub_indexes || '')
            .split(/[,\n\r]+/)
            .map(item => item.trim())
            .filter(Boolean);

        return {
            enabled: Boolean(form.enabled),
            base_path: form.base_path.trim() || 'knowledge_base',
            embedding: {
                provider: form.embedding_provider.trim() || 'openai',
                model: form.embedding_model.trim() || 'text-embedding-3-small',
                base_url: form.embedding_base_url.trim(),
                api_key: form.embedding_api_key
            },
            retrieval: {
                top_k: topK,
                similarity_threshold: threshold,
                sub_index_filter: form.sub_index_filter.trim(),
                multi_query: {
                    max_queries: maxQueries
                },
                rerank: {
                    provider: form.rerank_provider.trim(),
                    model: form.rerank_model.trim(),
                    base_url: form.rerank_base_url.trim(),
                    api_key: form.rerank_api_key
                },
                post_retrieve: {
                    prefetch_top_k: parseNonNegInt(form.prefetch_top_k, t('settingsBasic.prefetchTopK', null, '预取候选数')),
                    max_context_chars: parseNonNegInt(form.max_context_chars, t('settingsBasic.maxContextChars', null, '返回内容最大字符数')),
                    max_context_tokens: parseNonNegInt(form.max_context_tokens, t('settingsBasic.maxContextTokens', null, '返回内容最大 Token 数'))
                }
            },
            indexing: {
                chunk_strategy: form.chunk_strategy.trim(),
                chunk_size: parseNonNegInt(form.chunk_size, t('settingsBasic.chunkSize', null, '分块大小')),
                chunk_overlap: parseNonNegInt(form.chunk_overlap, t('settingsBasic.chunkOverlap', null, '分块重叠')),
                max_chunks_per_item: parseNonNegInt(form.max_chunks_per_item, t('settingsBasic.maxChunksPerItem', null, '单个知识项最大块数')),
                batch_size: parseNonNegInt(form.batch_size, t('settingsBasic.batchSize', null, '嵌入批大小')),
                request_timeout_seconds: parseNonNegInt(form.request_timeout_seconds, t('settingsBasic.requestTimeoutSeconds', null, '嵌入 HTTP 超时')),
                max_rpm: parseNonNegInt(form.max_rpm, t('settingsBasic.maxRpm', null, '每分钟最大请求数')),
                rate_limit_delay_ms: parseNonNegInt(form.rate_limit_delay_ms, t('settingsBasic.rateLimitDelay', null, '请求间隔延迟')),
                max_retries: parseNonNegInt(form.max_retries, t('settingsBasic.maxRetries', null, '最大重试次数')),
                retry_delay_ms: parseNonNegInt(form.retry_delay_ms, t('settingsBasic.retryDelay', null, '重试间隔')),
                prefer_source_file: Boolean(form.prefer_source_file),
                sub_indexes: subIndexes
            }
        };
    };

    const applyConfig = async message => {
        try {
            await api.post('/config/apply');
            setApplyPending(false);
            toastSuccess(message);
        } catch (error) {
            setApplyPending(true);
            setSaveError(error);
        }
    };

    const save = async () => {
        if (locked || !dirty) return;
        setSaveError(null);
        if (form.enabled && !form.embedding_model.trim()) {
            setSaveError(new Error('启用知识库时请填写嵌入模型名称（例如 text-embedding-v4 或 text-embedding-3-small）'));
            return;
        }
        let payload;
        try {
            payload = buildKnowledgePayload();
        } catch (error) {
            setSaveError(error);
            return;
        }
        setSaving(true);
        try {
            await api.put('/config', { knowledge: payload });
            setDraft({});
            await applyConfig('知识库配置已保存并应用');
            await configRes.reload({ silent: true });
        } catch (error) {
            setSaveError(error);
        } finally {
            setSaving(false);
        }
    };

    const retryApply = async () => {
        if (locked || !applyPending) return;
        setSaving(true);
        setSaveError(null);
        try {
            await applyConfig(t('settings.apply.applySuccess', null, '配置已成功应用'));
        } finally {
            setSaving(false);
        }
    };

    const testEmbedding = async () => {
        if (locked) return;
        if (!form.embedding_model.trim()) {
            setProbe({ key: 'embedding', payload: null, error: new Error('请先填写嵌入模型名称') });
            return;
        }
        setBusy('embedding');
        setProbe(EMPTY_PROBE);
        try {
            const payload = await api.post('/config/test-embedding', {
                channel_id: fallbackChannel.channelId || undefined,
                embedding: {
                    provider: form.embedding_provider.trim() || 'openai',
                    model: form.embedding_model.trim(),
                    base_url: form.embedding_base_url.trim(),
                    api_key: form.embedding_api_key
                },
                openai: fallbackChannel.openai
            });
            setProbe({ key: 'embedding', payload, error: null });
        } catch (error) {
            setProbe({ key: 'embedding', payload: null, error });
        } finally {
            setBusy('');
        }
    };

    const fetchModels = async () => {
        if (locked) return;
        setBusy('list');
        setProbe(EMPTY_PROBE);
        const usingOwnKey = Boolean(form.embedding_api_key);
        const effectiveBaseUrl = form.embedding_base_url.trim()
            || (!mainIsClaude ? fallbackChannel.openai.base_url : '')
            || 'https://api.openai.com/v1';
        try {
            const payload = await api.post('/config/list-models', {
                provider: 'openai_compatible',
                credential_scope: usingOwnKey ? 'knowledgeEmbedding' : (fallbackChannel.channelId ? undefined : 'openai'),
                channel_id: !usingOwnKey && fallbackChannel.channelId ? fallbackChannel.channelId : undefined,
                base_url: effectiveBaseUrl,
                api_key: usingOwnKey ? form.embedding_api_key : fallbackChannel.openai.api_key
            });
            setProbe({ key: 'list', payload, error: null });
        } catch (error) {
            setProbe({ key: 'list', payload: null, error });
        } finally {
            setBusy('');
        }
    };

    const listedModels = probe.key === 'list' && Array.isArray(probe.payload?.models) ? probe.payload.models : [];

    return (
        <>
            <ErrorBanner error={configRes.error} onRetry={configRes.reload} />
            {configRes.data && (
                <SectionCard
                    title={(
                        <Space wrap spacing={8}>
                            <span>{t('settings.knowledge.title', null, '知识库设置')}</span>
                            <Tag color={form.enabled ? 'green' : 'grey'} size="small">
                                {form.enabled ? t('common.enabled', null, '已启用') : t('common.disabled', null, '未启用')}
                            </Tag>
                            {dirty && <Tag color="amber" size="small">{t('settings.unsaved', null, '有未保存修改')}</Tag>}
                        </Space>
                    )}
                    description="开启后将自动初始化向量知识库并向智能体挂载 search_knowledge_base 检索工具。启用保存后，可前往「知识管理」新增知识条目并构建索引。"
                    className="settings-knowledge-panel"
                    extra={(
                        <Space wrap>
                            <Button onClick={() => navigate('/knowledge-management')}>
                                前往知识管理
                            </Button>
                            {canWrite ? (
                                <>
                                    {dirty && (
                                        <Button disabled={saving || Boolean(busy)} onClick={discard}>
                                            {t('toolGuard.reset', null, '撤销修改')}
                                        </Button>
                                    )}
                                    <Button
                                        theme="solid"
                                        type="primary"
                                        loading={saving}
                                        disabled={!dirty || Boolean(busy)}
                                        onClick={save}
                                    >
                                        {t('settings.saveAndApply', null, '保存并应用')}
                                    </Button>
                                </>
                            ) : (
                                <Tag color="grey" size="small">{t('settingsProbe.channelReadOnly', null, '只读')}</Tag>
                            )}
                        </Space>
                    )}
                >
                    <div className="settings-switch-row">
                        <Switch
                            checked={form.enabled}
                            disabled={locked}
                            onChange={next => update('enabled', next)}
                            aria-label={t('settingsBasic.enableKnowledge', null, '启用知识检索功能')}
                        />
                        <div className="settings-switch-copy">
                            <Text strong>{t('settingsBasic.enableKnowledge', null, '启用知识检索功能')}</Text>
                            <Hint>保存并应用后立即在线生效，无需重启服务。</Hint>
                        </div>
                    </div>

                    <div className="settings-vision-grid">
                        <label className="settings-field settings-field-wide">
                            <Text type="tertiary" size="small">{t('settingsBasic.knowledgeBasePath', null, '知识库路径')}</Text>
                            <Input
                                value={form.base_path}
                                onChange={value => update('base_path', value)}
                                readOnly={locked}
                                placeholder={t('settingsBasic.knowledgeBasePathPlaceholder', null, 'knowledge_base')}
                            />
                            <Hint>{t('settingsBasic.knowledgeBasePathHint', null, '相对于配置文件所在目录的路径')}</Hint>
                        </label>
                    </div>

                    <div className="settings-subsection">
                        <Text strong className="settings-subsection-title">{t('settingsBasic.embeddingConfig', null, '嵌入模型配置')}</Text>
                        {needsSeparateEmbeddingNotice && (
                            <Banner
                                type="info"
                                bordered
                                closeIcon={null}
                                description="提示：当前默认 AI 通道（如 DeepSeek 官方或 Claude 接口）通常不提供 OpenAI 兼容的 /embeddings 向量接口。启用知识库时，请在此处填写独立的向量服务地址与密钥（如阿里云百炼 DashScope、OpenAI、SiliconFlow 或本地 Ollama / vLLM）。"
                            />
                        )}
                        <div className="settings-vision-grid">
                            <div className="settings-field">
                                <Text id={providerLabelId} type="tertiary" size="small">{t('settingsBasic.provider', null, '提供商')}</Text>
                                <Select
                                    value={form.embedding_provider || 'openai'}
                                    onChange={value => update('embedding_provider', value)}
                                    optionList={[{ value: 'openai', label: t('settingsProbe.compatible', null, '兼容 OpenAI 的接口') }]}
                                    aria-labelledby={providerLabelId}
                                    disabled={locked}
                                    style={{ width: '100%' }}
                                />
                            </div>
                            <label className="settings-field">
                                <Text type="tertiary" size="small">{t('settingsBasic.modelName', null, '模型名称')}</Text>
                                <Input
                                    value={form.embedding_model}
                                    onChange={value => update('embedding_model', value)}
                                    readOnly={locked}
                                    placeholder={t('settingsBasic.embeddingModelPlaceholder', null, 'text-embedding-v4')}
                                />
                            </label>
                            <label className="settings-field settings-field-wide">
                                <Text type="tertiary" size="small">{t('settingsBasic.baseUrl', null, 'API 地址')}</Text>
                                <Input
                                    value={form.embedding_base_url}
                                    onChange={value => update('embedding_base_url', value)}
                                    readOnly={locked}
                                    placeholder={!mainIsClaude && fallbackChannel.openai.base_url
                                        ? `留空复用默认通道（${fallbackChannel.openai.base_url}）`
                                        : '例如 https://dashscope.aliyuncs.com/compatible-mode/v1'}
                                />
                            </label>
                            <label className="settings-field">
                                <Text type="tertiary" size="small">{t('settingsBasic.apiKey', null, 'API 密钥')}</Text>
                                <Input
                                    mode="password"
                                    autoComplete="new-password"
                                    value={form.embedding_api_key}
                                    onChange={value => update('embedding_api_key', value)}
                                    readOnly={locked}
                                    placeholder={t('settingsBasic.embeddingApiKeyPlaceholder', null, '留空则使用默认通道 API Key')}
                                />
                            </label>
                        </div>

                        <Space wrap className="settings-probe-actions">
                            <Button
                                loading={busy === 'embedding'}
                                disabled={!canWrite || saving || (Boolean(busy) && busy !== 'embedding')}
                                onClick={testEmbedding}
                            >
                                测试嵌入模型
                            </Button>
                            <Button
                                loading={busy === 'list'}
                                disabled={!canWrite || saving || (Boolean(busy) && busy !== 'list')}
                                onClick={fetchModels}
                            >
                                {t('settingsBasic.modelsList', null, '获取模型列表')}
                            </Button>
                        </Space>

                        {probe.error && <div className="settings-probe-result"><ErrorBanner error={probe.error} /></div>}
                        {probe.key === 'embedding' && probe.payload && (
                            <div className="settings-probe-result">
                                {probe.payload.success ? (
                                    <KeyValueList
                                        columns={4}
                                        items={[
                                            { label: t('settingsProbe.result', null, '测试结果'), value: <Tag color="green">{t('settingsBasic.testSuccess', null, '连接成功')}</Tag> },
                                            { label: t('settingsBasic.modelName', null, '模型名称'), value: probe.payload.model || form.embedding_model || '—' },
                                            { label: '向量维度', value: probe.payload.dimensions ? `${probe.payload.dimensions} 维` : '—' },
                                            {
                                                label: t('settingsProbe.latency', null, '响应耗时'),
                                                value: probe.payload.latency_ms != null
                                                    ? t('settingsProbe.milliseconds', { value: probe.payload.latency_ms }, `${probe.payload.latency_ms} 毫秒`)
                                                    : '—'
                                            }
                                        ]}
                                    />
                                ) : (
                                    <Banner type="danger" bordered closeIcon={null} description={probe.payload.error || t('settingsBasic.testFailed', null, '连接失败')} />
                                )}
                            </div>
                        )}
                        {probe.key === 'list' && probe.payload && (
                            <div className="settings-probe-result">
                                {probe.payload.success ? (
                                    <Space wrap spacing={8}>
                                        <Text>
                                            {t('settingsBasic.modelsListSuccess', { count: probe.payload.count ?? listedModels.length }, `已获取 ${probe.payload.count ?? listedModels.length} 个模型`)}
                                        </Text>
                                        <span id={pickerLabelId} hidden>{t('settingsBasic.modelsListSelectPlaceholder', null, '请选择嵌入模型')}</span>
                                        {listedModels.length > 0 && (
                                            <Select
                                                filter
                                                aria-labelledby={pickerLabelId}
                                                placeholder={t('settingsBasic.modelsListSelectPlaceholder', null, '请选择模型')}
                                                value={listedModels.includes(form.embedding_model) ? form.embedding_model : undefined}
                                                optionList={listedModels.map(id => ({ value: id, label: id }))}
                                                onChange={value => update('embedding_model', value)}
                                                disabled={locked}
                                                className="settings-model-picker"
                                            />
                                        )}
                                    </Space>
                                ) : (
                                    <Banner
                                        type={probe.payload.supported === false ? 'info' : 'danger'}
                                        bordered
                                        closeIcon={null}
                                        description={probe.payload.error || t('settingsBasic.modelsListFailed', null, '获取模型列表失败')}
                                    />
                                )}
                            </div>
                        )}
                    </div>

                    <div className="settings-subsection">
                        <Text strong className="settings-subsection-title">{t('settingsBasic.retrievalConfig', null, '检索配置')}</Text>
                        <div className="settings-vision-grid">
                            <label className="settings-field">
                                <Text type="tertiary" size="small">{t('settingsBasic.topK', null, 'Top-K 结果数量')}</Text>
                                <Input
                                    value={form.top_k}
                                    onChange={value => update('top_k', value)}
                                    inputMode="numeric"
                                    readOnly={locked}
                                    placeholder={t('settingsBasic.topKPlaceholder', null, '5')}
                                />
                            </label>
                            <label className="settings-field">
                                <Text type="tertiary" size="small">{t('settingsBasic.similarityThreshold', null, '相似度阈值（0 ~ 1）')}</Text>
                                <Input
                                    value={form.similarity_threshold}
                                    onChange={value => update('similarity_threshold', value)}
                                    readOnly={locked}
                                    placeholder="0.4"
                                />
                            </label>
                            <label className="settings-field">
                                <Text type="tertiary" size="small">{t('settingsBasic.multiQueryMaxQueries', null, 'MultiQuery 改写变体上限')}</Text>
                                <Input
                                    value={form.multi_query_max_queries}
                                    onChange={value => update('multi_query_max_queries', value)}
                                    inputMode="numeric"
                                    readOnly={locked}
                                    placeholder={t('settingsBasic.multiQueryMaxQueriesPlaceholder', null, '4')}
                                />
                            </label>
                            <label className="settings-field">
                                <Text type="tertiary" size="small">{t('settingsBasic.subIndexFilter', null, '子索引过滤（可选）')}</Text>
                                <Input
                                    value={form.sub_index_filter}
                                    onChange={value => update('sub_index_filter', value)}
                                    readOnly={locked}
                                    placeholder={t('settingsBasic.subIndexFilterPlaceholder', null, '如 prod，与索引 sub_indexes 一致')}
                                />
                            </label>
                        </div>
                        <Hint>{t('settingsBasic.similarityHint', null, '低于相似度阈值的结果将被过滤；MultiQuery 设为 1 时跳过 LLM 改写直接做向量检索。')}</Hint>
                    </div>

                    {(applyPending || saveError) && (
                        <div className="settings-channel-notices">
                            {applyPending && (
                                <Banner type="warning" bordered closeIcon={null} description={(
                                    <Space wrap>
                                        <span>知识库配置已写入 config.yaml，但动态应用失败（请检查嵌入模型配置或密钥）。</span>
                                        <Button size="small" disabled={locked} onClick={retryApply}>
                                            {t('settingsProbe.retryApply', null, '重试应用')}
                                        </Button>
                                    </Space>
                                )} />
                            )}
                            <ErrorBanner error={saveError} />
                        </div>
                    )}

                    <Collapse className="settings-audit-model">
                        <Collapse.Panel
                            itemKey="knowledge-rerank"
                            header={<Text strong>{t('settingsBasic.ragPipelineHeader', null, '高级：精排（Rerank）与检索后处理')}</Text>}
                        >
                            <Hint>{t('settingsBasic.ragPipelineHint', null, '精排失败时会自动降级为融合排序，不影响基础检索可用性。')}</Hint>
                            <div className="settings-vision-grid" style={{ marginTop: 8 }}>
                                <div className="settings-field">
                                    <Text id={rerankProviderId} type="tertiary" size="small">{t('settingsBasic.rerankProvider', null, '精排提供商')}</Text>
                                    <Select
                                        value={form.rerank_provider}
                                        onChange={value => update('rerank_provider', value)}
                                        optionList={[
                                            { value: '', label: t('settingsBasic.rerankProviderAuto', null, '自动（按 Base URL 推断）') },
                                            { value: 'dashscope', label: 'DashScope（gte-rerank）' },
                                            { value: 'cohere', label: t('settingsBasic.rerankProviderCohere', null, 'Cohere / /v1/rerank 兼容接口') }
                                        ]}
                                        aria-labelledby={rerankProviderId}
                                        disabled={locked}
                                        style={{ width: '100%' }}
                                    />
                                </div>
                                <label className="settings-field">
                                    <Text type="tertiary" size="small">{t('settingsBasic.rerankModel', null, '精排模型（可选）')}</Text>
                                    <Input
                                        value={form.rerank_model}
                                        onChange={value => update('rerank_model', value)}
                                        readOnly={locked}
                                        placeholder={t('settingsBasic.rerankModelPlaceholder', null, '留空按提供商自动选择')}
                                    />
                                </label>
                                <label className="settings-field settings-field-wide">
                                    <Text type="tertiary" size="small">{t('settingsBasic.rerankBaseUrl', null, '精排 Base URL（可选）')}</Text>
                                    <Input
                                        value={form.rerank_base_url}
                                        onChange={value => update('rerank_base_url', value)}
                                        readOnly={locked}
                                        placeholder={t('settingsBasic.rerankBaseUrlPlaceholder', null, '留空则复用嵌入 / OpenAI 的 base_url')}
                                    />
                                </label>
                                <label className="settings-field">
                                    <Text type="tertiary" size="small">{t('settingsBasic.rerankApiKey', null, '精排 API Key（可选）')}</Text>
                                    <Input
                                        mode="password"
                                        autoComplete="new-password"
                                        value={form.rerank_api_key}
                                        onChange={value => update('rerank_api_key', value)}
                                        readOnly={locked}
                                        placeholder={t('settingsBasic.rerankApiKeyPlaceholder', null, '留空则复用嵌入 / OpenAI 的 api_key')}
                                    />
                                </label>
                                <label className="settings-field">
                                    <Text type="tertiary" size="small">{t('settingsBasic.prefetchTopK', null, '预取候选数')}</Text>
                                    <Input
                                        value={form.prefetch_top_k}
                                        onChange={value => update('prefetch_top_k', value)}
                                        inputMode="numeric"
                                        readOnly={locked}
                                        placeholder={t('settingsBasic.prefetchTopKPlaceholder', null, '20')}
                                    />
                                </label>
                                <label className="settings-field">
                                    <Text type="tertiary" size="small">{t('settingsBasic.maxContextChars', null, '返回内容最大字符数')}</Text>
                                    <Input
                                        value={form.max_context_chars}
                                        onChange={value => update('max_context_chars', value)}
                                        inputMode="numeric"
                                        readOnly={locked}
                                        placeholder="0"
                                    />
                                </label>
                                <label className="settings-field">
                                    <Text type="tertiary" size="small">{t('settingsBasic.maxContextTokens', null, '返回内容最大 Token 数')}</Text>
                                    <Input
                                        value={form.max_context_tokens}
                                        onChange={value => update('max_context_tokens', value)}
                                        inputMode="numeric"
                                        readOnly={locked}
                                        placeholder="0"
                                    />
                                </label>
                            </div>
                        </Collapse.Panel>

                        <Collapse.Panel
                            itemKey="knowledge-indexing"
                            header={<Text strong>{t('settingsBasic.indexConfig', null, '高级：索引分块与限流配置')}</Text>}
                        >
                            <div className="settings-vision-grid">
                                <div className="settings-field">
                                    <Text id={strategyLabelId} type="tertiary" size="small">{t('settingsBasic.chunkStrategy', null, '分块策略')}</Text>
                                    <Select
                                        value={form.chunk_strategy}
                                        onChange={value => update('chunk_strategy', value)}
                                        optionList={[
                                            { value: '', label: t('settingsBasic.chunkStrategyMarkdownRecursive', null, 'Markdown 标题切分后递归（默认推荐）') },
                                            { value: 'markdown_then_recursive', label: t('settingsBasic.chunkStrategyMarkdownRecursive', null, 'Markdown 标题切分后递归') },
                                            { value: 'recursive', label: t('settingsBasic.chunkStrategyRecursive', null, '仅递归切分') }
                                        ]}
                                        aria-labelledby={strategyLabelId}
                                        disabled={locked}
                                        style={{ width: '100%' }}
                                    />
                                </div>
                                {[
                                    ['chunk_size', 'chunkSize', '分块大小（Chunk Size）', '512'],
                                    ['chunk_overlap', 'chunkOverlap', '分块重叠（Chunk Overlap）', '50'],
                                    ['max_chunks_per_item', 'maxChunksPerItem', '单个知识项最大块数', '0'],
                                    ['batch_size', 'batchSize', '嵌入批大小', '64'],
                                    ['request_timeout_seconds', 'requestTimeoutSeconds', '嵌入 HTTP 超时（秒）', '120'],
                                    ['max_rpm', 'maxRpm', '每分钟最大请求数（Max RPM）', '0'],
                                    ['rate_limit_delay_ms', 'rateLimitDelay', '请求间隔延迟（毫秒）', '0'],
                                    ['max_retries', 'maxRetries', '最大重试次数', '3'],
                                    ['retry_delay_ms', 'retryDelay', '重试间隔（毫秒）', '1000']
                                ].map(([field, key, fallback, placeholder]) => (
                                    <label key={field} className="settings-field">
                                        <Text type="tertiary" size="small">{t('settingsBasic.' + key, null, fallback)}</Text>
                                        <Input
                                            value={form[field]}
                                            onChange={value => update(field, value)}
                                            inputMode="numeric"
                                            readOnly={locked}
                                            placeholder={placeholder}
                                        />
                                    </label>
                                ))}
                                <label className="settings-field settings-field-wide">
                                    <Text type="tertiary" size="small">{t('settingsBasic.subIndexes', null, '子索引标签（逗号分隔）')}</Text>
                                    <Input
                                        value={form.sub_indexes}
                                        onChange={value => update('sub_indexes', value)}
                                        readOnly={locked}
                                        placeholder={t('settingsBasic.subIndexesPlaceholder', null, '例如: prod, knowledge')}
                                    />
                                </label>
                                <div className="settings-field">
                                    <Text type="tertiary" size="small">{t('settingsBasic.preferSourceFile', null, '优先从磁盘源文件读取')}</Text>
                                    <Switch
                                        checked={form.prefer_source_file}
                                        disabled={locked}
                                        onChange={next => update('prefer_source_file', next)}
                                        aria-label={t('settingsBasic.preferSourceFile', null, '优先从磁盘源文件读取')}
                                    />
                                </div>
                            </div>
                        </Collapse.Panel>
                    </Collapse>
                </SectionCard>
            )}
        </>
    );
}
