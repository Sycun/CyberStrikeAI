const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const web = path.resolve(__dirname, '../..');
const template = fs.readFileSync(path.join(web, 'templates/index.html'), 'utf8');

test('the production task page loads its goal-task controller and styles', () => {
    const scripts = [...template.matchAll(/<script\b[^>]*\bsrc=["']([^"']+)["'][^>]*>/g)]
        .map(match => new URL(match[1], 'http://fixture').pathname);
    for (const script of ['router', 'task-list-view', 'tasks', 'task-checklist-view', 'long-tasks']) {
        const url = `/static/js/${script}.js`;
        assert.equal(scripts.filter(src => src === url).length, 1, `${url} must be included once by the real entrypoint`);
        assert.ok(fs.existsSync(path.join(web, url)), `${url} must exist`);
    }
    const styles = [...template.matchAll(/<link\b[^>]*\bhref=["']([^"']+)["'][^>]*>/g)]
        .map(match => new URL(match[1], 'http://fixture').pathname);
    assert.ok(styles.includes('/static/css/long-tasks.css'));
});
