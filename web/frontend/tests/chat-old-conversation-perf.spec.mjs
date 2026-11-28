import { test, expect, ready, CONVERSATION } from './fixtures.mjs';

test('loads conversation history in summary mode and fetches deferred tool/thinking details on demand', async ({ page, mockApi }) => {
    const requestedQueries = [];
    const detailFetches = [];

    const summaryConversation = {
        ...CONVERSATION,
        messages: [
            { id: 'u1', role: 'user', content: '扫描目标端口' },
            {
                id: 'a1',
                role: 'assistant',
                content: '扫描完成，发现 80 端口开放。',
                processDetails: [
                    {
                        id: 'pd-think-1',
                        eventType: 'thinking',
                        message: '',
                        data: { streamId: 's1', _payloadDeferred: true, _textLength: 1024 }
                    },
                    {
                        id: 'pd-call-1',
                        eventType: 'tool_call',
                        message: '',
                        data: {
                            toolName: 'nmap_scan',
                            toolCallId: 'tc-1',
                            agentName: 'main',
                            _payloadDeferred: true,
                            _argsDeferred: true,
                            _hasPayload: true
                        }
                    },
                    {
                        id: 'pd-res-1',
                        eventType: 'tool_result',
                        message: '',
                        data: {
                            toolName: 'nmap_scan',
                            toolCallId: 'tc-1',
                            agentName: 'main',
                            success: true,
                            _payloadDeferred: true,
                            _hasPayload: true
                        }
                    }
                ]
            }
        ]
    };

    mockApi.respond('GET', `/api/conversations/${CONVERSATION.id}`, entry => {
        requestedQueries.push(entry.query);
        return { json: summaryConversation };
    });
    mockApi.respond('GET', '/api/process-details/pd-think-1', () => {
        detailFetches.push('pd-think-1');
        return {
            json: {
                processDetail: {
                    id: 'pd-think-1',
                    eventType: 'thinking',
                    message: '这是按需拉取的完整思考内容。',
                    data: { streamId: 's1' }
                }
            }
        };
    });
    mockApi.respond('GET', '/api/process-details/pd-call-1', () => {
        detailFetches.push('pd-call-1');
        return {
            json: {
                processDetail: {
                    id: 'pd-call-1',
                    eventType: 'tool_call',
                    message: '',
                    data: { toolName: 'nmap_scan', toolCallId: 'tc-1', argumentsObj: { target: '10.0.0.1', ports: '1-65535' } }
                }
            }
        };
    });
    mockApi.respond('GET', '/api/process-details/pd-res-1', () => {
        detailFetches.push('pd-res-1');
        return {
            json: {
                processDetail: {
                    id: 'pd-res-1',
                    eventType: 'tool_result',
                    message: '80/tcp open http',
                    data: { toolName: 'nmap_scan', toolCallId: 'tc-1', result: '80/tcp open http', success: true }
                }
            }
        };
    });

    await page.goto(`/static/app/#/chat?conversation=${CONVERSATION.id}`);
    const main = await ready(page);

    expect(requestedQueries.some(query => query.includes('include_process_details=summary'))).toBe(true);
    // Nothing deferred should be fetched before user expands the panel.
    expect(detailFetches).toEqual([]);

    await expect(main.getByText('扫描完成，发现 80 端口开放。')).toBeVisible();
    await expect(main.locator('.chat-tool')).toContainText('nmap_scan (1/1)');
    await expect(main.locator('.chat-tool')).toContainText('完成');

    // Expand tool card -> fetches both deferred call args and tool result on demand.
    await main.locator('.chat-tool .semi-collapse-header').click();
    await expect(main.locator('.chat-tool pre').nth(0)).toContainText('"target": "10.0.0.1"');
    await expect(main.locator('.chat-tool pre').nth(1)).toContainText('80/tcp open http');
    expect(detailFetches).toContain('pd-call-1');
    expect(detailFetches).toContain('pd-res-1');

    // Expand deferred thinking block -> fetches full thought text on demand.
    await main.locator('.chat-block-title', { hasText: '1024 字符' }).click();
    await expect(main.getByText('这是按需拉取的完整思考内容。')).toBeVisible();
    expect(detailFetches).toContain('pd-think-1');
});

test('background polling does not livelock slow conversation detail loading', async ({ page, mockApi }) => {
    let detailRequestCount = 0;
    let releaseDetail;
    const detailGate = new Promise(resolve => { releaseDetail = resolve; });

    mockApi.respond('GET', '/api/agent-loop/tasks', {
        json: { tasks: [{ conversationId: CONVERSATION.id, status: 'running' }] }
    });
    mockApi.respond('GET', `/api/conversations/${CONVERSATION.id}/plan-tasks`, {
        json: {
            running: true,
            total: 5,
            completed: 0,
            activeStep: 1,
            tasks: [{ id: '1', subject: '步骤一', status: 'in_progress' }]
        }
    });
    mockApi.respond('GET', `/api/conversations/${CONVERSATION.id}`, async () => {
        detailRequestCount += 1;
        await detailGate;
        return { json: CONVERSATION };
    });

    await page.goto(`/static/app/#/chat?conversation=${CONVERSATION.id}`);
    const main = page.locator('#main-content');
    await expect(main.getByText('0/5 · 第 1 步')).toBeVisible();
    const countAfterMount = detailRequestCount;
    expect(countAfterMount).toBeGreaterThanOrEqual(1);

    // Wait past the 4s polling interval while the initial detail request is still in flight.
    // Silent background polls must NOT fire competing requests that invalidate detailSeq.
    await page.waitForTimeout(4500);
    expect(detailRequestCount).toBe(countAfterMount);

    releaseDetail();
    await ready(page);
    await expect(main.getByText(CONVERSATION.title).first()).toBeVisible();
    await expect(main.getByText(CONVERSATION.messages[1].content)).toBeVisible();
});
