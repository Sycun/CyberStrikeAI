import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
    Banner, Button, Checkbox, Input, Space, Tabs, TabPane, Tag, Tooltip, Typography
} from '@douyinfe/semi-ui';
import {
    IconApps, IconBolt, IconDelete, IconDownload, IconExternalOpen, IconPlay, IconPlus, IconRefresh,
    IconSearch, IconSetting, IconStop, IconSync, IconTerminal, IconWrench
} from '@douyinfe/semi-icons';
import { useAuth } from '../../lib/auth';
import { useT } from '../../lib/i18n';
import { useInterval, useResource } from '../../components/hooks';
import {
    DataTable, ErrorBanner, PageHeader, SectionCard, StatGrid, confirmAction, toastError, toastSuccess
} from '../../components/ui';
import { formatDateTime } from '../../lib/format';
import { driverLabel, pluginApi, pluginErrorText, stateColor, stateLabel } from './api';
import WorkspaceView from './WorkspaceView';
import { AddCatalogDrawer, ImportGitDrawer, PlanApprovalDrawer, PluginDetailDrawer, RuntimeProbesDrawer } from './Drawers';
import './page.css';

const { Text, Paragraph } = Typography;

export default function PluginHubPage() {
    const t = useT();
    const { hasPermission } = useAuth();
    const canManage = hasPermission('plugins:manage');
    const canUse = hasPermission('plugins:use');
    const [searchParams, setSearchParams] = useSearchParams();

    const activeAppId = searchParams.get('app') || '';
    const activeTab = searchParams.get('tab') || 'installed';

    const [search, setSearch] = useState('');
    const [importOpen, setImportOpen] = useState(false);
    const [importInitialEntry, setImportInitialEntry] = useState(null);
    const [addCatalogOpen, setAddCatalogOpen] = useState(false);
    const [restoringCatalog, setRestoringCatalog] = useState(false);
    const [probesOpen, setProbesOpen] = useState(false);
    const [planState, setPlanState] = useState(null);
    const [planningCatalogId, setPlanningCatalogId] = useState('');
    const [detailTarget, setDetailTarget] = useState(null);
    const [activeOp, setActiveOp] = useState(null);
    const [busyInstallId, setBusyInstallId] = useState('');

    const installsRes = useResource(() => pluginApi.listInstalls(), []);
    const catalogRes = useResource(() => pluginApi.listCatalog(), []);
    const channelsRes = useResource(() => pluginApi.listLLMChannels(), []);
    const probesRes = useResource(() => pluginApi.probes('standard'), [], { skip: !canManage });

    const installs = Array.isArray(installsRes.data?.items) ? installsRes.data.items : [];
    const catalogItems = Array.isArray(catalogRes.data?.items) ? catalogRes.data.items : [];
    const hiddenBuiltins = Number(catalogRes.data?.hidden_builtins || 0);
    const channels = Array.isArray(channelsRes.data?.channels) ? channelsRes.data.channels : [];

    const activeApp = useMemo(
        () => installs.find(item => item.id === activeAppId) || null,
        [installs, activeAppId]
    );

    const setTab = nextTab => {
        const next = new URLSearchParams(searchParams);
        next.set('tab', nextTab);
        next.delete('app');
        setSearchParams(next, { replace: true });
    };

    const openApp = install => {
        const next = new URLSearchParams(searchParams);
        next.set('tab', 'installed');
        next.set('app', install.id);
        setSearchParams(next);
    };

    const closeApp = () => {
        const next = new URLSearchParams(searchParams);
        next.delete('app');
        setSearchParams(next, { replace: true });
    };

    const reloadAll = useCallback(() => {
        installsRes.reload({ silent: true });
        catalogRes.reload({ silent: true });
        channelsRes.reload({ silent: true });
    }, [installsRes, catalogRes, channelsRes]);

    // Poll active asynchronous operation until terminal state
    useInterval(async () => {
        if (!activeOp?.id || !canManage) return;
        try {
            const latest = await pluginApi.getOperation(activeOp.id);
            setActiveOp(latest);
            if (latest.status !== 'queued' && latest.status !== 'running') {
                installsRes.reload({ silent: true });
                if (latest.status === 'succeeded') {
                    toastSuccess(t('pluginHub.opSucceeded', { kind: latest.kind }, `操作 ${latest.kind} 已完成`));
                } else if (latest.status === 'failed') {
                    toastError(t('pluginHub.opFailed', { kind: latest.kind }, `操作 ${latest.kind} 失败，请查看插件日志`));
                }
            }
        } catch {
            // keep last known status
        }
    }, activeOp && (activeOp.status === 'queued' || activeOp.status === 'running') ? 1500 : null);

    // Light background refresh when any install is transitioning
    const hasTransitioning = installs.some(i => ['installing', 'starting', 'stopping'].includes(i.observed_state));
    useInterval(() => {
        installsRes.reload({ silent: true });
    }, hasTransitioning ? 3000 : null);

    const handleCatalogInstall = async entry => {
        if (entry.availability !== 'plan_available') {
            setImportInitialEntry({ repository: entry.repository, revision: entry.revision || '' });
            setImportOpen(true);
            return;
        }
        setPlanningCatalogId(entry.id);
        try {
            const planRes = await pluginApi.createPlan({
                catalog_id: entry.id,
                driver: 'auto',
                isolation: 'standard'
            });
            setPlanState(planRes);
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.createPlanFailed', null, '生成安装计划失败')));
        } finally {
            setPlanningCatalogId('');
        }
    };

    const confirmDeleteCatalog = entry => {
        confirmAction({
            title: t('pluginHub.confirmDeleteCatalogTitle', { name: entry.title || entry.id }, `从能力市场移除「${entry.title || entry.id}」？`),
            content: (
                <Text type="tertiary">
                    {entry.builtin
                        ? t('pluginHub.confirmHideBuiltinDesc', null, '该条目为内置精选目录项，移除后将在市场中隐藏，可随时通过「恢复默认精选」找回；不会影响已安装的实例。')
                        : t('pluginHub.confirmDeleteCustomDesc', null, '将从能力市场永久删除该自定义条目，不会影响已安装的插件实例。')}
                </Text>
            ),
            okText: t('common.delete', null, '删除'),
            onOk: async () => {
                try {
                    await pluginApi.deleteCatalog(entry.id);
                    toastSuccess(t('pluginHub.catalogDeleted', null, '已从能力市场移除'));
                    catalogRes.reload({ silent: true });
                } catch (err) {
                    toastError(pluginErrorText(t, err, t('pluginHub.catalogDeleteFailed', null, '删除市场条目失败')));
                }
            }
        });
    };

    const handleRestoreBuiltins = async () => {
        setRestoringCatalog(true);
        try {
            await pluginApi.restoreCatalog();
            toastSuccess(t('pluginHub.catalogRestored', null, '已恢复全部默认精选目录'));
            catalogRes.reload({ silent: true });
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.catalogRestoreFailed', null, '恢复默认精选失败')));
        } finally {
            setRestoringCatalog(false);
        }
    };

    const runOperation = async (install, kind, extra = {}) => {
        setBusyInstallId(install.id);
        try {
            const op = await pluginApi.operate(install.id, kind, extra);
            setActiveOp(op);
            toastSuccess(t('pluginHub.opQueued', { kind }, `已提交 ${kind} 任务`));
            installsRes.reload({ silent: true });
        } catch (err) {
            toastError(pluginErrorText(t, err));
        } finally {
            setBusyInstallId('');
        }
    };

    const confirmUninstall = install => {
        let purgeData = false;
        confirmAction({
            title: t('pluginHub.confirmUninstallTitle', { name: install.title || install.plugin_id }, `卸载插件「${install.title || install.plugin_id}」？`),
            content: (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    <Text type="tertiary">
                        {t('pluginHub.confirmUninstallDesc', null, '将停止插件进程/容器、撤销代理会话与模型网关 Token，并退役隔离端口。')}
                    </Text>
                    <Checkbox onChange={e => { purgeData = Boolean(e.target.checked); }}>
                        {t('pluginHub.purgeDataOption', null, '同时删除持久化运行数据与卷（默认保留数据）')}
                    </Checkbox>
                </div>
            ),
            okText: t('pluginHub.uninstall', null, '卸载'),
            onOk: () => runOperation(install, 'uninstall', { purge_data: purgeData })
        });
    };

    const filteredInstalls = useMemo(() => {
        const kw = search.trim().toLowerCase();
        if (!kw) return installs;
        return installs.filter(item =>
            String(item.title || '').toLowerCase().includes(kw) ||
            String(item.plugin_id || '').toLowerCase().includes(kw) ||
            String(item.driver || '').toLowerCase().includes(kw)
        );
    }, [installs, search]);

    const filteredCatalog = useMemo(() => {
        const kw = search.trim().toLowerCase();
        if (!kw) return catalogItems;
        return catalogItems.filter(item =>
            String(item.title || '').toLowerCase().includes(kw) ||
            String(item.id || '').toLowerCase().includes(kw) ||
            String(item.summary || '').toLowerCase().includes(kw)
        );
    }, [catalogItems, search]);

    const healthyCount = installs.filter(i => i.observed_state === 'healthy').length;
    const probes = probesRes.data?.drivers || {};
    const readyDrivers = Object.entries(probes).filter(([, p]) => p?.available).map(([k]) => k);

    // Render embedded full-area workspace when an app is open
    if (activeAppId && activeApp) {
        return (
            <div className="cs-ph-root">
                <WorkspaceView
                    install={activeApp}
                    canManage={canManage}
                    onBack={closeApp}
                    onOpenDetail={() => setDetailTarget({ id: activeApp.id, tab: 'overview' })}
                    onOpenLogs={() => setDetailTarget({ id: activeApp.id, tab: 'logs' })}
                />
                <PluginDetailDrawer
                    installId={detailTarget?.id}
                    initialTab={detailTarget?.tab}
                    visible={Boolean(detailTarget)}
                    canManage={canManage}
                    channels={channels}
                    onCancel={() => setDetailTarget(null)}
                    onRestartRequested={item => runOperation(item, 'restart')}
                    onChanged={reloadAll}
                />
            </div>
        );
    }

    const installColumns = [
        {
            title: t('pluginHub.pluginTitle', null, '插件应用'),
            dataIndex: 'title',
            render: (value, record) => (
                <Space vertical spacing={2} align="start">
                    <Space spacing={6}>
                        <Text strong>{value || record.plugin_id}</Text>
                        <Tag size="small" color="grey">{record.plugin_id}</Tag>
                    </Space>
                    {record.last_error_code && (
                        <Text type="danger" size="small">{record.last_error_code}</Text>
                    )}
                </Space>
            )
        },
        {
            title: t('pluginHub.observedState', null, '运行状态'),
            dataIndex: 'observed_state',
            width: 130,
            render: value => <Tag color={stateColor(value)} shape="square">{stateLabel(t, value)}</Tag>
        },
        {
            title: t('pluginHub.driver', null, '驱动'),
            dataIndex: 'driver',
            width: 160,
            render: value => driverLabel(t, value)
        },
        {
            title: t('pluginHub.updatedAt', null, '更新时间'),
            dataIndex: 'updated_at',
            width: 170,
            render: value => formatDateTime(value)
        },
        {
            title: t('common.actions', null, '操作'),
            width: 300,
            render: (_, record) => {
                const isRunning = record.observed_state === 'healthy';
                const isBusy = busyInstallId === record.id || ['installing', 'starting', 'stopping'].includes(record.observed_state);
                return (
                    <Space spacing={4} wrap>
                        {canUse && (
                            <Button
                                size="small"
                                theme="solid"
                                type="primary"
                                icon={<IconExternalOpen />}
                                disabled={!isRunning}
                                onClick={() => openApp(record)}
                            >
                                {t('pluginHub.openWorkspace', null, '打开应用')}
                            </Button>
                        )}
                        {canManage && !isRunning && (
                            <Tooltip content={t('pluginHub.start', null, '启动')}>
                                <Button size="small" icon={<IconPlay />} loading={isBusy} onClick={() => runOperation(record, 'start')} />
                            </Tooltip>
                        )}
                        {canManage && isRunning && (
                            <>
                                <Tooltip content={t('pluginHub.restart', null, '重启')}>
                                    <Button size="small" icon={<IconSync />} loading={isBusy} onClick={() => runOperation(record, 'restart')} />
                                </Tooltip>
                                <Tooltip content={t('pluginHub.stop', null, '停止')}>
                                    <Button size="small" icon={<IconStop />} loading={isBusy} onClick={() => runOperation(record, 'stop')} />
                                </Tooltip>
                            </>
                        )}
                        {canManage && (
                            <Tooltip content={t('pluginHub.logs', null, '实时日志')}>
                                <Button size="small" theme="borderless" icon={<IconTerminal />} onClick={() => setDetailTarget({ id: record.id, tab: 'logs' })} />
                            </Tooltip>
                        )}
                        <Tooltip content={t('pluginHub.settingsAndDetail', null, '详情与模型绑定')}>
                            <Button size="small" theme="borderless" icon={<IconSetting />} onClick={() => setDetailTarget({ id: record.id, tab: 'overview' })} />
                        </Tooltip>
                        {canManage && (
                            <Tooltip content={t('pluginHub.uninstall', null, '卸载')}>
                                <Button size="small" theme="borderless" type="danger" icon={<IconDelete />} onClick={() => confirmUninstall(record)} />
                            </Tooltip>
                        )}
                    </Space>
                );
            }
        }
    ];

    return (
        <div className="cs-ph-root">
            <PageHeader
                title={t('pluginHub.title', null, '插件化能力平台')}
                description={t('pluginHub.subtitle', null, '按需拉取独立红队与安全开源项目，保留原作者仓库隔离，支持页面无缝内嵌与 CSAI 模型通道统一复用。')}
                extra={
                    <Space wrap>
                        {canManage && (
                            <Button icon={<IconWrench />} onClick={() => setProbesOpen(true)}>
                                {t('pluginHub.runtimeProbes', null, '运行环境预检')}
                            </Button>
                        )}
                        {canManage && (
                            <Button theme="solid" type="primary" icon={<IconPlus />} onClick={() => setImportOpen(true)}>
                                {t('pluginHub.importGit', null, '手工导入 Git 仓库')}
                            </Button>
                        )}
                        <Button icon={<IconRefresh />} onClick={reloadAll} loading={installsRes.loading || catalogRes.loading}>
                            {t('common.refresh', null, '刷新')}
                        </Button>
                    </Space>
                }
            />

            <StatGrid
                columns={4}
                metrics={[
                    {
                        id: 'installed',
                        label: t('pluginHub.statInstalled', null, '已安装插件'),
                        value: installs.length,
                        icon: <IconApps />,
                        onClick: () => setTab('installed')
                    },
                    {
                        id: 'healthy',
                        label: t('pluginHub.statRunning', null, '运行中应用'),
                        value: healthyCount,
                        tone: healthyCount > 0 ? 'success' : undefined,
                        icon: <IconPlay />,
                        onClick: () => setTab('installed')
                    },
                    {
                        id: 'catalog',
                        label: t('pluginHub.statCatalog', null, '官方精选目录'),
                        value: catalogItems.length,
                        icon: <IconDownload />,
                        onClick: () => setTab('market')
                    },
                    {
                        id: 'drivers',
                        label: t('pluginHub.statDrivers', null, '宿主可用驱动'),
                        value: canManage ? (readyDrivers.length ? readyDrivers.join(' / ') : '—') : '—',
                        detail: probesRes.data ? `${probesRes.data.os}/${probesRes.data.arch}` : undefined,
                        icon: <IconBolt />,
                        onClick: canManage ? () => setProbesOpen(true) : undefined
                    }
                ]}
            />

            {activeOp && (
                <Banner
                    type={activeOp.status === 'failed' ? 'danger' : activeOp.status === 'succeeded' ? 'success' : 'info'}
                    bordered
                    onClose={() => setActiveOp(null)}
                    description={
                        <Space wrap>
                            <Text strong>{t('pluginHub.latestOperation', null, '最近任务')}: {activeOp.kind}</Text>
                            <Tag size="small" color={activeOp.status === 'succeeded' ? 'green' : activeOp.status === 'failed' ? 'red' : 'blue'}>
                                {activeOp.status}
                            </Tag>
                            {canManage && activeOp.install_id && (
                                <Button size="small" theme="borderless" onClick={() => setDetailTarget({ id: activeOp.install_id, tab: 'logs' })}>
                                    {t('pluginHub.viewLogs', null, '查看日志')}
                                </Button>
                            )}
                        </Space>
                    }
                />
            )}

            <SectionCard>
                <ErrorBanner error={installsRes.error || catalogRes.error} onRetry={reloadAll} />
                <Tabs
                    activeKey={activeTab}
                    onChange={setTab}
                    tabBarExtraContent={
                        <Input
                            prefix={<IconSearch />}
                            value={search}
                            onChange={setSearch}
                            placeholder={t('pluginHub.searchPlaceholder', null, '搜索插件名称或描述')}
                            style={{ width: 260 }}
                            showClear
                        />
                    }
                >
                    <TabPane tab={`${t('pluginHub.tabInstalled', null, '已安装应用')} (${installs.length})`} itemKey="installed">
                        <div style={{ marginTop: 12 }}>
                            <DataTable
                                columns={installColumns}
                                dataSource={filteredInstalls}
                                loading={installsRes.loading}
                                emptyText={t('pluginHub.emptyInstalled', null, '暂未安装插件，可切换到「能力市场」一键下载或手工导入 Git 仓库')}
                            />
                        </div>
                    </TabPane>

                    <TabPane tab={`${t('pluginHub.marketTitle', null, '能力市场')} (${catalogItems.length})`} itemKey="market">
                        {canManage && (
                            <Space style={{ justifyContent: 'space-between', width: '100%', marginTop: 12 }} wrap>
                                <Text type="tertiary" size="small">
                                    {t('pluginHub.marketManageHint', null, '支持自由新增开源仓库或删除/隐藏条目；已安装的插件不受市场目录变更影响。')}
                                </Text>
                                <Space wrap>
                                    {hiddenBuiltins > 0 && (
                                        <Button size="small" icon={<IconRefresh />} loading={restoringCatalog} onClick={handleRestoreBuiltins}>
                                            {t('pluginHub.restoreBuiltins', { count: hiddenBuiltins }, `恢复默认精选 (${hiddenBuiltins})`)}
                                        </Button>
                                    )}
                                    <Button size="small" theme="solid" type="primary" icon={<IconPlus />} onClick={() => setAddCatalogOpen(true)}>
                                        {t('pluginHub.addCatalogBtn', null, '新增市场插件')}
                                    </Button>
                                </Space>
                            </Space>
                        )}
                        <div className="cs-ph-catalog-grid" style={{ marginTop: 14 }}>
                            {filteredCatalog.length === 0 ? (
                                <Text type="tertiary" style={{ padding: '16px 0' }}>
                                    {t('pluginHub.emptyMarket', null, '当前能力市场暂无匹配条目，可点击「新增市场插件」添加。')}
                                </Text>
                            ) : filteredCatalog.map(entry => {
                                const existing = installs.find(i => i.plugin_id === entry.id);
                                const oneClick = entry.availability === 'plan_available';
                                return (
                                    <div key={entry.id} className="cs-ph-card">
                                        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                                            <div className="cs-ph-card-head">
                                                <div>
                                                    <Space spacing={6}>
                                                        <Text strong size="normal">{entry.title}</Text>
                                                        {!entry.builtin && (
                                                            <Tag size="small" color="violet">{t('pluginHub.catalogCustomBadge', null, '自定义')}</Tag>
                                                        )}
                                                    </Space>
                                                    <div>
                                                        <Text type="tertiary" size="small" className="cs-ph-mono">{entry.id}</Text>
                                                    </div>
                                                </div>
                                                <Tag size="small" color={oneClick ? 'green' : 'blue'}>
                                                    {oneClick
                                                        ? t('pluginHub.catalogOneClick', null, '支持一键安装')
                                                        : t('pluginHub.catalogInspectOnly', null, '支持探测导入')}
                                                </Tag>
                                            </div>
                                            <Paragraph type="tertiary" size="small" style={{ margin: 0 }}>
                                                {entry.summary || t('pluginHub.noSummary', null, '暂无简介')}
                                            </Paragraph>
                                            <div className="cs-ph-card-meta">
                                                <a href={entry.repository} target="_blank" rel="noreferrer" style={{ fontSize: 12 }}>
                                                    {entry.repository.replace('https://github.com/', 'github.com/')}
                                                </a>
                                            </div>
                                        </div>
                                        <div className="cs-ph-card-footer">
                                            {existing ? (
                                                <Tag color={stateColor(existing.observed_state)}>
                                                    {t('pluginHub.installedBadge', null, '已安装')} · {stateLabel(t, existing.observed_state)}
                                                </Tag>
                                            ) : (
                                                <Text type="tertiary" size="small">{t('pluginHub.externalRepoHint', null, '代码保留在原作者仓库')}</Text>
                                            )}
                                            <Space spacing={6}>
                                                {canManage && (
                                                    <Tooltip content={entry.builtin ? t('pluginHub.hideBuiltinTooltip', null, '从市场隐藏') : t('common.delete', null, '删除')}>
                                                        <Button
                                                            size="small"
                                                            theme="borderless"
                                                            type="danger"
                                                            icon={<IconDelete />}
                                                            onClick={() => confirmDeleteCatalog(entry)}
                                                        />
                                                    </Tooltip>
                                                )}
                                                {existing && canUse && existing.observed_state === 'healthy' && (
                                                    <Button size="small" theme="solid" type="primary" onClick={() => openApp(existing)}>
                                                        {t('pluginHub.openWorkspace', null, '打开应用')}
                                                    </Button>
                                                )}
                                                {!existing && canManage && (
                                                    <Button
                                                        size="small"
                                                        theme={oneClick ? 'solid' : 'light'}
                                                        type="primary"
                                                        icon={<IconDownload />}
                                                        loading={planningCatalogId === entry.id}
                                                        onClick={() => handleCatalogInstall(entry)}
                                                    >
                                                        {oneClick
                                                            ? t('pluginHub.oneClickInstall', null, '一键下载安装')
                                                            : t('pluginHub.inspectAndImport', null, '探测并导入')}
                                                    </Button>
                                                )}
                                            </Space>
                                        </div>
                                    </div>
                                );
                            })}
                        </div>
                    </TabPane>
                </Tabs>
            </SectionCard>

            <AddCatalogDrawer
                visible={addCatalogOpen}
                onCancel={() => setAddCatalogOpen(false)}
                onSaved={() => {
                    setAddCatalogOpen(false);
                    catalogRes.reload({ silent: true });
                }}
            />

            <ImportGitDrawer
                visible={importOpen}
                initialEntry={importInitialEntry}
                onCancel={() => {
                    setImportOpen(false);
                    setImportInitialEntry(null);
                }}
                onCatalogSaved={() => catalogRes.reload({ silent: true })}
                onPlanReady={res => {
                    setImportOpen(false);
                    setImportInitialEntry(null);
                    setPlanState(res);
                }}
            />

            <PlanApprovalDrawer
                planResponse={planState}
                channels={channels}
                visible={Boolean(planState)}
                onCancel={() => setPlanState(null)}
                onConfirmed={op => {
                    setPlanState(null);
                    setActiveOp(op);
                    setTab('installed');
                    installsRes.reload({ silent: true });
                }}
            />

            <PluginDetailDrawer
                installId={detailTarget?.id}
                initialTab={detailTarget?.tab}
                visible={Boolean(detailTarget)}
                canManage={canManage}
                channels={channels}
                onCancel={() => setDetailTarget(null)}
                onRestartRequested={item => runOperation(item, 'restart')}
                onChanged={reloadAll}
            />

            <RuntimeProbesDrawer
                visible={probesOpen}
                onCancel={() => setProbesOpen(false)}
            />
        </div>
    );
}
