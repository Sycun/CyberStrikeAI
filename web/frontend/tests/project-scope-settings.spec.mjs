import { test, expect, ready, PROJECT } from './fixtures.mjs';

test('structured scope writes back to the existing JSON object', async ({ page, mockApi }) => {
    const path = `/api/projects/${PROJECT.id}`;
    const project = {
        ...PROJECT, scope_json: JSON.stringify({ targets: ['example.com'], custom: { keep: true } })
    };
    mockApi.respond('GET', path, { json: project });
    mockApi.respond('PUT', path, ({ body }) => {
        Object.assign(project, body);
        return { json: project };
    });

    await page.goto(`/static/app/#/projects?id=${PROJECT.id}&tab=settings`);
    const main = await ready(page);
    await main.getByRole('button', { name: '添加约束' }).click();
    const kind = main.getByRole('radiogroup', { name: '约束类型' });
    await kind.getByText('允许', { exact: true }).click();
    await expect(kind.getByRole('radio', { name: '允许' })).toBeChecked();
    await main.getByRole('textbox', { name: '约束内容' }).fill('只读维护窗口');
    const noise = main.getByRole('radiogroup', { name: '噪音档位' });
    await noise.getByText('低噪音', { exact: true }).click();
    await expect(noise.getByRole('radio', { name: '低噪音' })).toBeChecked();
    const audit = main.getByRole('switch', { name: '处置经验影子审计' });
    await audit.click();
    await expect(audit).toHaveAttribute('aria-checked', 'true');
    const readsBeforeSave = mockApi.calls('GET', path).length;
    await main.getByRole('button', { name: '保存更改' }).click();

    await expect.poll(() => mockApi.calls('PUT', path).length).toBe(1);
    const { scope_json, lessons_mode } = mockApi.calls('PUT', path)[0].body;
    expect(lessons_mode).toBe('shadow');
    expect(JSON.parse(scope_json)).toEqual({
        targets: ['example.com'],
        custom: { keep: true },
        constraints: [{ kind: 'allow', text: '只读维护窗口', origin: 'human' }],
        noise_profile: 'quiet'
    });
    await expect.poll(() => mockApi.calls('GET', path).length).toBeGreaterThan(readsBeforeSave);
    await expect(noise.getByRole('radio', { name: '低噪音' })).toBeChecked();
    await expect(audit).toHaveAttribute('aria-checked', 'true');
    await audit.scrollIntoViewIfNeeded();
});
