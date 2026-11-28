import { test, expect, ready, visit, UI_PERMISSIONS } from './fixtures.mjs';

test.describe('vision and knowledge frontend configuration', () => {
    test.use({ appPermissions: [...UI_PERMISSIONS, 'config:write', 'knowledge:write'] });

    test('enables vision and knowledge from GUI without editing YAML and updates overview', async ({ page, mockApi }) => {
        const state = {
            ai: {
                default_channel: 'ds',
                channels: {
                    ds: {
                        name: 'ds',
                        provider: 'openai_compatible',
                        base_url: 'https://api.deepseek.com/v1',
                        model: 'deepseek-chat',
                        api_key: '********'
                    }
                }
            },
            vision: { enabled: false, model: '', base_url: '', api_key: '' },
            knowledge: {
                enabled: false,
                base_path: 'knowledge_base',
                embedding: { provider: 'openai', model: 'text-embedding-v4', base_url: '', api_key: '' },
                retrieval: { top_k: 5, similarity_threshold: 0.4, multi_query: { max_queries: 4 }, rerank: {}, post_retrieve: {} },
                indexing: { chunk_size: 512, chunk_overlap: 50 }
            },
            tools: []
        };

        mockApi.respond('GET', '/api/config', () => ({ json: state }));
        mockApi.respond('PUT', '/api/config', ({ body }) => {
            if (body.vision) {
                state.vision = {
                    ...state.vision,
                    ...body.vision,
                    api_key: body.vision.api_key ? '********' : ''
                };
            }
            if (body.knowledge) {
                state.knowledge = {
                    ...state.knowledge,
                    ...body.knowledge,
                    embedding: {
                        ...state.knowledge.embedding,
                        ...body.knowledge.embedding,
                        api_key: body.knowledge.embedding?.api_key ? '********' : ''
                    }
                };
            }
            return { json: {} };
        });
        mockApi.respond('POST', '/api/config/apply', { json: {} });
        mockApi.respond('POST', '/api/config/test-vision', { json: {
            success: true,
            model: 'qwen-vl-max',
            latency_ms: 185
        } });
        mockApi.respond('POST', '/api/config/test-embedding', { json: {
            success: true,
            model: 'text-embedding-v4',
            dimensions: 1024,
            latency_ms: 92
        } });

        // 1. Start on Basic Settings overview and jump to Vision config
        const main = await visit(page, 'settings?section=basic');
        const overview = main.locator('.cs-section-card').first();
        await expect(overview.getByRole('cell', { name: /视觉模型.*未启用/ })).toBeVisible();
        await expect(overview.getByRole('cell', { name: /知识库设置.*未启用/ })).toBeVisible();

        await overview.getByRole('cell', { name: /视觉模型/ }).getByRole('button', { name: '去启用' }).click();
        await expect(page).toHaveURL(/#\/settings\?section=models&focus=vision$/);

        const vision = main.locator('.settings-vision-panel');
        await vision.getByRole('switch', { name: '启用视觉分析工具 analyze_image' }).click();
        await vision.getByLabel('视觉模型', { exact: true }).fill('qwen-vl-max');
        await vision.getByLabel('API 地址', { exact: true }).fill('https://dashscope.aliyuncs.com/compatible-mode/v1');
        await vision.getByLabel('API 密钥', { exact: true }).fill('sk-vision-demo');

        await vision.getByRole('button', { name: '测试视觉模型', exact: true }).click();
        await expect(vision.getByText('185 毫秒', { exact: true })).toBeVisible();

        await vision.getByRole('button', { name: '保存视觉模型', exact: true }).click();
        await expect(vision.getByRole('button', { name: '保存视觉模型', exact: true })).toBeDisabled();
        expect(mockApi.calls('PUT', '/api/config').at(-1).body.vision).toMatchObject({
            enabled: true,
            model: 'qwen-vl-max',
            base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
            api_key: 'sk-vision-demo'
        });

        // 2. Switch to Knowledge tab, verify DeepSeek notice, probe embedding, and save
        await page.getByRole('tab', { name: '知识库' }).click();
        await expect(page).toHaveURL(/#\/settings\?section=knowledge$/);

        const knowledge = main.locator('.settings-knowledge-panel');
        await expect(knowledge.getByText(/DeepSeek 官方或 Claude 接口/)).toBeVisible();
        await knowledge.getByRole('switch', { name: '启用知识检索功能' }).click();
        await knowledge.getByLabel('API 地址', { exact: true }).fill('https://dashscope.aliyuncs.com/compatible-mode/v1');
        await knowledge.getByLabel('API 密钥', { exact: true }).fill('sk-embed-demo');

        await knowledge.getByRole('button', { name: '测试嵌入模型', exact: true }).click();
        await expect(knowledge.getByText('1024 维', { exact: true })).toBeVisible();
        await expect(knowledge.getByText('92 毫秒', { exact: true })).toBeVisible();

        await knowledge.getByRole('button', { name: '保存并应用', exact: true }).click();
        await expect(knowledge.getByRole('button', { name: '保存并应用', exact: true })).toBeDisabled();
        expect(mockApi.calls('PUT', '/api/config').at(-1).body.knowledge).toMatchObject({
            enabled: true,
            base_path: 'knowledge_base',
            embedding: {
                provider: 'openai',
                model: 'text-embedding-v4',
                base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
                api_key: 'sk-embed-demo'
            }
        });

        // 3. Return to Basic Settings and verify overview reflects both enabled states
        await page.getByRole('tab', { name: '基本设置' }).click();
        await expect(overview.getByRole('cell', { name: /视觉模型.*qwen-vl-max/ })).toBeVisible();
        await expect(overview.getByRole('cell', { name: /知识库设置.*已启用/ })).toBeVisible();

        // 4. Verify mobile layout has no horizontal overflow
        await page.setViewportSize({ width: 390, height: 844 });
        await page.goto('/static/app/#/settings?section=knowledge');
        await ready(page);
        expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(391);
    });

    test('knowledge management banner guides users to settings when knowledge is disabled', async ({ page, mockApi }) => {
        mockApi.respond('GET', '/api/knowledge/items', { json: {
            items: [],
            enabled: false,
            message: '知识库功能未启用，请前往系统设置启用知识检索功能'
        } });
        mockApi.respond('GET', '/api/knowledge/index-status', { json: {
            enabled: false,
            total_items: 0,
            indexed_items: 0
        } });
        const main = await visit(page, 'knowledge-management');
        await expect(main.getByText('知识库功能未启用，请前往系统设置启用知识检索功能')).toBeVisible();
        await expect(main.getByRole('button', { name: '构建索引', exact: true })).toBeDisabled();
        await main.getByRole('button', { name: '前往启用知识库', exact: true }).click();
        await expect(page).toHaveURL(/#\/settings\?section=knowledge$/);
    });

    test('chat composer offers vision model entry when current channel lacks native image support', async ({ page, mockApi }) => {
        mockApi.respond('GET', '/api/config', { json: {
            ai: {
                default_channel: 'opus-relay',
                channels: {
                    'opus-relay': {
                        name: 'opus4.6',
                        provider: 'claude',
                        base_url: 'https://relay.example.test',
                        model: 'claude-opus-4-6',
                        api_key: '********'
                    }
                }
            },
            vision: { enabled: false, model: '' },
            tools: []
        } });
        mockApi.respond('POST', '/api/chat-uploads', { json: { absolutePath: '/tmp/captcha.png' } });

        const main = await visit(page, 'chat');
        const composer = main.locator('.chat-composer');

        // Open model popover and check vision shortcut
        await composer.locator('.chat-composer-model').click();
        const popover = page.locator('.chat-reasoning-picker');
        await expect(popover.getByText('当前模型不支持识图？', { exact: true })).toBeVisible();
        await expect(popover.getByRole('button', { name: '配置视觉模型', exact: true })).toBeVisible();

        // Upload an image attachment and verify inline vision guidance appears
        const chooserPending = page.waitForEvent('filechooser');
        await composer.getByRole('button', { name: '附件', exact: true }).click();
        const chooser = await chooserPending;
        await chooser.setFiles({
            name: 'captcha.png',
            mimeType: 'image/png',
            buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64')
        });

        const tip = composer.locator('.chat-image-vision-tip');
        await expect(tip).toBeVisible();
        await tip.getByRole('button', { name: '去开启视觉模型', exact: true }).click();
        await expect(page).toHaveURL(/#\/settings\?section=models&focus=vision$/);
    });
});
