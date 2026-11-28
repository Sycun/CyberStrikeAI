import { useEffect, useMemo, useRef, useState } from 'react';
import {
    Banner, Button, Checkbox, Input, InputNumber, Radio, RadioGroup, Select, Space, Tabs, TabPane, Tag, TextArea, Typography
} from '@douyinfe/semi-ui';
import { IconBolt, IconCheckCircleStroked, IconExternalOpen, IconPlay, IconRefresh, IconStop } from '@douyinfe/semi-icons';
import { useT } from '../../lib/i18n';
import { CopyButton, Drawer, JsonViewer, KeyValueList, toastError, toastSuccess } from '../../components/ui';
import {
    driverLabel, pluginApi, pluginErrorText, starterManifestTemplate, stateColor, stateLabel, streamPluginLogs
} from './api';

const { Text, Paragraph } = Typography;

export function PlanApprovalDrawer({
    planResponse, channels, visible, canUseAgent, onCancel, onConfirmed, onAskAgentDriverFix
}) {
    const t = useT();
    const [autoBindLLM, setAutoBindLLM] = useState(true);
    const [submitting, setSubmitting] = useState(false);
    const [liveProbe, setLiveProbe] = useState(null);
    const [reprobing, setReprobing] = useState(false);

    useEffect(() => {
        if (visible) {
            setAutoBindLLM(true);
            setLiveProbe(planResponse?.driver_probe || null);
        }
    }, [visible, planResponse?.plan?.id, planResponse?.driver_probe]);

    const plan = planResponse?.plan;
    const manifest = planResponse?.manifest;
    const artifact = plan?.artifact;
    const effectiveProbe = liveProbe || planResponse?.driver_probe || null;
    const driverUnavailable = Boolean(effectiveProbe && !effectiveProbe.available);
    const llmDeclared = Boolean(manifest?.llm?.bindings?.length);
    const defaultChannel = useMemo(
        () => (channels || []).find(ch => ch.is_default) || (channels || [])[0] || null,
        [channels]
    );
    const matchingDefault = useMemo(() => {
        if (!llmDeclared || !defaultChannel) return false;
        return manifest.llm.bindings.some(b => b.protocol === defaultChannel.protocol);
    }, [llmDeclared, defaultChannel, manifest]);

    const handleReprobe = async () => {
        if (!plan || !artifact?.driver) return;
        setReprobing(true);
        try {
            const res = await pluginApi.probes(plan.isolation || 'standard');
            const isCompose = (manifest?.distribution || []).some(
                d => d.type === artifact.driver && d.mode === 'compose'
            );
            const key = artifact.driver === 'container' && isCompose ? 'compose' : artifact.driver;
            const nextProbe = res?.drivers?.[key] || res?.drivers?.[artifact.driver];
            if (nextProbe) {
                setLiveProbe(nextProbe);
                if (nextProbe.available) {
                    toastSuccess(t('pluginHub.driverNowReady', null, '运行驱动已就绪，可以继续一键安装'));
                }
            }
        } catch (err) {
            toastError(pluginErrorText(t, err));
        } finally {
            setReprobing(false);
        }
    };

    const handleConfirm = async () => {
        if (!plan || !planResponse?.plan_digest) return;
        setSubmitting(true);
        try {
            const op = await pluginApi.install({
                planId: plan.id,
                planDigest: planResponse.plan_digest,
                autoBindLLM: llmDeclared ? autoBindLLM : undefined
            });
            toastSuccess(t('pluginHub.installQueued', null, '安装任务已提交，正在后台准备并启动'));
            onConfirmed?.(op);
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.installFailed', null, '提交安装失败')));
        } finally {
            setSubmitting(false);
        }
    };

    return (
        <Drawer
            title={t('pluginHub.planApprovalTitle', null, '确认插件安装计划')}
            visible={visible}
            onCancel={onCancel}
            width={680}
            footer={
                <Space>
                    <Button onClick={onCancel}>{t('common.cancel', null, '取消')}</Button>
                    <Button
                        theme="solid"
                        type="primary"
                        loading={submitting}
                        disabled={!planResponse?.can_confirm || driverUnavailable}
                        onClick={handleConfirm}
                    >
                        {driverUnavailable
                            ? t('pluginHub.driverMissingBtn', null, '缺少运行驱动，请先安装依赖')
                            : t('pluginHub.confirmInstall', null, '确认并一键安装')}
                    </Button>
                </Space>
            }
        >
            {plan && manifest && (
                <div className="cs-ph-drawer-stack">
                    {driverUnavailable && (
                        <Banner
                            type="danger"
                            bordered
                            closeIcon={null}
                            title={t('pluginHub.driverMissingTitle', { driver: driverLabel(t, artifact?.driver) }, `当前宿主机缺少所需驱动：${driverLabel(t, artifact?.driver)}`)}
                            description={
                                <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginTop: 4 }}>
                                    <Text size="small">
                                        {pluginErrorText(t, effectiveProbe.code, effectiveProbe.guidance)}
                                    </Text>
                                    {effectiveProbe.install_command && (
                                        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                                            <code className="cs-ph-mono" style={{ fontSize: 12, padding: '4px 8px', borderRadius: 4, background: 'var(--semi-color-fill-1)', wordBreak: 'break-all' }}>
                                                {effectiveProbe.install_command}
                                            </code>
                                            <CopyButton text={effectiveProbe.install_command} label={t('common.copy', null, '复制')} />
                                        </div>
                                    )}
                                    <Space wrap>
                                        {canUseAgent && onAskAgentDriverFix && (
                                            <Button
                                                size="small"
                                                theme="solid"
                                                type="primary"
                                                icon={<IconBolt />}
                                                onClick={() => onAskAgentDriverFix({
                                                    driver: artifact?.driver,
                                                    probe: effectiveProbe,
                                                    hostOS: planResponse.host_os,
                                                    hostArch: planResponse.host_arch,
                                                    pluginTitle: manifest.metadata?.title || manifest.metadata?.id
                                                })}
                                            >
                                                {t('pluginHub.askAgentInstallDriver', null, '交给 CSAI Agent 自动安装配置')}
                                            </Button>
                                        )}
                                        {effectiveProbe.download_url && (
                                            <Button
                                                size="small"
                                                icon={<IconExternalOpen />}
                                                onClick={() => window.open(effectiveProbe.download_url, '_blank', 'noopener,noreferrer')}
                                            >
                                                {t('pluginHub.officialDownload', null, '官方下载')}
                                            </Button>
                                        )}
                                        <Button size="small" icon={<IconRefresh />} loading={reprobing} onClick={handleReprobe}>
                                            {t('pluginHub.recheckDriver', null, '重新检测')}
                                        </Button>
                                    </Space>
                                </div>
                            }
                        />
                    )}
                    {!planResponse.can_confirm && (
                        <Banner
                            type="warning"
                            bordered
                            closeIcon={null}
                            description={t('pluginHub.hostApprovalRequiredBanner', null, '当前宿主开启了 host_config_only 策略，需要先将下方 Manifest 与制品摘要写入 config.yaml 的 plugins.approved_artifacts 后方可安装。')}
                        />
                    )}
                    <KeyValueList columns={2} items={[
                        { label: t('pluginHub.pluginTitle', null, '插件名称'), value: `${manifest.metadata?.title || manifest.metadata?.id} (${manifest.metadata?.version || '—'})` },
                        { label: t('pluginHub.repository', null, '原作者仓库'), value: <span className="cs-ph-mono">{manifest.metadata?.repository}</span> },
                        { label: t('pluginHub.driver', null, '运行驱动'), value: driverLabel(t, artifact?.driver) },
                        { label: t('pluginHub.isolation', null, '隔离档位'), value: plan.isolation },
                        { label: t('pluginHub.platform', null, '目标平台'), value: `${plan.platform?.os}/${plan.platform?.arch}` },
                        { label: t('pluginHub.approvalPolicy', null, '审批策略'), value: planResponse.approval_policy || 'admin_confirm' },
                        { label: 'Manifest SHA-256', value: <span className="cs-ph-mono">{plan.manifestSha256}</span> },
                        { label: t('pluginHub.artifactDigest', null, '锁定制品摘要'), value: <span className="cs-ph-mono">{planResponse.artifact_digest}</span> }
                    ]} />

                    {artifact?.driver === 'git' && (
                        <KeyValueList columns={1} items={[
                            { label: t('pluginHub.lockedCommit', null, '锁定 Git Commit'), value: <span className="cs-ph-mono">{artifact.commit} ({artifact.requestedRef})</span> }
                        ]} />
                    )}
                    {artifact?.driver === 'release' && (
                        <KeyValueList columns={1} items={[
                            { label: t('pluginHub.lockedReleaseAsset', null, '锁定 Release 制品'), value: <span className="cs-ph-mono">{artifact.assetURL}</span> },
                            { label: 'SHA-256', value: <span className="cs-ph-mono">{artifact.sha256} ({artifact.verification})</span> }
                        ]} />
                    )}
                    {artifact?.driver === 'container' && Array.isArray(artifact.services) && (
                        <div>
                            <Text strong>{t('pluginHub.lockedImages', null, '锁定容器镜像 Digest')}</Text>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 6 }}>
                                {artifact.services.map(svc => (
                                    <div key={svc.name} className="cs-ph-probe-card">
                                        <Space>
                                            <Tag color="blue" size="small">{svc.name}</Tag>
                                            <Text size="small" className="cs-ph-mono">{svc.requestedImage}</Text>
                                        </Space>
                                        <Text type="tertiary" size="small" className="cs-ph-mono">
                                            {svc.repository}@{svc.manifestDigest}
                                        </Text>
                                    </div>
                                ))}
                            </div>
                        </div>
                    )}

                    <div className="cs-ph-probe-card">
                        <Text strong>{t('pluginHub.declaredPermissions', null, '声明权限摘要')}</Text>
                        <Space wrap>
                            <Tag color={manifest.permissions?.persistentData ? 'amber' : 'grey'}>
                                {t('pluginHub.permPersistentData', null, '持久化数据卷')}: {manifest.permissions?.persistentData ? 'Yes' : 'No'}
                            </Tag>
                            <Tag color={manifest.permissions?.modelGateway ? 'blue' : 'grey'}>
                                {t('pluginHub.permModelGateway', null, 'CSAI 模型网关')}: {manifest.permissions?.modelGateway ? 'Yes' : 'No'}
                            </Tag>
                            {(manifest.permissions?.egress || []).map(item => (
                                <Tag key={item} color="purple">{item}</Tag>
                            ))}
                        </Space>
                    </div>

                    {llmDeclared && (
                        <div className="cs-ph-probe-card">
                            <Checkbox checked={autoBindLLM} onChange={e => setAutoBindLLM(Boolean(e.target.checked))}>
                                {t('pluginHub.autoBindDefaultLLM', null, '安装时自动复用 CSAI 默认模型通道（开箱即用，不泄露真实上游 API Key）')}
                            </Checkbox>
                            {defaultChannel ? (
                                <Text type="tertiary" size="small">
                                    {matchingDefault
                                        ? t('pluginHub.defaultChannelMatch', { name: `${defaultChannel.name} (${defaultChannel.model})` }, `当前默认通道：${defaultChannel.name} (${defaultChannel.model})，协议匹配，启动即自动注入。`)
                                        : t('pluginHub.defaultChannelMismatch', { name: `${defaultChannel.name} (${defaultChannel.protocol})` }, `当前默认通道 ${defaultChannel.name} (${defaultChannel.protocol}) 与该插件声明协议不同，安装后可在插件设置中指定通道。`)}
                                </Text>
                            ) : (
                                <Text type="tertiary" size="small">
                                    {t('pluginHub.noDefaultChannelHint', null, '尚未在 CSAI 系统设置中配置模型通道，安装后可随时在插件详情中绑定模型。')}
                                </Text>
                            )}
                        </div>
                    )}

                    <JsonViewer value={manifest} collapsed title="Manifest" />
                </div>
            )}
        </Drawer>
    );
}

export function AddCatalogDrawer({ visible, onCancel, onSaved }) {
    const t = useT();
    const [repository, setRepository] = useState('');
    const [revision, setRevision] = useState('');
    const [id, setId] = useState('');
    const [title, setTitle] = useState('');
    const [summary, setSummary] = useState('');
    const [manifestText, setManifestText] = useState('');
    const [inspecting, setInspecting] = useState(false);
    const [saving, setSaving] = useState(false);

    useEffect(() => {
        if (visible) {
            setRepository('');
            setRevision('');
            setId('');
            setTitle('');
            setSummary('');
            setManifestText('');
        }
    }, [visible]);

    const inferFromRepo = repoValue => {
        const cleaned = String(repoValue || '').trim().replace(/\.git$/i, '').replace(/\/+$/, '');
        const seg = cleaned.split('/').pop() || '';
        const slug = seg.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '');
        return { seg, slug };
    };

    const handleRepoChange = value => {
        setRepository(value);
        const { seg, slug } = inferFromRepo(value);
        if (!id && slug && /^[a-z]/.test(slug)) setId(slug);
        if (!title && seg) setTitle(seg);
    };

    const handleProbe = async () => {
        if (!repository.trim()) return;
        setInspecting(true);
        try {
            const res = await pluginApi.inspectGit(repository, revision);
            const info = res?.inspection;
            if (info?.repository) setRepository(info.repository);
            if (info?.manifest) {
                const m = info.manifest;
                if (m.metadata?.id) setId(m.metadata.id);
                if (m.metadata?.title) setTitle(m.metadata.title);
                if (m.metadata?.summary) setSummary(m.metadata.summary);
                setManifestText(JSON.stringify(m, null, 2));
                toastSuccess(t('pluginHub.catalogProbeWithManifest', null, '已探测到仓库自带 Manifest，保存后支持一键安装'));
            } else {
                const { seg, slug } = inferFromRepo(info?.repository || repository);
                if (!id && slug && /^[a-z]/.test(slug)) setId(slug);
                if (!title && seg) setTitle(seg);
                toastSuccess(t('pluginHub.catalogProbeSuccess', null, '仓库只读探测完成，已自动填充基础信息'));
            }
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.inspectFailed', null, '探测仓库失败')));
        } finally {
            setInspecting(false);
        }
    };

    const handleSave = async () => {
        if (!repository.trim() && !manifestText.trim()) {
            toastError(t('pluginHub.catalogRepoRequired', null, '请填写 GitHub 仓库地址或提供 Manifest'));
            return;
        }
        setSaving(true);
        try {
            const res = await pluginApi.saveCatalog({
                id: id.trim(),
                title: title.trim(),
                repository: repository.trim(),
                revision: revision.trim(),
                summary: summary.trim(),
                manifest: manifestText.trim()
            });
            toastSuccess(t('pluginHub.catalogSaved', null, '已保存到能力市场'));
            onSaved?.(res?.item);
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.catalogSaveFailed', null, '保存到能力市场失败')));
        } finally {
            setSaving(false);
        }
    };

    return (
        <Drawer
            title={t('pluginHub.addCatalogTitle', null, '新增能力市场插件')}
            visible={visible}
            onCancel={onCancel}
            width={680}
            footer={
                <Space>
                    <Button onClick={onCancel}>{t('common.cancel', null, '取消')}</Button>
                    <Button theme="solid" type="primary" loading={saving} onClick={handleSave}>
                        {t('pluginHub.saveToMarket', null, '保存到能力市场')}
                    </Button>
                </Space>
            }
        >
            <div className="cs-ph-drawer-stack">
                <Paragraph type="tertiary" style={{ margin: 0 }}>
                    {t('pluginHub.addCatalogDesc', null, '将公开 GitHub 仓库收录到本机能力市场。留空 Manifest 将以「支持探测导入」模式展示；提供合法 YAML/JSON Manifest 后将直接支持「一键下载安装」。')}
                </Paragraph>
                <Space style={{ width: '100%' }} align="end" wrap>
                    <div style={{ flex: 2, minWidth: 260 }}>
                        <Text strong size="small">{t('pluginHub.repositoryURL', null, 'GitHub 仓库地址')}</Text>
                        <Input
                            value={repository}
                            onChange={handleRepoChange}
                            placeholder="https://github.com/owner/repo"
                            style={{ marginTop: 4 }}
                        />
                    </div>
                    <div style={{ flex: 1, minWidth: 130 }}>
                        <Text strong size="small">{t('pluginHub.revisionOptional', null, '分支 / Tag（可选）')}</Text>
                        <Input
                            value={revision}
                            onChange={setRevision}
                            placeholder="main / v1.0.0"
                            style={{ marginTop: 4 }}
                        />
                    </div>
                    <Button loading={inspecting} onClick={handleProbe} disabled={!repository.trim()}>
                        {t('pluginHub.probeAndFill', null, '探测并自动填充')}
                    </Button>
                </Space>

                <Space style={{ width: '100%' }} wrap>
                    <div style={{ flex: 1, minWidth: 220 }}>
                        <Text strong size="small">{t('pluginHub.pluginId', null, '插件标识 ID')}</Text>
                        <Input
                            value={id}
                            onChange={setId}
                            placeholder="my-security-tool"
                            style={{ marginTop: 4 }}
                        />
                    </div>
                    <div style={{ flex: 1, minWidth: 220 }}>
                        <Text strong size="small">{t('pluginHub.pluginTitle', null, '显示名称')}</Text>
                        <Input
                            value={title}
                            onChange={setTitle}
                            placeholder="My Security Tool"
                            style={{ marginTop: 4 }}
                        />
                    </div>
                </Space>

                <div>
                    <Text strong size="small">{t('pluginHub.catalogSummaryLabel', null, '功能简介')}</Text>
                    <Input
                        value={summary}
                        onChange={setSummary}
                        placeholder={t('pluginHub.catalogSummaryPlaceholder', null, '简述该开源安全工具的用途、架构或特性')}
                        style={{ marginTop: 4 }}
                    />
                </div>

                <div>
                    <Text strong size="small">{t('pluginHub.catalogManifestOptional', null, '插件 Manifest（可选，支持 YAML 或 JSON）')}</Text>
                    <TextArea
                        className="cs-ph-code-editor"
                        value={manifestText}
                        onChange={setManifestText}
                        placeholder={t('pluginHub.catalogManifestPlaceholder', null, '可选：粘贴 csai-plugin.yaml 或 JSON 声明；点击上方「探测并自动填充」可自动抓取仓库根目录的 csai-plugin.yaml')}
                        autosize={{ minRows: 8, maxRows: 18 }}
                        style={{ marginTop: 4 }}
                    />
                </div>
            </div>
        </Drawer>
    );
}

export function ImportGitDrawer({ visible, initialEntry, onCancel, onPlanReady, onCatalogSaved }) {
    const t = useT();
    const [repository, setRepository] = useState('');
    const [revision, setRevision] = useState('');
    const [inspecting, setInspecting] = useState(false);
    const [inspected, setInspected] = useState(null);
    const [manifestText, setManifestText] = useState('');
    const [driver, setDriver] = useState('auto');
    const [isolation, setIsolation] = useState('standard');
    const [planning, setPlanning] = useState(false);
    const [savingCatalog, setSavingCatalog] = useState(false);

    useEffect(() => {
        if (visible && initialEntry?.repository) {
            setRepository(initialEntry.repository);
            setRevision(initialEntry.revision || '');
            setInspected(null);
            setManifestText('');
        }
    }, [visible, initialEntry]);

    const handleInspect = async () => {
        if (!repository.trim()) return;
        setInspecting(true);
        setInspected(null);
        try {
            const res = await pluginApi.inspectGit(repository, revision);
            setInspected(res);
            if (res?.inspection?.manifest) {
                setManifestText(JSON.stringify(res.inspection.manifest, null, 2));
            } else {
                setManifestText(starterManifestTemplate(res?.inspection));
            }
            toastSuccess(t('pluginHub.inspectSucceeded', null, '仓库只读探测完成'));
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.inspectFailed', null, '探测仓库失败')));
        } finally {
            setInspecting(false);
        }
    };

    const handleGeneratePlan = async () => {
        if (!inspected?.id) return;
        let parsedManifest;
        try {
            parsedManifest = JSON.parse(manifestText);
        } catch {
            toastError(t('pluginHub.manifestJsonInvalid', null, 'Manifest 草案需为合法 JSON 格式'));
            return;
        }
        setPlanning(true);
        try {
            const planRes = await pluginApi.createPlan({
                inspection_id: inspected.id,
                manifest: parsedManifest,
                driver,
                isolation
            });
            onPlanReady?.(planRes);
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.createPlanFailed', null, '生成锁定安装计划失败')));
        } finally {
            setPlanning(false);
        }
    };

    const handleSaveToCatalog = async () => {
        if (!inspected?.inspection) return;
        setSavingCatalog(true);
        try {
            await pluginApi.saveCatalog({
                repository: inspected.inspection.repository || repository.trim(),
                revision: revision.trim(),
                manifest: manifestText.trim()
            });
            toastSuccess(t('pluginHub.catalogSaved', null, '已保存到能力市场'));
            onCatalogSaved?.();
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.catalogSaveFailed', null, '保存到能力市场失败')));
        } finally {
            setSavingCatalog(false);
        }
    };

    const info = inspected?.inspection;

    return (
        <Drawer
            title={t('pluginHub.importTitle', null, '手工导入 Git 仓库插件')}
            visible={visible}
            onCancel={onCancel}
            width={720}
            footer={
                <Space>
                    <Button onClick={onCancel}>{t('common.cancel', null, '取消')}</Button>
                    <Button
                        loading={savingCatalog}
                        disabled={!inspected?.inspection || !manifestText.trim()}
                        onClick={handleSaveToCatalog}
                    >
                        {t('pluginHub.saveToMarket', null, '保存到能力市场')}
                    </Button>
                    <Button
                        theme="solid"
                        type="primary"
                        loading={planning}
                        disabled={!inspected?.id || !manifestText.trim()}
                        onClick={handleGeneratePlan}
                    >
                        {t('pluginHub.generatePlan', null, '生成锁定安装计划')}
                    </Button>
                </Space>
            }
        >
            <div className="cs-ph-drawer-stack">
                <Paragraph type="tertiary" style={{ margin: 0 }}>
                    {t('pluginHub.importDesc', null, '输入公开 GitHub HTTPS 仓库地址后，CSAI 会以只读方式解析 Commit、根目录 csai-plugin.yaml 及构建/容器线索，不会执行任何第三方代码。')}
                </Paragraph>
                <Space style={{ width: '100%' }} align="end" wrap>
                    <div style={{ flex: 2, minWidth: 280 }}>
                        <Text strong size="small">{t('pluginHub.repositoryURL', null, 'GitHub 仓库地址')}</Text>
                        <Input
                            value={repository}
                            onChange={setRepository}
                            placeholder="https://github.com/owner/repo"
                            style={{ marginTop: 4 }}
                        />
                    </div>
                    <div style={{ flex: 1, minWidth: 140 }}>
                        <Text strong size="small">{t('pluginHub.revisionOptional', null, '分支 / Tag / Commit（可选）')}</Text>
                        <Input
                            value={revision}
                            onChange={setRevision}
                            placeholder="main / v1.0.0"
                            style={{ marginTop: 4 }}
                        />
                    </div>
                    <Button theme="solid" type="primary" loading={inspecting} onClick={handleInspect}>
                        {t('pluginHub.inspectRepo', null, '只读探测')}
                    </Button>
                </Space>

                {info && (
                    <>
                        <div className="cs-ph-probe-card">
                            <KeyValueList columns={2} items={[
                                { label: t('pluginHub.repository', null, '标准化仓库'), value: <span className="cs-ph-mono">{info.repository}</span> },
                                { label: t('pluginHub.resolvedRef', null, '解析引用'), value: <span className="cs-ph-mono">{info.resolved_ref}</span> },
                                { label: 'Commit', value: <span className="cs-ph-mono">{info.commit}</span> },
                                { label: t('pluginHub.detectedCandidates', null, '候选驱动'), value: (info.candidates || []).map(c => <Tag key={c} size="small" style={{ marginInlineEnd: 4 }}>{c}</Tag>) }
                            ]} />
                            {info.evidence?.length > 0 && (
                                <Space wrap style={{ marginTop: 6 }}>
                                    <Text size="small" type="tertiary">{t('pluginHub.detectedFiles', null, '检测到文件')}:</Text>
                                    {info.evidence.map(file => <Tag key={file} size="small" color="cyan">{file}</Tag>)}
                                </Space>
                            )}
                            {info.release?.tag && (
                                <Text size="small" type="tertiary">
                                    Release {info.release.tag}: {(info.release.assets || []).slice(0, 6).join(', ') || '—'}
                                </Text>
                            )}
                        </div>

                        <Space wrap>
                            <div>
                                <Text strong size="small">{t('pluginHub.driver', null, '安装驱动')}</Text>
                                <Select
                                    value={driver}
                                    onChange={setDriver}
                                    style={{ width: 240, display: 'block', marginTop: 4 }}
                                    optionList={[
                                        { value: 'auto', label: driverLabel(t, 'auto') },
                                        { value: 'release', label: driverLabel(t, 'release') },
                                        { value: 'git', label: driverLabel(t, 'git') },
                                        { value: 'container', label: driverLabel(t, 'container') }
                                    ]}
                                />
                            </div>
                            <div>
                                <Text strong size="small">{t('pluginHub.isolation', null, '隔离档位')}</Text>
                                <Select
                                    value={isolation}
                                    onChange={setIsolation}
                                    style={{ width: 200, display: 'block', marginTop: 4 }}
                                    optionList={[
                                        { value: 'standard', label: t('pluginHub.isolationStandard', null, 'standard（非 root 单机默认）') },
                                        { value: 'strict', label: t('pluginHub.isolationStrict', null, 'strict（强制容器隔离）') }
                                    ]}
                                />
                            </div>
                        </Space>

                        <div>
                            <Space style={{ justifyContent: 'space-between', width: '100%', marginBottom: 4 }}>
                                <Text strong size="small">
                                    {info.manifest
                                        ? t('pluginHub.manifestFromRepo', null, '已从仓库读取 csai-plugin.yaml（可审阅）')
                                        : t('pluginHub.manifestDraftLabel', null, '仓库无自带 Manifest，请编辑以下接入草案（JSON）')}
                                </Text>
                            </Space>
                            <TextArea
                                className="cs-ph-code-editor"
                                value={manifestText}
                                onChange={setManifestText}
                                autosize={{ minRows: 12, maxRows: 22 }}
                            />
                        </div>
                    </>
                )}
            </div>
        </Drawer>
    );
}

export function PluginDetailDrawer({
    installId, initialTab = 'overview', visible, canManage, canUseAgent, channels,
    onCancel, onRestartRequested, onChanged, onAskAgentTroubleshoot
}) {
    const t = useT();
    const [activeTab, setActiveTab] = useState(initialTab);
    const [detail, setDetail] = useState(null);
    const [loading, setLoading] = useState(false);

    // LLM form state
    const [llmMode, setLlmMode] = useState('follow_default');
    const [channelId, setChannelId] = useState('');
    const [concurrency, setConcurrency] = useState(4);
    const [rpm, setRpm] = useState(60);
    const [tokenBudget, setTokenBudget] = useState(2000000);
    const [savingLLM, setSavingLLM] = useState(false);
    const [restartBanner, setRestartBanner] = useState(false);

    // Log stream state
    const [logs, setLogs] = useState([]);
    const [streaming, setStreaming] = useState(false);
    const logBoxRef = useRef(null);
    const abortRef = useRef(null);

    useEffect(() => {
        if (visible) setActiveTab(initialTab || 'overview');
    }, [visible, initialTab, installId]);

    const loadDetail = async () => {
        if (!installId) return;
        setLoading(true);
        try {
            const data = await pluginApi.getInstall(installId);
            setDetail(data);
            const first = Array.isArray(data?.llm_bindings) ? data.llm_bindings[0] : null;
            if (first) {
                setLlmMode(first.mode || 'follow_default');
                setChannelId(first.channel_id || '');
                setConcurrency(first.concurrency || 4);
                setRpm(first.rpm || 60);
                setTokenBudget(first.token_budget || 2000000);
            } else {
                setLlmMode('follow_default');
                setChannelId('');
            }
            setRestartBanner(false);
        } catch (err) {
            toastError(pluginErrorText(t, err));
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        if (visible && installId) loadDetail();
    }, [visible, installId]); // eslint-disable-line react-hooks/exhaustive-deps

    useEffect(() => {
        if (!visible || !installId || activeTab !== 'logs' || !canManage) {
            abortRef.current?.();
            abortRef.current = null;
            setStreaming(false);
            return undefined;
        }
        let active = true;
        setLogs([]);
        (async () => {
            try {
                const initial = await pluginApi.readLogs(installId, 0, 200);
                if (!active) return;
                const items = Array.isArray(initial?.items) ? initial.items : [];
                setLogs(items);
                setStreaming(true);
                abortRef.current = streamPluginLogs(installId, {
                    after: initial?.next_cursor || 0,
                    onEntry: entry => {
                        setLogs(prev => [...prev.slice(-499), entry]);
                    },
                    onClose: () => setStreaming(false),
                    onError: () => setStreaming(false)
                });
            } catch (err) {
                if (active) toastError(pluginErrorText(t, err));
            }
        })();
        return () => {
            active = false;
            abortRef.current?.();
            abortRef.current = null;
            setStreaming(false);
        };
    }, [visible, installId, activeTab, canManage]); // eslint-disable-line react-hooks/exhaustive-deps

    useEffect(() => {
        if (logBoxRef.current) {
            logBoxRef.current.scrollTop = logBoxRef.current.scrollHeight;
        }
    }, [logs]);

    const saveLLM = async () => {
        if (!installId) return;
        setSavingLLM(true);
        try {
            const body = {
                mode: llmMode,
                channel_id: llmMode === 'channel' ? channelId : '',
                limits: {
                    concurrency: Number(concurrency) || 4,
                    rpm: Number(rpm) || 60,
                    token_budget: Number(tokenBudget) || 2000000
                }
            };
            const res = await pluginApi.patchLLM(installId, body);
            toastSuccess(t('pluginHub.llmSaved', null, '模型通道绑定已更新'));
            setRestartBanner(Boolean(res?.restart_required));
            await loadDetail();
            onChanged?.();
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.llmSaveFailed', null, '保存模型配置失败')));
        } finally {
            setSavingLLM(false);
        }
    };

    const declaredProtocols = useMemo(
        () => new Set((detail?.llm_declared?.bindings || []).map(b => b.protocol)),
        [detail]
    );
    const compatibleChannels = useMemo(
        () => (channels || []).filter(ch => declaredProtocols.size === 0 || declaredProtocols.has(ch.protocol)),
        [channels, declaredProtocols]
    );

    const logText = useMemo(
        () => logs.map(item => `[${item.created_at || ''}] ${item.line}`).join('\n'),
        [logs]
    );

    return (
        <Drawer
            title={detail?.title || detail?.plugin_id || t('pluginHub.detailTitle', null, '插件详情')}
            visible={visible}
            onCancel={onCancel}
            width={760}
        >
            <Tabs activeKey={activeTab} onChange={setActiveTab}>
                <TabPane tab={t('pluginHub.tabOverview', null, '概览与权限')} itemKey="overview">
                    {detail && (
                        <div className="cs-ph-drawer-stack" style={{ marginTop: 12 }}>
                            {(detail.observed_state === 'failed' || detail.observed_state === 'degraded') && (
                                <Banner
                                    type="danger"
                                    bordered
                                    closeIcon={null}
                                    title={pluginErrorText(t, detail.last_error_code, t('pluginHub.errorInstallFailed', null, '插件安装或运行异常'))}
                                    description={
                                        <Space wrap style={{ marginTop: 4 }}>
                                            {canUseAgent && onAskAgentTroubleshoot && (
                                                <Button
                                                    size="small"
                                                    theme="solid"
                                                    type="primary"
                                                    icon={<IconBolt />}
                                                    onClick={() => onAskAgentTroubleshoot(detail)}
                                                >
                                                    {t('pluginHub.askAgentTroubleshoot', null, '交给 CSAI Agent 排错并修复')}
                                                </Button>
                                            )}
                                            {canManage && (
                                                <Button size="small" onClick={() => setActiveTab('logs')}>
                                                    {t('pluginHub.viewLogs', null, '查看运行日志')}
                                                </Button>
                                            )}
                                        </Space>
                                    }
                                />
                            )}
                            <KeyValueList columns={2} items={[
                                { label: t('pluginHub.installId', null, '实例 ID'), value: <span className="cs-ph-mono">{detail.id}</span> },
                                { label: t('pluginHub.pluginId', null, '插件标识'), value: detail.plugin_id },
                                { label: t('pluginHub.version', null, '版本'), value: detail.version || '—' },
                                { label: t('pluginHub.driver', null, '驱动'), value: driverLabel(t, detail.driver) },
                                { label: t('pluginHub.observedState', null, '运行状态'), value: <Tag color={stateColor(detail.observed_state)}>{stateLabel(t, detail.observed_state)}</Tag> },
                                { label: t('pluginHub.proxyMode', null, '代理隔离模式'), value: detail.proxy_mode || 'port' },
                                { label: t('pluginHub.repository', null, '源码仓库'), value: detail.repository ? <span className="cs-ph-mono">{detail.repository}</span> : '—' },
                                { label: t('pluginHub.lastError', null, '最近错误'), value: detail.last_error_code || '—' }
                            ]} />
                            {detail.summary && <Paragraph type="tertiary">{detail.summary}</Paragraph>}
                            {detail.permissions && (
                                <div className="cs-ph-probe-card">
                                    <Text strong>{t('pluginHub.declaredPermissions', null, '声明权限')}</Text>
                                    <Space wrap>
                                        <Tag>{t('pluginHub.permPersistentData', null, '持久化数据卷')}: {detail.permissions.persistentData ? 'Yes' : 'No'}</Tag>
                                        <Tag>{t('pluginHub.permModelGateway', null, 'CSAI 模型网关')}: {detail.permissions.modelGateway ? 'Yes' : 'No'}</Tag>
                                        {(detail.permissions.egress || []).map(e => <Tag key={e} color="purple">{e}</Tag>)}
                                    </Space>
                                </div>
                            )}
                            {detail.extensions?.capabilities?.length > 0 && (
                                <div className="cs-ph-probe-card">
                                    <Text strong>{t('pluginHub.declaredExtensions', null, '声明的扩展能力（MCP / OpenAPI 预留）')}</Text>
                                    <Space wrap>
                                        {detail.extensions.capabilities.map(cap => (
                                            <Tag key={cap.id} color="cyan">{cap.id} ({cap.transport} {cap.path})</Tag>
                                        ))}
                                    </Space>
                                </div>
                            )}
                        </div>
                    )}
                </TabPane>

                {detail?.llm_declared && (
                    <TabPane tab={t('pluginHub.tabLLM', null, 'CSAI 模型绑定')} itemKey="llm">
                        <div className="cs-ph-drawer-stack" style={{ marginTop: 12 }}>
                            {restartBanner && (
                                <Banner
                                    type="info"
                                    bordered
                                    closeIcon={null}
                                    description={
                                        <Space wrap>
                                            <span>{t('pluginHub.restartRequiredForLLM', null, '模型绑定已更新，重启插件后将立即注入新的 CSAI 网关凭据。')}</span>
                                            {canManage && onRestartRequested && (
                                                <Button size="small" theme="solid" type="primary" onClick={() => onRestartRequested(detail)}>
                                                    {t('pluginHub.restartNow', null, '立即重启生效')}
                                                </Button>
                                            )}
                                        </Space>
                                    }
                                />
                            )}
                            <Paragraph type="tertiary" style={{ margin: 0 }}>
                                {t('pluginHub.llmBindingDesc', null, 'CSAI 通过本地模型网关向插件注入隔离 Token 与通道配置，插件无法接触您的真实上游 API Key。')}
                            </Paragraph>
                            <RadioGroup value={llmMode} onChange={e => setLlmMode(e.target.value)} disabled={!canManage}>
                                <Radio value="follow_default">{t('pluginHub.llmModeFollowDefault', null, '跟随 CSAI 默认模型通道')}</Radio>
                                <Radio value="channel">{t('pluginHub.llmModeChannel', null, '指定 CSAI 模型通道')}</Radio>
                                <Radio value="disabled">{t('pluginHub.llmModeDisabled', null, '关闭模型托管')}</Radio>
                            </RadioGroup>

                            {llmMode === 'channel' && (
                                <div>
                                    <Text strong size="small">{t('pluginHub.selectChannel', null, '选择兼容通道')}</Text>
                                    <Select
                                        value={channelId}
                                        onChange={setChannelId}
                                        disabled={!canManage}
                                        style={{ width: '100%', marginTop: 4 }}
                                        placeholder={t('pluginHub.selectChannelPlaceholder', null, '请选择在 CSAI 系统设置中配置的模型通道')}
                                        optionList={compatibleChannels.map(ch => ({
                                            value: ch.id,
                                            label: `${ch.name} (${ch.model} · ${ch.provider})${ch.is_default ? ' [Default]' : ''}`
                                        }))}
                                    />
                                </div>
                            )}

                            {llmMode !== 'disabled' && (
                                <Space wrap>
                                    <div>
                                        <Text size="small" type="tertiary">{t('pluginHub.limitConcurrency', null, '并发上限')}</Text>
                                        <InputNumber value={concurrency} onChange={setConcurrency} min={1} max={32} disabled={!canManage} style={{ display: 'block', marginTop: 4 }} />
                                    </div>
                                    <div>
                                        <Text size="small" type="tertiary">{t('pluginHub.limitRPM', null, '每分钟请求 (RPM)')}</Text>
                                        <InputNumber value={rpm} onChange={setRpm} min={1} max={1000} disabled={!canManage} style={{ display: 'block', marginTop: 4 }} />
                                    </div>
                                    <div>
                                        <Text size="small" type="tertiary">{t('pluginHub.limitTokenBudget', null, '每日 Token 预算')}</Text>
                                        <InputNumber value={tokenBudget} onChange={setTokenBudget} min={1000} step={100000} disabled={!canManage} style={{ display: 'block', marginTop: 4, width: 180 }} />
                                    </div>
                                </Space>
                            )}

                            {canManage && (
                                <div>
                                    <Button theme="solid" type="primary" loading={savingLLM} onClick={saveLLM}>
                                        {t('pluginHub.saveLLM', null, '保存模型配置')}
                                    </Button>
                                </div>
                            )}
                        </div>
                    </TabPane>
                )}

                {canManage && (
                    <TabPane tab={t('pluginHub.logs', null, '运行日志')} itemKey="logs">
                        <div className="cs-ph-drawer-stack" style={{ marginTop: 12 }}>
                            <Space style={{ justifyContent: 'space-between', width: '100%' }}>
                                <Space>
                                    <Tag color={streaming ? 'green' : 'grey'}>
                                        {streaming ? t('pluginHub.logStreaming', null, 'SSE 实时追踪中') : t('pluginHub.logPaused', null, '已停止追踪')}
                                    </Tag>
                                    <Text size="small" type="tertiary">{logs.length} lines</Text>
                                </Space>
                                <Space>
                                    <CopyButton text={logText} label={t('common.copy', null, '复制')} />
                                    <Button size="small" icon={<IconRefresh />} onClick={() => setActiveTab('overview') || setTimeout(() => setActiveTab('logs'), 0)}>
                                        {t('common.refresh', null, '刷新')}
                                    </Button>
                                </Space>
                            </Space>
                            <pre ref={logBoxRef} className="cs-ph-log-box">
                                {logText || t('pluginHub.noLogs', null, '暂无运行日志')}
                            </pre>
                        </div>
                    </TabPane>
                )}
            </Tabs>
            {loading && !detail && <Text type="tertiary">{t('common.loading', null, '加载中…')}</Text>}
        </Drawer>
    );
}

export function RuntimeProbesDrawer({ visible, canUseAgent, onCancel, onAskAgentDriverFix }) {
    const t = useT();
    const [isolation, setIsolation] = useState('standard');
    const [data, setData] = useState(null);
    const [loading, setLoading] = useState(false);

    const load = async (targetIsolation = isolation) => {
        setLoading(true);
        try {
            const res = await pluginApi.probes(targetIsolation);
            setData(res);
        } catch (err) {
            toastError(pluginErrorText(t, err));
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        if (visible) load(isolation);
    }, [visible, isolation]); // eslint-disable-line react-hooks/exhaustive-deps

    return (
        <Drawer
            title={t('pluginHub.runtimeProbesTitle', null, '宿主运行环境与驱动预检')}
            visible={visible}
            onCancel={onCancel}
            width={660}
        >
            <div className="cs-ph-drawer-stack">
                <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
                    <RadioGroup type="button" value={isolation} onChange={e => setIsolation(e.target.value)}>
                        <Radio value="standard">standard</Radio>
                        <Radio value="strict">strict</Radio>
                    </RadioGroup>
                    <Button icon={<IconRefresh />} loading={loading} onClick={() => load(isolation)}>
                        {t('common.refresh', null, '重新检测')}
                    </Button>
                </Space>
                {data && (
                    <>
                        <KeyValueList columns={2} items={[
                            { label: t('pluginHub.hostPlatform', null, '宿主平台'), value: `${data.os}/${data.arch}` },
                            {
                                label: t('pluginHub.workerReady', null, '运行调度器'),
                                value: (
                                    <Tag color={data.execution_enabled ? 'green' : 'amber'}>
                                        {data.execution_enabled ? t('pluginHub.ready', null, '就绪') : t('pluginHub.notReady', null, '未就绪')}
                                    </Tag>
                                )
                            }
                        ]} />
                        {Object.entries(data.drivers || {}).map(([name, probe]) => (
                            <div key={name} className="cs-ph-probe-card">
                                <Space style={{ justifyContent: 'space-between', width: '100%' }}>
                                    <Text strong>{driverLabel(t, name)}</Text>
                                    <Tag color={probe.available ? 'green' : 'red'} prefixIcon={probe.available ? <IconCheckCircleStroked /> : <IconStop />}>
                                        {probe.available ? t('pluginHub.available', null, '可用') : (probe.code || t('pluginHub.unavailable', null, '不可用'))}
                                    </Tag>
                                </Space>
                                {!probe.available && probe.code && (
                                    <Text type="danger" size="small">{pluginErrorText(t, probe.code, probe.guidance)}</Text>
                                )}
                                {probe.guidance && <Text type="tertiary" size="small">{probe.guidance}</Text>}
                                {!probe.available && probe.install_command && (
                                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', marginTop: 4 }}>
                                        <code className="cs-ph-mono" style={{ fontSize: 12, padding: '4px 8px', borderRadius: 4, background: 'var(--semi-color-fill-1)', wordBreak: 'break-all' }}>
                                            {probe.install_command}
                                        </code>
                                        <CopyButton text={probe.install_command} label={t('common.copy', null, '复制')} />
                                    </div>
                                )}
                                {!probe.available && (canUseAgent || probe.download_url) && (
                                    <Space wrap style={{ marginTop: 6 }}>
                                        {canUseAgent && onAskAgentDriverFix && (
                                            <Button
                                                size="small"
                                                theme="solid"
                                                type="primary"
                                                icon={<IconBolt />}
                                                onClick={() => onAskAgentDriverFix({
                                                    driver: name,
                                                    probe,
                                                    hostOS: data.os,
                                                    hostArch: data.arch
                                                })}
                                            >
                                                {t('pluginHub.askAgentInstallDriver', null, '交给 CSAI Agent 自动安装配置')}
                                            </Button>
                                        )}
                                        {probe.download_url && (
                                            <Button
                                                size="small"
                                                icon={<IconExternalOpen />}
                                                onClick={() => window.open(probe.download_url, '_blank', 'noopener,noreferrer')}
                                            >
                                                {t('pluginHub.officialDownload', null, '官方下载')}
                                            </Button>
                                        )}
                                    </Space>
                                )}
                            </div>
                        ))}
                        {data.drivers?.container && !data.drivers.container.available && (
                            <Banner
                                type="info"
                                bordered
                                closeIcon={null}
                                description={
                                    data.os === 'linux'
                                        ? t('pluginHub.dockerInstallLinuxHint', null, 'Linux / Kali 宿主可直接点击上方「交给 CSAI Agent 自动安装配置」，或手动安装 docker.io 与 docker-compose-plugin 并启动 dockerd，完成后点击「重新检测」。')
                                        : t('pluginHub.dockerInstallDesktopHint', null, 'macOS / Windows 可点击「官方下载」安装并启动 Docker Desktop（或交给 Agent 通过 Homebrew/winget 协助安装），也可优先选择 Release / Git 原生驱动。')
                                }
                            />
                        )}
                    </>
                )}
            </div>
        </Drawer>
    );
}
