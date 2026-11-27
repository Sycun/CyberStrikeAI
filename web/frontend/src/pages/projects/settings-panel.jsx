/** 项目设置：元数据、测试范围 JSON、归档/恢复与删除。 */
import { useEffect, useId, useState } from 'react';
import { Button, Divider, Input, Radio, RadioGroup, Select, Space, Switch, TextArea, Typography } from '@douyinfe/semi-ui';
import { api } from '../../lib/http';
import { useT } from '../../lib/i18n';
import { SectionCard, confirmAction, toastError, toastSuccess } from '../../components/ui';
import { PROJECT_STATUS, errText } from './helpers';

const { Text } = Typography;

const SCOPE_EXAMPLE = {
    targets: ['https://example.com'],
    exclude: ['*.cdn.example.com'],
    notes: '仅授权 Web 应用层测试'
};

function readScopeObject(raw) {
    try {
        const scope = raw.trim() ? JSON.parse(raw) : {};
        return scope && typeof scope === 'object' && !Array.isArray(scope) ? scope : null;
    } catch {
        return null;
    }
}

export default function SettingsPanel({ project, canWrite, canDelete, onChanged, onDeleted }) {
    const t = useT();
    const labelId = useId();
    const [form, setForm] = useState({ name: '', description: '', scope_json: '', lessons_mode: 'off', status: 'active', pinned: false });
    const [saving, setSaving] = useState(false);

    useEffect(() => {
        if (!project) return;
        setForm({
            name: project.name || '',
            description: project.description || '',
            scope_json: project.scope_json || '',
            lessons_mode: project.lessons_mode || 'off',
            status: project.status || 'active',
            pinned: Boolean(project.pinned)
        });
    }, [project]);

    const update = patch => setForm(prev => ({ ...prev, ...patch }));
    const scope = readScopeObject(form.scope_json);
    const constraints = scope?.constraints ?? [];
    const editableConstraints = Array.isArray(constraints) &&
        constraints.every(rule => rule && typeof rule === 'object' && !Array.isArray(rule));
    const updateScope = patch => setForm(prev => {
        const current = readScopeObject(prev.scope_json);
        return current ? { ...prev, scope_json: JSON.stringify({ ...current, ...patch }, null, 2) } : prev;
    });
    const updateConstraint = (index, patch) => updateScope({
        constraints: constraints.map((rule, i) => i === index ? { ...rule, ...patch } : rule)
    });
    const noiseProfiles = [
        { value: 'off', label: t('projects.noiseOff', null, '关闭') },
        { value: 'quiet', label: t('projects.noiseQuiet', null, '低噪音') },
        { value: 'balanced', label: t('projects.noiseBalanced', null, '均衡') },
        { value: 'aggressive', label: t('projects.noiseAggressive', null, '维护窗口') }
    ];
    const constraintKinds = [
        { value: 'allow', label: t('projects.constraintAllow', null, '允许') },
        { value: 'deny', label: t('projects.constraintDeny', null, '禁止') }
    ];

    const formatScope = () => {
        const raw = form.scope_json.trim();
        if (!raw) return;
        try {
            update({ scope_json: JSON.stringify(JSON.parse(raw), null, 2) });
        } catch (error) {
            toastError(t('projects.invalidJson', null, 'JSON 格式无效') + '：' + errText(error, ''));
        }
    };

    const insertExample = () => update({ scope_json: JSON.stringify(SCOPE_EXAMPLE, null, 2) });

    const save = async () => {
        const name = form.name.trim();
        if (!name) {
            toastError(t('projects.enterProjectName', null, '请输入项目名称'));
            return;
        }
        const scopeRaw = form.scope_json.trim();
        if (scopeRaw) {
            try {
                JSON.parse(scopeRaw);
            } catch (error) {
                toastError(t('projects.invalidScopeJson', null, '测试范围 JSON 无效，请先修正或点击「格式化」'));
                return;
            }
        }
        setSaving(true);
        try {
            await api.put(`/projects/${project.id}`, {
                name,
                description: form.description,
                scope_json: scopeRaw,
                ...(form.lessons_mode !== (project.lessons_mode || 'off') ? { lessons_mode: form.lessons_mode } : {}),
                ...(form.status !== (project.status || 'active') ? { status: form.status } : {}),
                pinned: Boolean(form.pinned)
            });
            toastSuccess(t('projects.saved', null, '已保存'));
            onChanged?.();
        } catch (error) {
            toastError(errText(error, t('projects.saveFailed', null, '保存失败')));
        } finally {
            setSaving(false);
        }
    };

    const archived = (project?.status || 'active') === 'archived';

    const toggleArchive = () => {
        confirmAction({
            title: archived ? t('projects.restoreProjectActive', null, '恢复为进行中') : t('projects.archiveProject', null, '归档'),
            content: archived
                ? t('projects.confirmRestoreProjectActive', null, '恢复后，项目记忆与随项目归档的漏洞将重新启用。单独归档的漏洞仍保留在归档列表。')
                : t('projects.confirmArchiveProject', null, '项目记忆和关联漏洞将随项目归档，可从已归档项目中查阅。是否继续？'),
            okText: t('common.confirm', null, '确认'),
            danger: !archived,
            onOk: async () => {
                try {
                    await api.put(`/projects/${project.id}`, { status: archived ? 'active' : 'archived' });
                    toastSuccess(t('projects.saved', null, '已保存'));
                    onChanged?.();
                } catch (error) {
                    toastError(errText(error, t('projects.operationFailed', null, '操作失败')));
                }
            }
        });
    };

    const remove = () => {
        confirmAction({
            title: t('projects.deleteProject', null, '删除项目'),
            content: t('projects.confirmDeleteProject', null, '确定删除该项目？事实将一并删除，对话将解除绑定。'),
            okText: t('common.delete', null, '删除'),
            onOk: async () => {
                try {
                    await api.del(`/projects/${project.id}`);
                    toastSuccess(t('projects.deleteProject', null, '删除项目'));
                    onDeleted?.(project.id);
                } catch (error) {
                    toastError(errText(error, t('projects.deleteFailed', null, '删除失败')));
                }
            }
        });
    };

    if (!project) {
        return (
            <SectionCard title={t('projects.tabSettings', null, '设置')}>
                <Text type="tertiary">{t('projects.selectOrCreateTitle', null, '选择或创建项目')}</Text>
            </SectionCard>
        );
    }

    return (
        <SectionCard
            title={t('projects.settingsIntroTitle', null, '项目设置')}
            description={t('projects.settingsIntroHint', null, '配置项目元数据与 Agent 授权边界，保存后即时生效于绑定对话。')}
            extra={(
                <Button type="primary" loading={saving} disabled={!canWrite} onClick={save}>
                    {t('projects.saveSettings', null, '保存更改')}
                </Button>
            )}
        >
            <div className="project-form-grid">
                <div className="project-form-field">
                    <Text strong size="small">{t('projects.projectName', null, '项目名称')}</Text>
                    <Input value={form.name} disabled={!canWrite} onChange={value => update({ name: value })} />
                    <Text type="tertiary" size="small">{t('projects.saveChangesHint', null, '修改后请点击保存以同步到服务器')}</Text>
                </div>
                <div className="project-form-field">
                    <Text strong size="small">{t('projects.status', null, '状态')}</Text>
                    <Select
                        value={form.status}
                        disabled={!canWrite}
                        optionList={PROJECT_STATUS}
                        onChange={value => update({ status: value })}
                        style={{ width: '100%' }}
                    />
                    <div className="project-switch-row">
                        <Switch checked={form.pinned} disabled={!canWrite} onChange={checked => update({ pinned: checked })} />
                        <Text size="small">{t('projects.pinProject', null, '置顶项目（列表优先显示）')}</Text>
                    </div>
                </div>
            </div>

            <div className="project-form-field">
                <Text strong size="small">{t('projects.projectDescription', null, '项目描述')}</Text>
                <TextArea
                    value={form.description}
                    rows={3}
                    maxCount={4000}
                    disabled={!canWrite}
                    placeholder={t('projects.editDescriptionPlaceholder', null, '客户/任务备注、协作说明、联系人…')}
                    onChange={value => update({ description: value })}
                />
            </div>

            <div className="project-form-field">
                <Space>
                    <Text strong size="small">{t('projects.scopeJsonLabel', null, '范围 JSON')}</Text>
                    <Button size="small" theme="borderless" disabled={!canWrite} onClick={formatScope}>
                        {t('projects.formatJson', null, '格式化')}
                    </Button>
                    <Button size="small" theme="borderless" disabled={!canWrite} onClick={insertExample}>
                        {t('projects.example', null, '示例')}
                    </Button>
                </Space>
                <TextArea
                    value={form.scope_json}
                    rows={8}
                    disabled={!canWrite}
                    placeholder={'{\n  "targets": ["https://example.com"]\n}'}
                    onChange={value => update({ scope_json: value })}
                />
                <Text type="tertiary" size="small">
                    {t('projects.scopeStructuredHint', null, '范围、排除项和操作约束保存在同一份 JSON。新增目标需明确授权。')}
                </Text>
                {!scope || !editableConstraints ? (
                    <Text type="warning" size="small">
                        {t('projects.scopeStructuredInvalid', null, '请在 JSON 中修正范围对象和操作约束列表。')}
                    </Text>
                ) : (
                    <>
                        <div className="project-form-field">
                            <Text strong size="small">{t('projects.scopeConstraints', null, '操作约束')}</Text>
                            {constraints.map((rule, index) => (
                                <Space key={index} wrap>
                                    <Text size="small"><span id={`${labelId}-constraint-${index}`}>{t('projects.constraintKind', null, '约束类型')}</span></Text>
                                    <RadioGroup
                                        type="button"
                                        aria-labelledby={`${labelId}-constraint-${index}`}
                                        disabled={!canWrite}
                                        value={rule.kind}
                                        onChange={event => updateConstraint(index, { kind: event.target.value })}
                                    >
                                        {constraintKinds.map(({ value, label }) => <Radio key={value} value={value}>{label}</Radio>)}
                                    </RadioGroup>
                                    <Input
                                        aria-label={t('projects.constraintText', null, '约束内容')}
                                        value={typeof rule.text === 'string' ? rule.text : ''}
                                        disabled={!canWrite}
                                        onChange={value => updateConstraint(index, { text: value })}
                                    />
                                    <Text size="small">{rule.origin === 'goals'
                                        ? t('projects.constraintGoals', null, '目标建议，待人工确认')
                                        : t('projects.constraintHuman', null, '人工')}</Text>
                                    <Button size="small" disabled={!canWrite} onClick={() => updateScope({
                                        constraints: constraints.filter((_, i) => i !== index)
                                    })}>
                                        {t('common.delete', null, '删除')}
                                    </Button>
                                </Space>
                            ))}
                            <Button size="small" disabled={!canWrite} onClick={() => updateScope({
                                constraints: [...constraints, { kind: 'deny', text: '', origin: 'human' }]
                            })}>
                                {t('projects.addScopeConstraint', null, '添加约束')}
                            </Button>
                        </div>
                        <div className="project-form-field">
                            <Text strong size="small"><span id={`${labelId}-noise`}>{t('projects.noiseProfile', null, '噪音档位')}</span></Text>
                            <RadioGroup
                                type="button"
                                aria-labelledby={`${labelId}-noise`}
                                disabled={!canWrite}
                                value={scope.noise_profile || 'off'}
                                onChange={event => updateScope({ noise_profile: event.target.value })}
                            >
                                {noiseProfiles.map(({ value, label }) => <Radio key={value} value={value}>{label}</Radio>)}
                            </RadioGroup>
                        </div>
                    </>
                )}
            </div>

            <div className="project-switch-row">
                <Switch
                    aria-labelledby={`${labelId}-lessons`}
                    checked={form.lessons_mode === 'shadow' || form.lessons_mode === 'inject'}
                    disabled={!canWrite}
                    onChange={checked => update({ lessons_mode: checked ? 'shadow' : 'off' })}
                />
                <Text size="small"><span id={`${labelId}-lessons`}>{t('projects.lessonsMode', null, '处置经验影子审计')}</span></Text>
                {(form.lessons_mode === 'shadow' || form.lessons_mode === 'inject') ? (
                    <>
                        <Switch
                            aria-labelledby={`${labelId}-lessons-inject`}
                            checked={form.lessons_mode === 'inject'}
                            disabled={!canWrite}
                            onChange={checked => update({ lessons_mode: checked ? 'inject' : 'shadow' })}
                        />
                        <Text size="small"><span id={`${labelId}-lessons-inject`}>{t('projects.lessonsInject', null, '注入活跃经验到上下文')}</span></Text>
                    </>
                ) : null}
            </div>

            <Divider margin="12px" />
            <div className="project-danger-zone">
                <div>
                    <Text strong>{t('projects.dangerZoneTitle', null, '危险操作')}</Text>
                    <div>
                        <Text type="tertiary" size="small">
                            {t('projects.dangerZoneHint', null, '在项目列表选择「已归档」可查看归档项目。删除项目会永久删除项目记忆。')}
                        </Text>
                    </div>
                </div>
                <Space>
                    <Button disabled={!canWrite} onClick={toggleArchive}>
                        {archived ? t('projects.restoreProjectActive', null, '恢复为进行中') : t('projects.archiveProject', null, '归档')}
                    </Button>
                    <Button type="danger" disabled={!canDelete} onClick={remove}>
                        {t('projects.deleteProject', null, '删除项目')}
                    </Button>
                </Space>
            </div>
        </SectionCard>
    );
}
