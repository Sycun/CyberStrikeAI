import { test, expect, visit, label, UI_PERMISSIONS } from './fixtures.mjs';

test.use({ appPermissions: [...UI_PERMISSIONS, 'config:write'] });

test('model channel configures image input mode without redundant test-image button while audit settings start collapsed', async ({ page, mockApi }) => {
    mockApi.respond('GET', '/api/config', { json: {
        ai: { default_channel: 'deepseek', channels: {
            deepseek: { name: 'DeepSeek', model: 'deepseek-flash', base_url: 'https://api.deepseek.com', api_key: '********' }
        } },
        image_input_defaults: { deepseek: true },
        hitl: { audit_model: {} },
        tools: []
    } });
    const main = await visit(page, 'settings?section=models');
    const panel = main.locator('.settings-channel-panel');
    const testAudit = main.getByRole('button', { name: label('settingsProbe.testAuditModel'), exact: true });
    await expect(panel.getByRole('button', { name: label('settingsProbe.testImage'), exact: true })).toHaveCount(0);
    await expect(panel.getByText(label('settingsProbe.imageInputSupported'), { exact: true })).toBeVisible();
    const imageSelect = panel.getByRole('combobox', { name: '图片输入能力', exact: true });
    await expect(imageSelect).toContainText('自动识别（支持图片）');
    await expect(testAudit).toBeHidden();

    await imageSelect.click();
    await page.getByRole('option', { name: /禁用/ }).click();
    await expect(panel.getByText(label('settingsProbe.imageInputSupported'), { exact: true })).toHaveCount(0);

    await main.getByText(label('settingsProbe.auditModelTitle'), { exact: true }).click();
    await expect(testAudit).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(testAudit).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(391);
});
