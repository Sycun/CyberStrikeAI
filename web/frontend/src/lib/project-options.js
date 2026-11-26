export function isArchivedProject(project) {
    return (project?.status || 'active') === 'archived';
}

export function formatProjectOptionLabel(project, t, fallbackLabel = '') {
    const base = project?.name || project?.id || fallbackLabel;
    if (!isArchivedProject(project)) return base;
    return `${base}（${t('projects.statusArchived', null, '已归档')}）`;
}

/**
 * 新建/绑定表单的项目下拉项：仅列出进行中的项目；
 * 若当前记录已绑定到某个已归档项目（currentId），保留该项并标注“已归档”。
 */
export function activeProjectOptions(projects, t, { currentId = '', disableCurrentArchived = false, fallbackLabel = '' } = {}) {
    const seen = new Set();
    const out = [];
    (Array.isArray(projects) ? projects : []).forEach(project => {
        if (!project?.id || seen.has(project.id)) return;
        const archived = isArchivedProject(project);
        if (archived && project.id !== currentId) return;
        seen.add(project.id);
        out.push({
            value: project.id,
            label: formatProjectOptionLabel(project, t, fallbackLabel),
            ...(archived && disableCurrentArchived ? { disabled: true } : {})
        });
    });
    return out;
}
