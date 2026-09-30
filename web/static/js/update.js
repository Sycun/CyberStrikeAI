// 控制台「一键更新」：这一页动的就是当前正在应答请求的那套安装树本身。
// 三条硬规矩，每一条都是这个仓库已经踩过一次的坑：
//   1) 查不了必须说查不了。checkError 非空时页面写出原因，绝不渲染成「已是最新」——
//      把"没查成"画成"没更新"，操作员要等到下一次故障才发现树早就没动了。
//   2) 提示只念真实 HTTP 结果，而且只用平台确实存在的 showNotification；没有就什么都不做。
//   3) 服务端来的字符串进 HTML 一律 escapeHtml，进属性一律 escapeAttr。
const UPDATE_POLL_INTERVAL_MS = 1500;

// withUpdateBusy 按动作挑错误文案的前缀，四个 key 都必须在两份语言里存在。
const UPDATE_FAILURE_KEYS = {
    check: 'checkFailed',
    apply: 'applyFailed',
    rollback: 'rollbackFailed',
    load: 'loadFailed',
};

let updateConsoleState = null;   // GET /api/system/update 的整包：{status, job, canRestart}
let updateCheck = { done: false, error: '' };
let updateBusy = false;          // 页面自己发着的动作请求（检查 / 启动更新 / 回滚）
let updateChecking = false;      // 只影响「检查更新」按钮的措辞
let updatePollTimer = null;
let updateRestartChoice = false; // 勾选框要在轮询重绘之后仍然是勾着的

function updateT(key, opts) {
    const k = 'update.' + key;
    return typeof window.t === 'function' ? window.t(k, opts) : k;
}

function isUpdateConsoleActive() {
    const page = document.getElementById('page-system-update');
    return !!(page && page.classList.contains('active'));
}

function updateStatusOf() {
    return (updateConsoleState && updateConsoleState.status) || {};
}

function updateJobOf() {
    const job = updateConsoleState && updateConsoleState.job;
    return job && typeof job === 'object' ? job : null;
}

function updateCanRestart() {
    return !!(updateConsoleState && updateConsoleState.canRestart);
}

function isUpdateJobRunning() {
    const job = updateJobOf();
    return !!(job && job.state === 'running');
}

function resetUpdateCheck() {
    // 一次 GET 拿不到 behind/updateAvailable（它不联网），留着上一次的 done=true
    // 就会把"没查"画成"已是最新"。
    updateCheck = { done: false, error: '' };
}

// ---------------------------------------------------------------------------
// 请求层
// ---------------------------------------------------------------------------

// 每个调用都要拿到状态码和解析后的体：apply/rollback 的 409 里带着那个正在跑的任务，
// 直接 throw 掉就等于把"已经有一次在跑了，这是它的进度"扔了。
// ok 不等于状态码：rollback 在"源码回了、二进制没回"这种半途情况下是按 202 回答并带着
// error 字段的（handler 里的 statusForError 把 no_toolchain 归到 202），只信 resp.ok
// 就会对着一次没做完的回滚播报成功。
async function runUpdateRequest(method, url, body) {
    const resp = await apiFetch(url, {
        method: method,
        headers: { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    const data = await resp.json().catch(() => ({}));
    const refused = !!(data && typeof data.error === 'string' && data.error);
    return {
        ok: !!resp.ok && !refused,
        status: resp.status,
        data: data || {},
        error: refused ? data.error : ('HTTP ' + resp.status),
    };
}

async function fetchUpdateStatus() {
    const r = await runUpdateRequest('GET', '/api/system/update');
    if (!r.ok) throw new Error(r.error);
    updateConsoleState = {
        status: r.data.status || {},
        job: r.data.job || null,
        canRestart: !!r.data.canRestart,
    };
    return updateConsoleState;
}

// notify 只在平台真的载了 showNotification 时才动。它绝不能成为"操作看起来失败"的原因：
// 请求已经成功了，为了播报一句去调一个页面上不存在的函数，会把成功炸成失败。
function notify(message, type) {
    if (typeof showNotification === 'function') {
        try {
            showNotification(message, type);
        } catch (e) {
            console.warn('notify', e);
        }
    }
}

async function withUpdateBusy(fn, label) {
    if (updateBusy) return;
    updateBusy = true;
    try {
        await fn();
    } catch (err) {
        const reason = err && err.message ? err.message : String(err);
        const text = updateT(UPDATE_FAILURE_KEYS[label] || 'loadFailed', { reason: reason });
        const el = document.getElementById('update-console');
        if (el && isUpdateConsoleActive()) {
            // 先按当前状态重绘再挂横幅：请求炸在半路上时，画面还停在"正在检查远端…"这种
            // 已经不再成立的状态上，光加一条红字是盖不住它的。
            renderUpdateConsole();
            el.insertAdjacentHTML('afterbegin', '<div class="update-error">' + escapeHtml(text) + '</div>');
        }
        // 内联横幅会被下一次轮询重绘冲掉，toast 才是留得住的那一份。
        notify(text, 'error');
    } finally {
        updateBusy = false;
    }
}

// ---------------------------------------------------------------------------
// 入口与轮询
// ---------------------------------------------------------------------------

async function loadUpdateConsole() {
    stopUpdatePolling();
    resetUpdateCheck();
    updateRestartChoice = false;
    const el = document.getElementById('update-console');
    if (!el) return;
    el.innerHTML = '<div class="empty-state">' + escapeHtml(updateT('loading')) + '</div>';
    try {
        await fetchUpdateStatus();
    } catch (err) {
        el.innerHTML = '<div class="empty-state">' +
            escapeHtml(updateT(UPDATE_FAILURE_KEYS.load, { reason: err.message })) + '</div>';
        return;
    }
    renderUpdateConsole();
    // 开页就有一次在跑的更新（比如另开一个标签页点的），接着看它，别让人以为没在动。
    if (isUpdateJobRunning()) startUpdatePolling();
}

// 只有一个定时器：不清就重新 arm，是"页面开两次之后两条轮询往同一份日志里写"的来路。
function startUpdatePolling() {
    stopUpdatePolling();
    updatePollTimer = setInterval(pollUpdateJob, UPDATE_POLL_INTERVAL_MS);
}

function stopUpdatePolling() {
    if (updatePollTimer) clearInterval(updatePollTimer);
    updatePollTimer = null;
}

async function pollUpdateJob() {
    if (!isUpdateConsoleActive()) {
        // 页面已经切走了。router.js 只在进入时调 initPage，离开时没人喊，
        // 所以这里自己停，不留野定时器。
        stopUpdatePolling();
        return;
    }
    const r = await runUpdateRequest('GET', '/api/system/update/job');
    if (!r.ok) {
        stopUpdatePolling();
        notify(updateT('jobPollFailed', { reason: r.error }), 'error');
        return;
    }
    const job = r.data && r.data.job ? r.data.job : null;
    if (updateConsoleState) updateConsoleState.job = job;
    renderUpdateConsole();
    if (job && job.state !== 'running') {
        stopUpdatePolling();
        reportFinishedJob(job);
    }
}

// 播报只念服务端写下来的结论：成功念 result 的两个提交号，失败念 failure.message，
// 页面自己不在这里编"更新完成"。
function reportFinishedJob(job) {
    const result = job.result || null;
    if (job.state === 'succeeded') {
        const tail = result && result.toCommit ? ' ' + result.fromCommit + ' → ' + result.toCommit : '';
        notify(updateT('jobDoneToast') + tail, 'success');
        if (result && result.needsRestart) notify(updateT('needsRestartToast'), 'info');
    } else {
        const reason = (job.failure && job.failure.message) ? job.failure.message : updateT('failureNoMessage');
        notify(updateT('jobFailedToast') + ': ' + reason, 'error');
    }
    resetUpdateCheck();
    // 二进制换过了，本机状态得重读一遍；读不到就照实说读不到。
    fetchUpdateStatus().then(renderUpdateConsole).catch(err => {
        notify(updateT(UPDATE_FAILURE_KEYS.load, { reason: err.message }), 'error');
    });
}

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

async function checkForUpdates() {
    await withUpdateBusy(async () => {
        updateChecking = true;
        renderUpdateConsole();
        let r;
        try {
            r = await runUpdateRequest('POST', '/api/system/update/check');
        } finally {
            // 请求本身炸掉（401、网络断了）也必须把"正在检查"放下来，否则按钮永远灰着。
            updateChecking = false;
        }
        if (!r.ok) {
            // HTTP 层的失败同样是"查不了"，不能留下一个看起来像"没有更新"的页面。
            updateCheck = { done: false, error: r.error };
            renderUpdateConsole();
            notify(updateT(UPDATE_FAILURE_KEYS.check, { reason: r.error }), 'error');
            return;
        }
        const status = r.data.status || {};
        if (updateConsoleState) updateConsoleState.status = status;
        updateCheck = { done: true, error: status.checkError || '' };
        renderUpdateConsole();
        if (updateCheck.error) {
            notify(updateT(UPDATE_FAILURE_KEYS.check, { reason: updateCheck.error }), 'error');
        } else if (status.updateAvailable) {
            notify(updateT('behindCount', { n: updateBehindCount(status) }), 'info');
        } else {
            notify(updateT('upToDate', { commit: status.commit || '-' }), 'success');
        }
    }, 'check');
}

function updateBehindCount(status) {
    const listed = (status.incoming || []).length;
    const total = status.incomingTotal || status.behind || 0;
    return total > 0 ? total : listed;
}

// 按钮为什么点不动，得说得出原因；"灰着"不算解释。
function updateApplyBlockers(status, job) {
    const blockers = [];
    if (!status.installed) blockers.push(updateT('blockedNotGitTree'));
    if (job && job.state === 'running') blockers.push(updateT('blockedJobRunning'));
    if ((status.blockingChanges || []).length) {
        blockers.push(updateT('blockedLocalEdits', { n: status.blockingChanges.length }));
    }
    if (status.diverged) {
        blockers.push(updateT('blockedDiverged', {
            ahead: status.ahead || 0,
            behind: status.behind || 0,
        }));
    }
    if (!status.updateAvailable) blockers.push(updateT('blockedNoUpdate'));
    return blockers;
}

function updateRestartChoiceChanged(checked) {
    updateRestartChoice = !!checked;
    renderUpdateConsole();
}

// 勾选框是唯一的真相来源；模块里那份只是给轮询重绘用的记忆。
function updateRestartRequested() {
    const box = document.getElementById('update-restart-choice');
    if (box && typeof box.checked === 'boolean') return box.checked;
    return updateRestartChoice;
}

function updateConfirmText(status, restart) {
    const base = updateT('confirmApply', {
        remote: status.remote || '-',
        branch: status.branch || '-',
        commit: status.commit || '-',
        target: status.remoteCommit || '-',
        n: updateBehindCount(status),
    });
    return base + ' ' + (restart ? updateT('confirmRestartYes') : updateT('confirmRestartNo'));
}

async function startUpdateApply() {
    if (updateBusy) return;
    const status = updateStatusOf();
    const blockers = updateApplyBlockers(status, updateJobOf());
    if (blockers.length) {
        // 浏览器不会把 disabled 按钮的点击送到这里，走到这儿说明状态已经和画面不一致了，
        // 那就照实说为什么不能点，一声不吭更像坏了。
        notify(blockers[0], 'error');
        return;
    }
    const restart = updateRestartRequested();
    if (!window.confirm(updateConfirmText(status, restart))) return;

    await withUpdateBusy(async () => {
        const r = await runUpdateRequest('POST', '/api/system/update/apply', { restart: restart });
        if (r.status === 409 && r.data.job) {
            // 另一次更新真的在跑：把它的进度接下来并接着看，这才是"当前状态"。
            if (updateConsoleState) updateConsoleState.job = r.data.job;
            renderUpdateConsole();
            startUpdatePolling();
        }
        if (!r.ok) throw new Error(r.error);
        resetUpdateCheck();
        startUpdatePolling();
        notify(updateT('applyAccepted', { id: r.data.job_id || '-' }), 'success');
        await pollUpdateJob();
    }, 'apply');
}

async function rollbackUpdate() {
    if (updateBusy) return;
    const status = updateStatusOf();
    if (!status.hasRollback) return;
    if (isUpdateJobRunning()) {
        notify(updateT('blockedJobRunning'), 'error');
        return;
    }
    if (!window.confirm(updateT('confirmRollback', { commit: status.rollbackTo || '-' }))) return;

    await withUpdateBusy(async () => {
        const r = await runUpdateRequest('POST', '/api/system/update/rollback');
        if (!r.ok) {
            // 服务端连"哪些文件没回滚"都给了，不能只报一句失败。
            const items = r.data && r.data.items && r.data.items.length
                ? ' · ' + updateT('rollbackItems') + ': ' + r.data.items.join(', ')
                : '';
            throw new Error(r.error + items);
        }
        const to = (r.data.result && r.data.result.toCommit) || status.rollbackTo || '-';
        resetUpdateCheck();
        updateRestartChoice = false;
        await fetchUpdateStatus();
        renderUpdateConsole();
        notify(updateT('rollbackDone', { commit: to }), 'success');
    }, 'rollback');
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

function updateSection(title, body) {
    return '<section class="update-section"><h3 class="update-section-title">' +
        escapeHtml(title) + '</h3>' + body + '</section>';
}

// 值既进文本也进 title（长路径要靠 title 看全），所以属性那一份必须走 escapeAttr。
function updateFact(label, value) {
    const text = value == null || value === '' ? '-' : String(value);
    return '<div class="update-fact"><span>' + escapeHtml(label) + '</span>' +
        '<code title="' + escapeAttr(text) + '">' + escapeHtml(text) + '</code></div>';
}

function updateMark(ok, yesKey, noKey, reason) {
    const cls = ok ? 'update-chip update-chip-ok' : 'update-chip update-chip-warn';
    if (!reason) {
        return '<span class="' + cls + '">' + escapeHtml(ok ? updateT(yesKey) : updateT(noKey)) + '</span>';
    }
    return '<span class="' + cls + '" title="' + escapeAttr(String(reason)) + '">' +
        escapeHtml(ok ? updateT(yesKey) : updateT(noKey)) + '</span>';
}

function renderUpdateInstallBody(status) {
    const facts = [
        updateFact(updateT('root'), status.root),
        updateFact(updateT('branch'), status.branch),
        updateFact(updateT('remote'), status.remote),
        updateFact(updateT('commit'), status.commit),
        updateFact(updateT('committedAt'), status.committedAt),
        updateFact(updateT('subject'), status.subject),
    ].join('');
    const marks = [
        updateMark(!!status.installed, 'gitTree', 'notGitTree'),
        updateMark(!!status.canBuild, 'toolchainFound', 'toolchainMissing',
            status.canBuild ? (status.goToolchain || '') : (status.goToolchain || updateT('toolchainMissingHint'))),
        updateMark(!!status.hasBinary, 'binaryPresent', 'binaryMissing'),
        updateMark(!!status.hasRollback, 'rollbackPresent', 'rollbackMissing'),
    ].join('');
    return '<div class="update-facts">' + facts + '</div><div class="update-marks">' + marks + '</div>';
}

function renderUpdateRemoteBody(status) {
    const failure = updateCheck.error || status.checkError || '';
    const parts = [];
    if (failure) {
        // 这条分支就是"不许说谎"的落点：有原因就只写原因，不提"已是最新"。
        parts.push('<div class="update-error update-check-failed">' +
            escapeHtml(updateT('checkFailed', { reason: failure })) + '</div>');
    } else if (!updateCheck.done) {
        parts.push('<div class="update-hint">' + escapeHtml(updateT('notChecked')) + '</div>');
    } else if (status.updateAvailable) {
        parts.push('<div class="update-behind">' +
            escapeHtml(updateT('behindCount', { n: updateBehindCount(status) })) + '</div>');
    } else {
        parts.push('<div class="update-ok">' +
            escapeHtml(updateT('upToDate', { commit: status.commit || '-' })) + '</div>');
    }

    if (status.ahead > 0) {
        parts.push('<div class="update-warn">' + escapeHtml(updateT('aheadCount', { n: status.ahead })) + '</div>');
    }
    if (status.remoteCommit) {
        parts.push('<div class="update-facts">' +
            updateFact(updateT('remoteHead'), status.remoteCommit) +
            updateFact(updateT('remoteSubject'), status.remoteSubject) + '</div>');
    }

    const incoming = status.incoming || [];
    if (incoming.length) {
        // incoming 是被截断过的清单，incomingTotal 才是真实数量，两个都得说。
        const total = status.incomingTotal || incoming.length;
        parts.push('<h4 class="update-sub-title">' + escapeHtml(updateT('incomingTitle')) + '</h4>');
        if (total > incoming.length) {
            parts.push('<div class="update-hint">' +
                escapeHtml(updateT('incomingTruncated', { total: total, shown: incoming.length })) + '</div>');
        }
        parts.push('<ul class="update-commit-list">' + incoming.map(c =>
            '<li><code title="' + escapeAttr(String(c.commit || '-')) + '">' + escapeHtml(String(c.commit || '-')) +
            '</code> <span class="update-commit-subject">' + escapeHtml(String(c.subject || '')) + '</span></li>'
        ).join('') + '</ul>');
    }
    return parts.join('');
}

function renderUpdateChangesTable(list) {
    // 行底色就是"这个文件会不会被更新覆盖"的分区：本机内容绿、产品源码红。
    const rows = (list || []).map(c => '<tr class="' + (c.protected ? 'update-row-kept' : 'update-row-blocking') + '">' +
        '<td><code title="' + escapeAttr(String(c.path || '')) + '">' + escapeHtml(String(c.path || '-')) + '</code></td>' +
        '<td>' + escapeHtml(String(c.status || '-')) + '</td>' +
        '<td>' + escapeHtml(c.protected ? updateT('keptFile') : updateT('productSource')) + '</td></tr>').join('');
    return '<table class="update-table"><thead><tr>' +
        '<th>' + escapeHtml(updateT('colPath')) + '</th>' +
        '<th>' + escapeHtml(updateT('colChangeStatus')) + '</th>' +
        '<th>' + escapeHtml(updateT('colOwner')) + '</th>' +
        '</tr></thead><tbody>' + rows + '</tbody></table>';
}

function renderUpdateRestartChoice() {
    if (!updateCanRestart()) {
        // 后端没给重启钩子，勾选框就不该出现：勾一个不会被兑现的选项是骗人。
        return '<div class="update-hint">' + escapeHtml(updateT('restartNotWired')) + '</div>';
    }
    const preview = updateRestartChoice ? updateT('confirmRestartYes') : updateT('confirmRestartNo');
    return '<label class="update-restart-choice">' +
        '<input type="checkbox" id="update-restart-choice"' +
        (updateRestartChoice ? ' checked' : '') +
        ' onchange="updateRestartChoiceChanged(this.checked)">' +
        '<span>' + escapeHtml(updateT('restartChoiceLabel')) + '</span></label>' +
        '<div class="update-hint update-restart-preview">' + escapeHtml(preview) + '</div>';
}

function renderUpdateApplyBody(status, job) {
    const blockers = updateApplyBlockers(status, job);
    const blocked = blockers.length > 0;
    const parts = [];

    if (job && job.state === 'running') {
        parts.push('<div class="update-warn">' + escapeHtml(updateT('jobRunningHint')) + '</div>');
    }
    if (!status.canBuild && status.installed) {
        parts.push('<div class="update-warn">' + escapeHtml(updateT('noToolchainWarn')) + '</div>');
    }

    // Both halves matter: `disabled` for the browser, `data-state-disabled` so the platform's
    // permission gate (which writes el.disabled for every [data-require-permission] element)
    // does not re-enable the button for a user who holds update:apply. In a real browser that
    // write produced an enabled 一键更新 directly under "有 53 个产品源码文件被本地改过".
    const stateDisabled = blocked ? ' disabled data-state-disabled="true"' : '';
    const checkingDisabled = updateChecking ? ' disabled data-state-disabled="true"' : '';
    parts.push('<div class="update-actions">' +
        '<button class="btn-primary update-apply-btn"' + stateDisabled +
        ' data-require-permission="update:apply" onclick="startUpdateApply()">' +
        escapeHtml(updateT('applyBtn')) + '</button>' +
        '<button class="btn-secondary update-check-btn"' + checkingDisabled +
        ' data-require-permission="update:apply" onclick="checkForUpdates()">' +
        escapeHtml(updateChecking ? updateT('checking') : updateT('checkBtn')) + '</button>' +
        '</div>');

    parts.push(renderUpdateRestartChoice());

    if (blocked) {
        parts.push('<ul class="update-blocker-list">' + blockers.map(b =>
            '<li class="update-error">' + escapeHtml(b) + '</li>').join('') + '</ul>');
    }

    const local = status.localChanges || [];
    const blocking = status.blockingChanges || [];
    // localChanges 通常是 blockingChanges 的超集，但两者万一不一致，阻塞更新的文件绝不能因此
    // 不列出来：上面那句"有 N 个文件被本地改过"点名的就是这些得人去处理的文件名。
    const seenPaths = Object.create(null);
    const listed = local.concat(blocking).filter(c => {
        const p = String((c && c.path) || '');
        if (!p || seenPaths[p]) return false;
        seenPaths[p] = true;
        return true;
    });
    if (listed.length) {
        parts.push('<h4 class="update-sub-title">' + escapeHtml(updateT('localChangesTitle')) + '</h4>');
        parts.push('<div class="update-hint">' + escapeHtml(updateT('localChangesHint')) + '</div>');
        parts.push(renderUpdateChangesTable(listed));
    } else {
        parts.push('<div class="update-hint">' + escapeHtml(updateT('noLocalChanges')) + '</div>');
    }
    return parts.join('');
}

function updateStateChip(state) {
    const known = { running: 'update-chip-running', succeeded: 'update-chip-ok', failed: 'update-chip-danger' };
    const cls = known[state] || 'update-chip';
    const label = state === 'running' ? updateT('stateRunning')
        : state === 'succeeded' ? updateT('stateSucceeded')
            : state === 'failed' ? updateT('stateFailed') : updateT('stateUnknown');
    return '<span class="' + cls + '">' + escapeHtml(label) + '</span>';
}

function renderUpdateStep(step) {
    return '<li class="update-step">' +
        '<code class="update-step-phase">' + escapeHtml(String(step.phase || '-')) + '</code>' +
        '<span class="update-step-message">' + escapeHtml(String(step.message || '')) + '</span>' +
        '<span class="update-step-at">' + escapeHtml(String(step.at || '')) + '</span></li>';
}

function renderUpdateResult(result) {
    if (!result) return '';
    const kept = result.keptContent || [];
    const facts = [
        updateFact(updateT('resultFrom'), result.fromCommit),
        updateFact(updateT('resultTo'), result.toCommit),
        updateFact(updateT('resultCommits'), result.commits),
        updateFact(updateT('resultFiles'), result.filesTouched),
        updateFact(updateT('resultDuration'), result.duration),
        updateFact(updateT('resultBackup'), result.backupDir),
    ].join('');
    const marks = [
        updateMark(!!result.binaryBuilt, 'binarySwapped', 'binaryNotSwapped', result.binaryPath || ''),
        updateMark(!!result.needsRestart, 'restartNeeded', 'restartNotNeeded'),
    ].join('');
    const keptList = kept.length
        ? '<ul class="update-kept-list">' + kept.map(p =>
            '<li><code title="' + escapeAttr(String(p)) + '">' + escapeHtml(String(p)) + '</code></li>').join('') + '</ul>'
        : '<div class="update-hint">' + escapeHtml(updateT('nothingKept')) + '</div>';
    return '<div class="update-result">' +
        '<h4 class="update-sub-title">' + escapeHtml(updateT('resultTitle')) + '</h4>' +
        '<div class="update-facts">' + facts + '</div><div class="update-marks">' + marks + '</div>' +
        '<h4 class="update-sub-title">' + escapeHtml(updateT('keptTitle')) + '</h4>' + keptList +
        '</div>';
}

function renderUpdateFailure(failure) {
    if (!failure) return '';
    const items = failure.items || [];
    const reason = failure.reason ? '（' + escapeHtml(String(failure.reason)) + '）' : '';
    return '<div class="update-error update-failure">' +
        '<div class="update-failure-title">' + escapeHtml(updateT('failureTitle')) + reason + '</div>' +
        '<div class="update-failure-message">' +
        escapeHtml(String(failure.message || updateT('failureNoMessage'))) + '</div>' +
        (items.length
            ? '<ul class="update-item-list">' + items.map(i =>
                '<li><code title="' + escapeAttr(String(i)) + '">' + escapeHtml(String(i)) + '</code></li>').join('') + '</ul>'
            : '') +
        '</div>';
}

function renderUpdateJobBody(job) {
    if (!job) return '<div class="empty-state">' + escapeHtml(updateT('noJob')) + '</div>';
    const parts = ['<div class="update-job-head">' + updateStateChip(job.state) +
        '<code title="' + escapeAttr(String(job.id || '-')) + '">' + escapeHtml(String(job.id || '-')) + '</code>' +
        (job.started ? '<span class="update-job-time">' + escapeHtml(String(job.started)) + '</span>' : '') +
        (job.restartRequested ? '<span class="update-chip">' + escapeHtml(updateT('restartRequested')) + '</span>' : '') +
        '</div>'];
    const steps = job.steps || [];
    parts.push(steps.length
        ? '<ol class="update-step-list">' + steps.map(renderUpdateStep).join('') + '</ol>'
        : '<div class="empty-state">' + escapeHtml(updateT('noSteps')) + '</div>');
    if (job.state === 'running') {
        parts.push('<div class="update-hint">' +
            escapeHtml(updateT('pollingHint', { sec: UPDATE_POLL_INTERVAL_MS / 1000 })) + '</div>');
    }
    if (job.result) parts.push(renderUpdateResult(job.result));
    if (job.failure) parts.push(renderUpdateFailure(job.failure));
    return parts.join('');
}

function renderUpdateRollbackBody(status) {
    if (!status.hasRollback) {
        return '<div class="empty-state">' + escapeHtml(updateT('noRollback')) + '</div>';
    }
    const running = isUpdateJobRunning();
    return '<p class="update-hint">' +
        escapeHtml(updateT('rollbackTarget', { commit: status.rollbackTo || '-' })) + '</p>' +
        '<div class="update-actions"><button class="btn-secondary update-rollback-btn"' +
        (running ? ' disabled data-state-disabled="true"' : '') + ' data-require-permission="update:apply" ' +
        'onclick="rollbackUpdate()">' + escapeHtml(updateT('rollbackBtn')) + '</button></div>';
}

function renderUpdateConsole() {
    const el = document.getElementById('update-console');
    if (!el || !updateConsoleState) return;
    const status = updateStatusOf();
    const job = updateJobOf();

    const parts = [];
    parts.push(updateSection(updateT('installTitle'), renderUpdateInstallBody(status)));
    parts.push(updateSection(updateT('remoteTitle'), renderUpdateRemoteBody(status)));
    parts.push(updateSection(updateT('applyTitle'), renderUpdateApplyBody(status, job)));
    parts.push(updateSection(updateT('jobTitle'), renderUpdateJobBody(job)));
    parts.push(updateSection(updateT('rollbackTitle'), renderUpdateRollbackBody(status)));

    el.innerHTML = parts.join('');
    if (typeof window.applyRBACToUI === 'function') {
        window.applyRBACToUI(el);
    }
}

window.loadUpdateConsole = loadUpdateConsole;
window.checkForUpdates = checkForUpdates;
window.startUpdateApply = startUpdateApply;
window.rollbackUpdate = rollbackUpdate;
window.updateRestartChoiceChanged = updateRestartChoiceChanged;
window.stopUpdatePolling = stopUpdatePolling;
window.renderUpdateConsole = renderUpdateConsole;
