/* Navigation uses stable assessment/step IDs; names and tags are display only. */
(function () {
    'use strict';
    const labels = { source: '来源深度测评', step: '来源步骤', openStep: '返回测评步骤', evidence: '查看步骤与复现证据', deleted: '测评已删除，漏洞记录仍保留', unavailable: '当前账号不能打开关联测评' };
    const t = key => { const path = 'assessmentFindingLinks.' + key; const text = typeof window.t === 'function' ? window.t(path) : ''; return text && text !== path ? text : labels[key]; };
    const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
    function href(v, tab = 'plan') {
        return '#deep-assessments?' + new URLSearchParams({ assessment_id: v.assessment_id, step_id: v.assessment_step_id || '', tab, vulnerability_id: v.id });
    }
    function render(v) {
        if (!v.assessment_id) return '';
        const links = v.assessment_can_open && !v.assessment_deleted
            ? `<div class="vulnerability-inspector-actions"><a class="btn-secondary" data-assessment-jump href="${escape(href(v))}">${escape(t('openStep'))}</a><a class="btn-secondary" data-assessment-jump href="${escape(href(v, 'evidence'))}">${escape(t('evidence'))}</a></div>`
            : `<p class="da-muted">${escape(t(v.assessment_deleted ? 'deleted' : 'unavailable'))}</p>`;
        return `<section class="vulnerability-assessment-source"><strong>${escape(t('source'))}：${escape(v.assessment_name || v.assessment_id)}</strong><p>${escape(t('step'))}：${escape(v.assessment_step_title || v.assessment_step_id)}</p>${links}</section>`;
    }
    function openFinding(id, assessmentID = '') {
        const params = new URLSearchParams({ view: 'task' });
        if (id) params.set('id', id);
        if (assessmentID) params.set('assessment_id', assessmentID);
        const alreadyOpen = document.getElementById('page-vulnerabilities')?.classList.contains('active');
        history.replaceState(null, '', '#vulnerabilities?' + params);
        if (typeof window.switchPage === 'function') window.switchPage('vulnerabilities');
        // New-page initialization loads once and opens the exact finding. When
        // already on this page, update its filters and let that same path run.
        if (alreadyOpen) {
            if (typeof window.syncVulnerabilityFiltersFromLocationHash === 'function') window.syncVulnerabilityFiltersFromLocationHash();
            if (typeof window.refreshVulnerabilities === 'function') window.refreshVulnerabilities();
        }
    }
    document.addEventListener('click', event => {
        const link = event.target.closest?.('a[data-assessment-jump]');
        if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        event.preventDefault();
        if (typeof window.closeVulnerabilityInspectorForNavigation === 'function') window.closeVulnerabilityInspectorForNavigation();
        else if (typeof window.closeAppModal === 'function') window.closeAppModal('vulnerability-inspector');
        history.replaceState(null, '', link.hash);
        const alreadyOpen = document.getElementById('page-deep-assessments')?.classList.contains('active');
        if (typeof window.switchPage === 'function') window.switchPage('deep-assessments');
        if (alreadyOpen) window.DeepAssessments?.fromLocation();
    });
    window.AssessmentFindingLinks = { href, render, openFinding };
})();
