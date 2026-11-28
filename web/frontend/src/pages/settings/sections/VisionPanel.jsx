/**
 * 独立视觉分析模型配置（vision 段 / analyze_image 工具）：
 * 当主对话模型不支持图片输入，或安全测试中需要识别本地截图、验证码文件时，
 * 由独立 VL 模型识图并将文字结果返回给主模型。
 */
import { useEffect, useId, useRef, useState } from 'react';
import { Banner, Button, Collapse, Input, Select, Space, Switch, Tag, Typography } from '@douyinfe/semi-ui';
import { api } from '../../../lib/http';
import { useT } from '../../../lib/i18n';
import { modelChannelName } from '../../../lib/model-channel';
import { ErrorBanner, KeyValueList, SectionCard, toastSuccess } from '../../../components/ui';
import { Hint } from './common';

const { Text } = Typography;

const EMPTY_PROBE = { key: '', payload: null, error: null };

function normalizeProvider(raw) {
    const value = String(raw || '').trim();
    const lower = value.toLowerCase();
    if (!lower) return '';
    if (lower === 'openai' || lower === 'openai_compatible') return 'openai_compatible';
    if (lower === 'claude' || lower === 'anthropic') return 'claude';
    return value;
}

function visionValues(vision = {}) {
    return {
        enabled: Boolean(vision.enabled),
        provider: normalizeProvider(vision.provider),
        base_url: vision.base_url || '',
        api_key: vision.api_key || '',
        model: vision.model || '',
        detail: vision.detail || '',
        timeout_seconds: vision.timeout_seconds > 0 ? String(vision.timeout_seconds) : '',
        max_image_bytes: vision.max_image_bytes > 0 ? String(vision.max_image_bytes) : '',
        max_dimension: vision.max_dimension > 0 ? String(vision.max_dimension) : '',
        jpeg_quality: vision.jpeg_quality > 0 ? String(vision.jpeg_quality) : '',
        max_payload_bytes: vision.max_payload_bytes > 0 ? String(vision.max_payload_bytes) : '',
        skip_preprocess_below_bytes: vision.skip_preprocess_below_bytes != null && vision.skip_preprocess_below_bytes !== 0
            ? String(vision.skip_preprocess_below_bytes)
            : (vision.skip_preprocess_below_bytes === 0 && Object.hasOwn(vision, 'skip_preprocess_below_bytes') ? '0' : '')
    };
}

function parseNonNegInt(raw) {
    const trimmed = String(raw ?? '').trim();
    if (!trimmed) return 0;
    if (!/^\d+$/.test(trimmed)) return null;
    const num = Number(trimmed);
    return Number.isSafeInteger(num) && num >= 0 ? num : null;
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

export default function VisionPanel({ config, canWrite, reloadConfig, focused }) {
    const t = useT();
    const cardRef = useRef(null);
    const providerLabelId = useId();
    const detailLabelId = useId();
    const pickerLabelId = useId();

    const baseline = visionValues(config.vision);
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

    useEffect(() => {
        if (focused && cardRef.current) {
            cardRef.current.scrollIntoView({ behavior: 'smooth', block: 'start' });
        }
    }, [focused]);

    const locked = !canWrite || saving || Boolean(busy);
    const fallbackChannel = resolveDefaultChannel(config);
    const effectiveProvider = form.provider || normalizeProvider(fallbackChannel.openai.provider) || 'openai_compatible';
    const effectiveBaseUrl = form.base_url.trim() || fallbackChannel.openai.base_url || (
        effectiveProvider === 'claude' ? 'https://api.anthropic.com' : 'https://api.openai.com/v1'
    );

    const providerOptions = [
        {
            value: '',
            label: fallbackChannel.displayName
                ? `${t('settingsBasic.visionProviderReuseOpenAI', null, '默认 AI 通道（留空复用）')} · ${fallbackChannel.displayName}`
                : t('settingsBasic.visionProviderReuseOpenAI', null, '默认 AI 通道（留空复用）')
        },
        { value: 'openai_compatible', label: t('settingsProbe.compatible', null, '兼容 OpenAI 的接口') },
        { value: 'claude', label: t('settingsBasic.providerClaude', null, 'Claude（Anthropic Messages）') }
    ];
    if (form.provider && !providerOptions.some(opt => opt.value === form.provider)) {
        providerOptions.push({ value: form.provider, label: form.provider });
    }

    const detailOptions = [
        { value: '', label: '默认（low）' },
        { value: 'auto', label: 'auto（自动）' },
        { value: 'low', label: 'low（省流快速）' },
        { value: 'high', label: 'high（高清细节）' }
    ];

    const update = (field, value) => {
        if (probe.key !== 'list' || ['provider', 'base_url', 'api_key'].includes(field)) {
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

    const buildVisionPayload = () => {
        const numericFields = [
            ['timeout_seconds', t('settingsBasic.visionTimeout', null, '超时（秒）')],
            ['max_image_bytes', t('settingsBasic.visionMaxImageBytes', null, '原始文件上限（字节）')],
            ['max_dimension', t('settingsBasic.visionMaxDimension', null, '长边缩放像素')],
            ['jpeg_quality', t('settingsBasic.visionJpegQuality', null, 'JPEG 质量')],
            ['max_payload_bytes', t('settingsBasic.visionMaxPayloadBytes', null, '送 API 体积上限（字节）')],
            ['skip_preprocess_below_bytes', t('settingsBasic.visionSkipPreprocessBytes', null, '低于该字节可原图直传')]
        ];
        const parsedNums = {};
        for (const [key, labelText] of numericFields) {
            const num = parseNonNegInt(form[key]);
            if (num === null) {
                throw new Error(t('settingsProbe.invalidLimit', { field: labelText }, `${labelText}请输入非负整数`));
            }
            parsedNums[key] = num;
        }
        if (parsedNums.jpeg_quality > 100) {
            throw new Error('JPEG 质量需在 1 ~ 100 之间（留空或 0 使用默认 82）');
        }
        return {
            enabled: Boolean(form.enabled),
            provider: form.provider.trim(),
            base_url: form.base_url.trim(),
            api_key: form.api_key,
            model: form.model.trim(),
            detail: form.detail.trim(),
            ...parsedNums
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
        if (form.enabled && !form.model.trim()) {
            setSaveError(new Error(t('settingsBasic.visionModelRequired', null, '启用视觉分析时请填写视觉模型名称')));
            return;
        }
        let visionPayload;
        try {
            visionPayload = buildVisionPayload();
        } catch (error) {
            setSaveError(error);
            return;
        }
        setSaving(true);
        try {
            await api.put('/config', { vision: visionPayload });
            setDraft({});
            await applyConfig(t('settingsProbe.visionSaved', null, '视觉模型配置已保存并应用'));
            await reloadConfig({ silent: true });
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

    const testVision = async () => {
        if (locked) return;
        if (!form.model.trim()) {
            setProbe({
                key: 'vision',
                payload: null,
                error: new Error(t('settingsBasic.visionTestFillRequired', null, '请填写视觉模型，并确保 API Key 可用（可复用 OpenAI）'))
            });
            return;
        }
        setBusy('vision');
        setProbe(EMPTY_PROBE);
        try {
            const visionPayload = buildVisionPayload();
            const payload = await api.post('/config/test-vision', {
                channel_id: fallbackChannel.channelId || undefined,
                vision: visionPayload,
                openai: fallbackChannel.openai
            });
            setProbe({ key: 'vision', payload, error: null });
        } catch (error) {
            setProbe({ key: 'vision', payload: null, error });
        } finally {
            setBusy('');
        }
    };

    const fetchModels = async () => {
        if (locked) return;
        setBusy('list');
        setProbe(EMPTY_PROBE);
        const usingOwnKey = Boolean(form.api_key);
        try {
            const payload = await api.post('/config/list-models', {
                provider: effectiveProvider,
                credential_scope: usingOwnKey ? 'vision' : (fallbackChannel.channelId ? undefined : 'openai'),
                channel_id: !usingOwnKey && fallbackChannel.channelId ? fallbackChannel.channelId : undefined,
                base_url: effectiveBaseUrl,
                api_key: usingOwnKey ? form.api_key : fallbackChannel.openai.api_key
            });
            setProbe({ key: 'list', payload, error: null });
        } catch (error) {
            setProbe({ key: 'list', payload: null, error });
        } finally {
            setBusy('');
        }
    };

    const statusTag = form.enabled
        ? (form.model.trim()
            ? <Tag color="green" size="small">{`${t('common.enabled', null, '已启用')} · ${form.model.trim()}`}</Tag>
            : <Tag color="amber" size="small">待填写模型</Tag>)
        : <Tag color="grey" size="small">{t('common.disabled', null, '未启用')}</Tag>;

    const listedModels = probe.key === 'list' && Array.isArray(probe.payload?.models) ? probe.payload.models : [];

    return (
        <div id="settings-vision-panel" ref={cardRef} className={focused ? 'settings-focus-ring' : undefined}>
            <SectionCard
                title={(
                    <Space wrap spacing={8}>
                        <span>{t('settingsBasic.visionConfig', null, '视觉分析（analyze_image）')}</span>
                        {statusTag}
                        {dirty && <Tag color="amber" size="small">{t('settings.unsaved', null, '有未保存修改')}</Tag>}
                    </Space>
                )}
                description="当主对话模型不支持图片识别（如纯文本推理模型），或安全测试中需要识别本地截图、验证码图片时，开启后将挂载 analyze_image 工具，由独立视觉模型代为识图并仅向主模型返回文字摘要。"
                className="settings-vision-panel"
                extra={canWrite ? (
                    <Space wrap>
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
                            保存视觉模型
                        </Button>
                    </Space>
                ) : (
                    <Tag color="grey" size="small">{t('settingsProbe.channelReadOnly', null, '只读')}</Tag>
                )}
            >
                <div className="settings-switch-row">
                    <Switch
                        checked={form.enabled}
                        disabled={locked}
                        onChange={next => update('enabled', next)}
                        aria-label={t('settingsBasic.visionEnabled', null, '启用视觉分析工具 analyze_image')}
                    />
                    <div className="settings-switch-copy">
                        <Text strong>{t('settingsBasic.visionEnabled', null, '启用视觉分析工具 analyze_image')}</Text>
                        <Hint>{t('settingsBasic.visionEnabledHint', null, '启用后注册 MCP 工具；图片仅在单次 VL 调用中出现，Agent 上下文只保留文字摘要。保存并应用后生效。')}</Hint>
                    </div>
                </div>

                <div className="settings-vision-grid">
                    <label className="settings-field">
                        <Text type="tertiary" size="small">{t('settingsBasic.visionModel', null, '视觉模型')}</Text>
                        <Input
                            value={form.model}
                            onChange={value => update('model', value)}
                            readOnly={locked}
                            placeholder={t('settingsBasic.visionModelPlaceholder', null, 'qwen-vl-max')}
                        />
                    </label>
                    <div className="settings-field">
                        <Text id={providerLabelId} type="tertiary" size="small">{t('settingsBasic.apiProvider', null, 'API 提供商')}</Text>
                        <Select
                            value={form.provider}
                            onChange={value => update('provider', value)}
                            optionList={providerOptions}
                            aria-labelledby={providerLabelId}
                            disabled={locked}
                            style={{ width: '100%' }}
                        />
                    </div>
                    <label className="settings-field settings-field-wide">
                        <Text type="tertiary" size="small">{t('settingsBasic.baseUrl', null, 'API 地址')}</Text>
                        <Input
                            value={form.base_url}
                            onChange={value => update('base_url', value)}
                            readOnly={locked}
                            placeholder={fallbackChannel.openai.base_url
                                ? `留空复用默认通道（${fallbackChannel.openai.base_url}）`
                                : t('settingsBasic.visionBaseUrlPlaceholder', null, '留空则复用 OpenAI Base URL')}
                        />
                    </label>
                    <label className="settings-field">
                        <Text type="tertiary" size="small">{t('settingsBasic.apiKey', null, 'API 密钥')}</Text>
                        <Input
                            mode="password"
                            autoComplete="new-password"
                            value={form.api_key}
                            onChange={value => update('api_key', value)}
                            readOnly={locked}
                            placeholder={t('settingsBasic.visionApiKeyPlaceholder', null, '留空则复用 OpenAI API Key')}
                        />
                    </label>
                </div>

                {(applyPending || saveError) && (
                    <div className="settings-channel-notices">
                        {applyPending && (
                            <Banner type="warning" bordered closeIcon={null} description={(
                                <Space wrap>
                                    <span>视觉模型配置已写入，但应用失败。</span>
                                    <Button size="small" disabled={locked} onClick={retryApply}>
                                        {t('settingsProbe.retryApply', null, '重试应用')}
                                    </Button>
                                </Space>
                            )} />
                        )}
                        <ErrorBanner error={saveError} />
                    </div>
                )}

                <Space wrap className="settings-probe-actions">
                    <Button
                        loading={busy === 'vision'}
                        disabled={!canWrite || saving || (Boolean(busy) && busy !== 'vision')}
                        onClick={testVision}
                    >
                        {t('settingsProbe.testVision', null, '测试视觉模型')}
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
                {probe.key === 'vision' && probe.payload && (
                    <div className="settings-probe-result">
                        {probe.payload.success ? (
                            <KeyValueList
                                columns={3}
                                items={[
                                    { label: t('settingsProbe.result', null, '测试结果'), value: <Tag color="green">{t('settingsBasic.testSuccess', null, '连接成功')}</Tag> },
                                    { label: t('settingsBasic.visionModel', null, '视觉模型'), value: probe.payload.model || form.model || '—' },
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
                                <span id={pickerLabelId} hidden>{t('settingsBasic.modelsListSelectPlaceholder', null, '请选择视觉模型')}</span>
                                {listedModels.length > 0 && (
                                    <Select
                                        filter
                                        aria-labelledby={pickerLabelId}
                                        placeholder={t('settingsBasic.modelsListSelectPlaceholder', null, '请选择模型')}
                                        value={listedModels.includes(form.model) ? form.model : undefined}
                                        optionList={listedModels.map(id => ({ value: id, label: id }))}
                                        onChange={value => update('model', value)}
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

                <Collapse className="settings-audit-model">
                    <Collapse.Panel
                        itemKey="vision-advanced"
                        header={<Text strong>{t('settingsBasic.visionAdvanced', null, '高级：预处理与限制')}</Text>}
                    >
                        <div className="settings-vision-grid">
                            <div className="settings-field">
                                <Text id={detailLabelId} type="tertiary" size="small">{t('settingsBasic.visionDetail', null, '图片精度（Detail）')}</Text>
                                <Select
                                    value={form.detail}
                                    onChange={value => update('detail', value)}
                                    optionList={detailOptions}
                                    aria-labelledby={detailLabelId}
                                    disabled={locked}
                                    style={{ width: '100%' }}
                                />
                            </div>
                            {[
                                ['timeout_seconds', 'visionTimeout', '超时（秒）', '60'],
                                ['max_image_bytes', 'visionMaxImageBytes', '原始文件上限（字节）', '5242880'],
                                ['max_dimension', 'visionMaxDimension', '长边缩放像素', '2048'],
                                ['jpeg_quality', 'visionJpegQuality', 'JPEG 质量', '82'],
                                ['max_payload_bytes', 'visionMaxPayloadBytes', '送 API 体积上限（字节）', '524288'],
                                ['skip_preprocess_below_bytes', 'visionSkipPreprocessBytes', '低于该字节可原图直传', '2097152']
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
                        </div>
                        <Hint>{t('settingsBasic.visionSkipPreprocessHint', null, '0 表示始终 JPEG 压缩；需同时满足长边与 payload 限制。留空使用系统默认值。')}</Hint>
                    </Collapse.Panel>
                </Collapse>
            </SectionCard>
        </div>
    );
}
