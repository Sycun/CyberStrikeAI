/** 单轮输出：执行过程与事件在前，助手最终正文在末尾独立展示。 */
import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Collapse, Spin, Tag, Typography } from '@douyinfe/semi-ui';
import { MarkdownView, JsonViewer } from '../../components/ui';
import { api, errorMessage } from '../../lib/http';
import { formatNumber } from '../../lib/format';
import { useI18n } from '../../lib/i18n';
import {
    extractProcessDetailArgs,
    extractProcessDetailResult,
    prettyJson,
    truncateText
} from './stream';

const { Text } = Typography;

const STATUS_TAG = {
    running: { color: 'blue', label: '执行中' },
    success: { color: 'green', label: '完成' },
    failed: { color: 'red', label: '失败' },
    cancelled: { color: 'grey', label: '已取消' },
    blocked: { color: 'orange', label: '已拦截' }
};

function isPanelActive(activeKey) {
    return Array.isArray(activeKey) ? activeKey.includes('1') : activeKey === '1';
}

function ReasoningBlock({ block, defaultOpen }) {
    const [open, setOpen] = useState(Boolean(defaultOpen));
    const [loadedText, setLoadedText] = useState('');
    const [loading, setLoading] = useState(false);
    const [loadError, setLoadError] = useState('');
    const abortRef = useRef(null);

    const effectiveText = block.text || loadedText;
    const textLength = effectiveText ? effectiveText.length : (Number(block.textLength) || 0);
    const needsFetch = Boolean(block.payloadDeferred && block.detailId && !effectiveText);

    const fetchDetail = useCallback(async () => {
        if (!block.detailId) return;
        abortRef.current?.abort();
        const controller = new AbortController();
        abortRef.current = controller;
        setLoading(true);
        setLoadError('');
        try {
            const payload = await api.get(`/process-details/${encodeURIComponent(block.detailId)}`, null, { signal: controller.signal });
            if (controller.signal.aborted) return;
            const detail = payload?.processDetail;
            setLoadedText(String(detail?.message || ''));
        } catch (error) {
            if (!controller.signal.aborted) setLoadError(errorMessage(error, '加载思考过程失败'));
        } finally {
            if (abortRef.current === controller) {
                abortRef.current = null;
                setLoading(false);
            }
        }
    }, [block.detailId]);

    useEffect(() => () => abortRef.current?.abort(), []);

    useEffect(() => {
        if (open && needsFetch && !loading && !loadError) {
            void fetchDetail();
        }
    }, [open, needsFetch, loading, loadError, fetchDetail]);

    const displayText = useMemo(() => (open && effectiveText ? truncateText(effectiveText) : ''), [open, effectiveText]);

    if (!effectiveText.trim() && !block.payloadDeferred) return null;
    const title = block.label === 'reasoning_chain' ? '🔗 推理过程' : '🤔 思考过程';
    return (
        <Collapse
            className="chat-block-collapse"
            activeKey={open ? ['1'] : []}
            onChange={activeKey => setOpen(isPanelActive(activeKey))}
        >
            <Collapse.Panel itemKey="1" header={<span className="chat-block-title">{title} · {textLength} 字符</span>}>
                {open && loading && (
                    <div className="chat-tool-loading"><Spin size="small" /><span>正在加载思考过程…</span></div>
                )}
                {open && loadError && (
                    <div className="chat-tool-error">
                        <span>{loadError}</span>
                        <button type="button" className="chat-link-btn" onClick={() => void fetchDetail()}>重试</button>
                    </div>
                )}
                {open && displayText && <pre className="chat-pre">{displayText}</pre>}
            </Collapse.Panel>
        </Collapse>
    );
}

function ToolBlock({ block, index, total }) {
    const { t } = useI18n();
    const [open, setOpen] = useState(false);
    const [loadedArgs, setLoadedArgs] = useState(undefined);
    const [loadedResult, setLoadedResult] = useState(undefined);
    const [loading, setLoading] = useState(false);
    const [loadError, setLoadError] = useState('');
    const abortRef = useRef(null);

    const meta = STATUS_TAG[block.status] || STATUS_TAG.running;
    const effectiveArgs = loadedArgs !== undefined ? loadedArgs : block.args;
    const effectiveResult = loadedResult !== undefined ? loadedResult : (block.result || '');
    const needArgs = Boolean(block.argsDeferred && block.callDetailId && loadedArgs === undefined);
    const needResult = Boolean(block.resultDeferred && block.resultDetailId && loadedResult === undefined);
    const hasInlineArgs = effectiveArgs !== null && effectiveArgs !== undefined && effectiveArgs !== '';
    const hasBody = Boolean(hasInlineArgs || effectiveResult || needArgs || needResult || block.hasDeferredPayload);

    const fetchDeferredDetails = useCallback(async () => {
        const idsToFetch = [];
        if (needArgs && block.callDetailId) idsToFetch.push({ kind: 'args', id: block.callDetailId });
        if (needResult && block.resultDetailId) idsToFetch.push({ kind: 'result', id: block.resultDetailId });
        if (!idsToFetch.length) return;

        abortRef.current?.abort();
        const controller = new AbortController();
        abortRef.current = controller;
        setLoading(true);
        setLoadError('');
        try {
            const results = await Promise.all(idsToFetch.map(async item => {
                const payload = await api.get(`/process-details/${encodeURIComponent(item.id)}`, null, { signal: controller.signal });
                return { kind: item.kind, detail: payload?.processDetail || null };
            }));
            if (controller.signal.aborted) return;
            for (const item of results) {
                if (item.kind === 'args') {
                    setLoadedArgs(extractProcessDetailArgs(item.detail));
                } else if (item.kind === 'result') {
                    setLoadedResult(extractProcessDetailResult(item.detail));
                }
            }
        } catch (error) {
            if (!controller.signal.aborted) setLoadError(errorMessage(error, '加载工具详情失败'));
        } finally {
            if (abortRef.current === controller) {
                abortRef.current = null;
                setLoading(false);
            }
        }
    }, [block.callDetailId, block.resultDetailId, needArgs, needResult]);

    useEffect(() => () => abortRef.current?.abort(), []);

    useEffect(() => {
        if (open && (needArgs || needResult) && !loading && !loadError) {
            void fetchDeferredDetails();
        }
    }, [open, needArgs, needResult, loading, loadError, fetchDeferredDetails]);

    const argsText = useMemo(() => (open ? prettyJson(effectiveArgs) : ''), [open, effectiveArgs]);
    const resultText = open ? effectiveResult : '';

    const name = block.toolName || t('chat.unknownTool', null, '未知工具');
    const header = (
        <span className="chat-tool-head">
            <span className="chat-tool-name">
                {block.agentName ? `${block.agentName} · ` : ''}
                {t('chat.callTool', { name, index, total }, `调用工具: ${name} (${index}/${total})`)}
            </span>
            <Tag size="small" color={meta.color} shape="circle">{meta.label}</Tag>
        </span>
    );
    if (!hasBody) {
        return <div className="chat-tool"><div className="chat-tool-head-row">{header}</div></div>;
    }
    return (
        <div className="chat-tool">
            <Collapse
                className="chat-block-collapse"
                activeKey={open ? ['1'] : []}
                onChange={activeKey => setOpen(isPanelActive(activeKey))}
            >
                <Collapse.Panel itemKey="1" header={header}>
                    {open && loading && (
                        <div className="chat-tool-loading"><Spin size="small" /><span>正在加载工具详情…</span></div>
                    )}
                    {open && loadError && (
                        <div className="chat-tool-error">
                            <span>{loadError}</span>
                            <button type="button" className="chat-link-btn" onClick={() => void fetchDeferredDetails()}>重试</button>
                        </div>
                    )}
                    {open && argsText && (
                        <>
                            <Text type="tertiary" size="small">参数</Text>
                            <pre className="chat-pre">{argsText}</pre>
                        </>
                    )}
                    {open && resultText && (
                        <>
                            <Text type="tertiary" size="small">结果</Text>
                            <pre className="chat-pre">{resultText}</pre>
                        </>
                    )}
                </Collapse.Panel>
            </Collapse>
        </div>
    );
}

function NoteBlock({ block }) {
    const [open, setOpen] = useState(false);
    const tone = block.tone || 'info';
    return (
        <div className={`chat-note chat-note-${tone}`}>
            <div className="chat-note-head">
                <span className="chat-note-title">{block.title}</span>
                {block.data && (
                    <button type="button" className="chat-link-btn" onClick={() => setOpen(!open)}>
                        {open ? '收起 JSON' : '查看 JSON'}
                    </button>
                )}
            </div>
            {block.message && <div className="chat-note-body">{block.message}</div>}
            {open && block.data && <JsonViewer value={block.data} collapsed={false} title="事件数据" />}
        </div>
    );
}

function UsageSummaryBlock({ data }) {
    const { t, lang } = useI18n();
    const number = value => formatNumber(Math.max(0, Math.trunc(Number(value) || 0)), lang);
    const rows = [
        ['chat.piUsageModelCalls', data.modelCalls],
        ['chat.piUsagePromptTokens', data.promptTokens],
        ['chat.piUsageCompletionTokens', data.completionTokens],
        ['chat.piUsageTotalTokens', data.totalTokens]
    ];
    const optionalRows = [
        ['chat.piUsageCachedTokens', data.cachedTokens],
        ['chat.piUsageCacheWriteTokens', data.cacheWriteTokens],
        ['chat.piUsageReasoningTokens', data.reasoningTokens]
    ];
    const message = [...rows, ...optionalRows.filter(([, count]) => Number(count) > 0)]
        .map(([label, count]) => `${t(label)}: ${number(count)}`)
        .join('\n');
    const total = Number(data.totalTokens);
    const title = [
        data.agentName,
        t('chat.piUsageSummaryTitle'),
        Number.isFinite(total) && total > 0 ? number(total) : ''
    ].filter(Boolean).join(' · ');
    return <NoteBlock block={{ title, message, data }} />;
}

function EventLog({ events }) {
    if (!events || !events.length) return null;
    return (
        <Collapse className="chat-block-collapse chat-event-log">
            <Collapse.Panel itemKey="1" header={<span className="chat-block-title">🧾 原始事件 · {events.length} 条</span>}>
                <pre className="chat-pre">
                    {events.map((event, index) => `${index + 1}. ${JSON.stringify(event)}`).join('\n')}
                </pre>
            </Collapse.Panel>
        </Collapse>
    );
}

const TurnBody = memo(function TurnBody({
    text,
    blocks,
    events,
    streaming = false,
    openReasoning = false,
    isAssistant = false
}) {
    const { t } = useI18n();
    const list = Array.isArray(blocks) ? blocks : [];
    const body = text || '';
    const toolCount = list.filter(block => block.kind === 'tool').length;
    let toolIndex = 0;
    return (
        <div className="chat-turn-body">
            {list.map(block => {
                if (block.kind === 'reasoning') {
                    return <ReasoningBlock key={block.id} block={block} defaultOpen={openReasoning} />;
                }
                if (block.kind === 'tool') {
                    toolIndex += 1;
                    return <ToolBlock key={block.id} block={block} index={toolIndex} total={toolCount} />;
                }
                if (block.kind === 'iteration') {
                    return (
                        <div className="chat-iteration" key={block.id}>
                            <span className="chat-iteration-label">{block.title}</span>
                        </div>
                    );
                }
                if (block.kind === 'note') {
                    return <NoteBlock key={block.id} block={block} />;
                }
                if (block.kind === 'usage') {
                    return <UsageSummaryBlock key={block.id} data={block.data} />;
                }
                return null;
            })}
            <EventLog events={events} />
            {body.trim() && (isAssistant && !streaming ? (
                <section className="chat-task-summary" aria-label={t('chat.taskSummary')} data-chat-summary>
                    <details key={body.length} open>
                        <summary className="chat-task-summary-title">{t('chat.taskSummary')}</summary>
                        <div className="chat-task-summary-content"><MarkdownView content={body} /></div>
                    </details>
                </section>
            ) : streaming ? (
                <pre className="chat-pre chat-stream-text">{body}</pre>
            ) : <MarkdownView content={body} />)}
            {!body.trim() && streaming && <div className="chat-stream-placeholder">正在生成…</div>}
        </div>
    );
});

export default TurnBody;
