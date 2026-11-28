import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { Button, Space, Spin, Tag, Tooltip, Typography } from '@douyinfe/semi-ui';
import {
    IconChevronLeft, IconExternalOpen, IconMaximize, IconMinimize, IconRefresh, IconSetting, IconTerminal
} from '@douyinfe/semi-icons';
import { useT } from '../../lib/i18n';
import { toastError } from '../../components/ui';
import { pluginApi, pluginErrorText, stateColor, stateLabel } from './api';

const { Text } = Typography;

function submitTicketForm(action, ticket, target) {
    const form = document.createElement('form');
    form.method = 'POST';
    form.action = action;
    form.target = target;
    form.style.display = 'none';
    const input = document.createElement('input');
    input.type = 'hidden';
    input.name = 'ticket';
    input.value = ticket;
    form.appendChild(input);
    document.body.appendChild(form);
    form.submit();
    form.remove();
}

export default function WorkspaceView({ install, canManage, onBack, onOpenDetail, onOpenLogs }) {
    const t = useT();
    const rawId = useId();
    const frameName = `csai-plugin-frame-${rawId.replace(/[^a-zA-Z0-9_-]/g, '')}`;
    const containerRef = useRef(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [sessionInfo, setSessionInfo] = useState(null);
    const [expanded, setExpanded] = useState(false);

    const connect = useCallback(async () => {
        if (!install?.id) return;
        setLoading(true);
        setError('');
        try {
            const embed = await pluginApi.issueEmbedSession(install.id);
            setSessionInfo(embed);
            requestAnimationFrame(() => {
                submitTicketForm(embed.action, embed.ticket, frameName);
            });
        } catch (err) {
            setError(pluginErrorText(t, err, t('pluginHub.embedInitFailed', null, '无法建立插件工作区会话')));
            setLoading(false);
        }
    }, [install?.id, frameName, t]);

    useEffect(() => {
        connect();
    }, [connect]);

    const openStandaloneWindow = async () => {
        try {
            const embed = await pluginApi.issueEmbedSession(install.id);
            const winName = `csai-plugin-standalone-${Date.now()}`;
            window.open('about:blank', winName, 'noopener');
            submitTicketForm(embed.action, embed.ticket, winName);
        } catch (err) {
            toastError(pluginErrorText(t, err, t('pluginHub.embedInitFailed', null, '无法打开独立窗口')));
        }
    };

    const toggleFullscreen = async () => {
        const el = containerRef.current;
        if (!el) return;
        if (document.fullscreenElement) {
            try { await document.exitFullscreen(); } catch { /* ignore */ }
            setExpanded(false);
            return;
        }
        if (el.requestFullscreen) {
            try {
                await el.requestFullscreen();
                setExpanded(true);
                return;
            } catch {
                // fallback to CSS overlay expand
            }
        }
        setExpanded(current => !current);
    };

    useEffect(() => {
        const onFsChange = () => {
            if (!document.fullscreenElement) setExpanded(false);
        };
        document.addEventListener('fullscreenchange', onFsChange);
        return () => document.removeEventListener('fullscreenchange', onFsChange);
    }, []);

    return (
        <div ref={containerRef} className={`cs-ph-workspace ${expanded ? 'is-expanded' : ''}`}>
            <div className="cs-ph-workspace-bar">
                <div className="cs-ph-workspace-title">
                    <Button size="small" theme="borderless" icon={<IconChevronLeft />} onClick={onBack}>
                        {t('pluginHub.backToHub', null, '返回能力平台')}
                    </Button>
                    <Text strong ellipsis style={{ maxWidth: 260 }}>{install.title || install.plugin_id}</Text>
                    <Tag size="small" color={stateColor(install.observed_state)} shape="square">
                        {stateLabel(t, install.observed_state)}
                    </Tag>
                    {sessionInfo?.origin && (
                        <Text type="tertiary" size="small" className="cs-ph-mono" ellipsis style={{ maxWidth: 260 }}>
                            {sessionInfo.origin}
                        </Text>
                    )}
                </div>
                <Space spacing={6} wrap>
                    <Tooltip content={t('pluginHub.reconnectSession', null, '刷新 / 重新建立隔离会话')}>
                        <Button size="small" icon={<IconRefresh />} onClick={connect} loading={loading}>
                            {t('common.refresh', null, '刷新')}
                        </Button>
                    </Tooltip>
                    <Tooltip content={t('pluginHub.openFirstPartyTip', null, '若浏览器严格拦截跨站 Cookie，可在独立首方窗口中打开')}>
                        <Button size="small" icon={<IconExternalOpen />} onClick={openStandaloneWindow}>
                            {t('pluginHub.openStandalone', null, '独立窗口')}
                        </Button>
                    </Tooltip>
                    {canManage && (
                        <Button size="small" icon={<IconTerminal />} onClick={onOpenLogs}>
                            {t('pluginHub.logs', null, '日志')}
                        </Button>
                    )}
                    <Button size="small" icon={<IconSetting />} onClick={onOpenDetail}>
                        {t('pluginHub.settingsAndDetail', null, '详情与模型')}
                    </Button>
                    <Button
                        size="small"
                        icon={expanded ? <IconMinimize /> : <IconMaximize />}
                        onClick={toggleFullscreen}
                    >
                        {expanded ? t('pluginHub.exitFullscreen', null, '退出全屏') : t('mobile.fullscreen', null, '全屏')}
                    </Button>
                </Space>
            </div>

            <div className="cs-ph-workspace-frame-wrap">
                <iframe
                    name={frameName}
                    title={install.title || install.plugin_id}
                    className="cs-ph-workspace-iframe"
                    sandbox="allow-scripts allow-same-origin allow-forms allow-downloads"
                    allow="clipboard-read; clipboard-write"
                    onLoad={() => setLoading(false)}
                />
                {loading && !error && (
                    <div className="cs-ph-workspace-overlay">
                        <Spin size="large" />
                        <Text type="tertiary">{t('pluginHub.connectingWorkspace', null, '正在建立隔离代理会话并加载插件页面…')}</Text>
                    </div>
                )}
                {error && (
                    <div className="cs-ph-workspace-overlay">
                        <Text type="danger" strong>{error}</Text>
                        <Text type="tertiary" size="small">
                            {t('pluginHub.embedTroubleshooting', null, '请确认插件处于运行中状态；若浏览器限制跨站 Cookie，可点击上方「独立窗口」以首方会话打开。')}
                        </Text>
                        <Space>
                            <Button theme="solid" type="primary" onClick={connect}>{t('pluginHub.retryConnect', null, '重试连接')}</Button>
                            <Button icon={<IconExternalOpen />} onClick={openStandaloneWindow}>{t('pluginHub.openStandalone', null, '独立窗口')}</Button>
                        </Space>
                    </div>
                )}
            </div>
        </div>
    );
}
