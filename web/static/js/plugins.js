// 能力包控制台：装 / 摘 / 启停都走 /api/plugins，读的是服务端那张能力表。
// 页面上每个单元都显式标出"运行路径是否真的在读它"（served）与没在读的原因，
// 避免"已安装"被读成"已生效"。
function pluginsT(key, opts) {
    const k = 'plugins.' + key;
    return typeof window.t === 'function' ? window.t(k, opts) : k;
}

let pluginConsoleState = null;
let pluginConsoleCatalog = null;
let pluginConsoleBusy = false;

function isPluginConsoleActive() {
    const page = document.getElementById('page-plugins-management');
    return !!(page && page.classList.contains('active'));
}

async function loadPluginConsole() {
    if (pluginConsoleBusy) return;
    pluginConsoleBusy = true;
    try {
        await fetchPluginConsole();
    } finally {
        pluginConsoleBusy = false;
    }
}

// fetchPluginConsole is split from the entry point because a mutation reloads the table while it
// still holds the busy flag: calling the guarded version there would return without refreshing, and
// the console would keep showing the pre-mutation state.
async function fetchPluginConsole() {
    const listEl = document.getElementById('plugin-console');
    if (!listEl) return;
    listEl.innerHTML = '<div class="empty-state">' + escapeHtml(pluginsT('loading')) + '</div>';
    try {
        const [stateResp, catalogResp] = await Promise.all([
            apiFetch('/api/plugins'),
            apiFetch('/api/plugins/available'),
        ]);
        if (!stateResp.ok || !catalogResp.ok) {
            throw new Error('HTTP ' + stateResp.status + ' / ' + catalogResp.status);
        }
        pluginConsoleState = await stateResp.json();
        pluginConsoleCatalog = await catalogResp.json();
        renderPluginConsole();
    } catch (err) {
        listEl.innerHTML = '<div class="empty-state">' +
            escapeHtml(pluginsT('loadFailed')) + ': ' + escapeHtml(err.message) + '</div>';
    }
}

function renderPluginConsole() {
    const listEl = document.getElementById('plugin-console');
    if (!listEl || !pluginConsoleState) return;
    const state = pluginConsoleState;
    const catalog = (pluginConsoleCatalog && pluginConsoleCatalog.bundles) || [];
    const installedIds = new Set((state.bundles || []).map(b => b.id));

    const parts = [];
    parts.push(renderPluginSummary(state));

    const installable = catalog.filter(b => !installedIds.has(b.id));
    parts.push(renderPluginSection(pluginsT('availableTitle'), installable.length
        ? installable.map(renderPluginAvailableCard).join('')
        : '<div class="empty-state">' + escapeHtml(pluginsT('nothingAvailable')) + '</div>'));

    parts.push(renderPluginSection(pluginsT('installedTitle'), (state.bundles || []).length
        ? (state.bundles || []).map(renderPluginInstalledCard).join('')
        : '<div class="empty-state">' + escapeHtml(pluginsT('nothingInstalled')) + '</div>'));

    parts.push(renderPluginSection(pluginsT('standaloneTitle'), (state.standalone || []).length
        ? renderPluginUnitTable(state.standalone)
        : '<div class="empty-state">' + escapeHtml(pluginsT('nothingStandalone')) + '</div>'));

    const drift = state.drift || [];
    parts.push(renderPluginSection(pluginsT('driftTitle'), drift.length
        ? '<ul class="plugin-drift-list">' + drift.map(d =>
            '<li><code>' + escapeHtml(d) + '</code></li>').join('') + '</ul>'
        : '<div class="empty-state">' + escapeHtml(pluginsT('noDrift')) + '</div>'));

    listEl.innerHTML = parts.join('');
    if (typeof window.applyRBACToUI === 'function') {
        window.applyRBACToUI(listEl);
    }
}

function renderPluginSummary(state) {
    const served = (state.servedKinds || []).slice().sort();
    return '<div class="plugin-summary">' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('bundlesRoot')) + '</span>' +
        '<code>' + escapeHtml(state.bundlesRoot || '-') + '</code></div>' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('generation')) + '</span>' +
        '<strong>' + escapeHtml(String(state.generation == null ? '-' : state.generation)) + '</strong></div>' +
        '<div class="plugin-summary-item"><span>' + escapeHtml(pluginsT('servedKinds')) + '</span>' +
        (served.length
            ? served.map(k => '<span class="plugin-chip plugin-chip-served">' + escapeHtml(k) + '</span>').join('')
            : '<span class="plugin-chip">-</span>') + '</div>' +
        '</div>';
}

function renderPluginSection(title, body) {
    return '<section class="plugin-section"><h3 class="plugin-section-title">' +
        escapeHtml(title) + '</h3>' + body + '</section>';
}

function unitKindLabel(kind) {
    return pluginsT('kind.' + kind);
}

function servedMark(unit) {
    if (unit.served) {
        return '<span class="plugin-chip plugin-chip-served">' + escapeHtml(pluginsT('served')) + '</span>';
    }
    const reason = unit.reason || pluginsT('notServedDefault');
    // reason goes into an attribute; the shared escapeHtml mirrors the browser's innerHTML rules,
    // which leave a double quote alone - that would break out of title="...".
    return '<span class="plugin-chip plugin-chip-unserved" title="' + escapeAttr(reason) + '">' +
        escapeHtml(pluginsT('notServed')) + '</span>';
}

function unitCells(unit) {
    return '<td><span class="plugin-kind">' + escapeHtml(unitKindLabel(unit.kind)) + '</span></td>' +
        '<td><code>' + escapeHtml(unit.name) + '</code></td>' +
        '<td>' + servedMark(unit) + '</td>';
}

function renderPluginUnitTable(units) {
    const rows = units.map(u => {
        const bundleCell = u.bundle ? '<code>' + escapeHtml(u.bundle) + '</code>' : '-';
        return '<tr>' + unitCells(u) +
            '<td>' + bundleCell + '</td>' +
            '<td>' + unitSwitch(u) + '</td></tr>';
    }).join('');
    return '<table class="plugin-table"><thead><tr>' +
        '<th>' + escapeHtml(pluginsT('colKind')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colName')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colServed')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colBundle')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colEnabled')) + '</th>' +
        '</tr></thead><tbody>' + rows + '</tbody></table>';
}

function unitSwitch(unit) {
    // Three positional arguments, each quoted separately: emitting one JSON array would hand the
    // whole array to the first parameter, and the browser would POST
    // /units/tool,semgrep,false/undefined/enabled while every unit test still looked green.
    const args = [unit.kind, unit.name, !unit.enabled]
        .map(v => typeof v === 'string' ? escapeAttr(JSON.stringify(v)) : String(v === true))
        .join(',');
    return '<button class="btn-secondary btn-sm" data-require-permission="plugins:write" ' +
        'onclick="setPluginUnitEnabled(' + args + ')">' +
        escapeHtml(unit.enabled ? pluginsT('disable') : pluginsT('enable')) + '</button>';
}

function renderPluginAvailableCard(pack) {
    const units = pack.units || [];
    const kinds = {};
    units.forEach(u => { kinds[u.kind] = (kinds[u.kind] || 0) + 1; });
    const kindText = Object.keys(kinds).sort().map(k => unitKindLabel(k) + ' × ' + kinds[k]).join('、');
    const broken = pack.error
        ? '<div class="plugin-error">' + escapeHtml(pluginsT('manifestBroken')) + ': ' +
          escapeHtml(pack.error) + '</div>'
        : '';
    const installArg = escapeAttr(JSON.stringify(pack.id));
    return '<article class="plugin-card' + (pack.error ? ' plugin-card-broken' : '') + '">' +
        '<header class="plugin-card-head">' +
        '<h4>' + escapeHtml(pack.name || pack.id) + '</h4>' +
        '<span class="plugin-card-meta">' + escapeHtml(pack.id) +
        (pack.version ? ' · v' + escapeHtml(pack.version) : '') + '</span>' +
        '</header>' +
        (pack.description ? '<p class="plugin-card-desc">' + escapeHtml(pack.description) + '</p>' : '') +
        (kindText ? '<p class="plugin-card-units">' + escapeHtml(kindText) + '</p>' : '') +
        broken +
        (pack.error ? '' :
            '<div class="plugin-card-actions">' +
            '<button class="btn-primary btn-sm" data-require-permission="plugins:install" ' +
            'onclick="installPluginBundle(' + installArg + ')">' +
            escapeHtml(pluginsT('install')) + '</button></div>') +
        '</article>';
}

function renderPluginInstalledCard(pack) {
    const units = pack.units || [];
    const idArg = escapeAttr(JSON.stringify(pack.id));
    return '<article class="plugin-card plugin-card-installed">' +
        '<header class="plugin-card-head">' +
        '<h4>' + escapeHtml(pack.name || pack.id) + '</h4>' +
        '<span class="plugin-card-meta">' + escapeHtml(pack.id) +
        (pack.version ? ' · v' + escapeHtml(pack.version) : '') + '</span>' +
        '</header>' +
        (pack.description ? '<p class="plugin-card-desc">' + escapeHtml(pack.description) + '</p>' : '') +
        '<table class="plugin-table"><thead><tr>' +
        '<th>' + escapeHtml(pluginsT('colKind')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colName')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colServed')) + '</th>' +
        '<th>' + escapeHtml(pluginsT('colEnabled')) + '</th>' +
        '</tr></thead><tbody>' +
        units.map(u => '<tr>' + unitCells(u) + '<td>' + unitSwitch(u) + '</td></tr>').join('') +
        '</tbody></table>' +
        '<div class="plugin-card-actions">' +
        '<button class="btn-secondary btn-sm" data-require-permission="plugins:install" ' +
        'onclick="unplugPluginBundle(' + idArg + ')">' +
        escapeHtml(pluginsT('unplug')) + '</button></div>' +
        '</article>';
}

// notify uses the platform's shared toast when it is loaded. It must never be the reason a
// mutation looks like it failed: the request already succeeded, and awaiting a toast helper that
// does not exist on this page threw and showed "install failed" over a successful install.
function notify(message, type) {
    if (typeof showNotification === 'function') {
        try {
            showNotification(message, type);
        } catch (e) {
            console.warn('notify', e);
        }
    }
}

async function runPluginRequest(method, url, body) {
    const resp = await apiFetch(url, {
        method: method,
        headers: { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    const data = await resp.json().catch(() => ({}));
    if (!resp.ok) {
        throw new Error(data.error || ('HTTP ' + resp.status));
    }
    return data;
}

async function installPluginBundle(bundleId) {
    await withPluginBusy(async () => {
        const data = await runPluginRequest('POST', '/api/plugins/install', { bundle: bundleId });
        const note = data.tools_rebuilt ? pluginsT('installWithTools') : pluginsT('installPlain');
        notify(note + ' ' + (data.bundle && data.bundle.id ? data.bundle.id : bundleId), 'success');
        await fetchPluginConsole();
    }, 'install');
}

async function unplugPluginBundle(bundleId) {
    if (!window.confirm(pluginsT('confirmUnplug', { name: bundleId }))) return;
    await withPluginBusy(async () => {
        await runPluginRequest('DELETE', '/api/plugins/bundles/' + encodeURIComponent(bundleId));
        notify(pluginsT('unplugDone'), 'success');
        await fetchPluginConsole();
    }, 'unplug');
}

async function setPluginUnitEnabled(kind, name, enabled) {
    await withPluginBusy(async () => {
        await runPluginRequest('POST',
            '/api/plugins/units/' + encodeURIComponent(kind) + '/' + encodeURIComponent(name) + '/enabled',
            { enabled: enabled });
        notify(pluginsT('switchDone'), 'success');
        await fetchPluginConsole();
    }, 'switch');
}

async function withPluginBusy(fn, label) {
    if (pluginConsoleBusy) return;
    pluginConsoleBusy = true;
    try {
        await fn();
    } catch (err) {
        if (!isPluginConsoleActive()) return;
        const listEl = document.getElementById('plugin-console');
        if (listEl) {
            listEl.insertAdjacentHTML('afterbegin',
                '<div class="plugin-error">' + escapeHtml(pluginsT(label + 'Failed')) + ': ' +
                escapeHtml(err.message) + '</div>');
        }
    } finally {
        pluginConsoleBusy = false;
    }
}

window.loadPluginConsole = loadPluginConsole;
window.renderPluginConsole = renderPluginConsole;
window.installPluginBundle = installPluginBundle;
window.unplugPluginBundle = unplugPluginBundle;
window.setPluginUnitEnabled = setPluginUnitEnabled;
