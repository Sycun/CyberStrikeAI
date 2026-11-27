import { Button, Space, Tag, Typography } from '@douyinfe/semi-ui';
import { DataTable, ErrorBanner, SectionCard } from '../../components/ui';
import { useResource } from '../../components/hooks';
import { api } from '../../lib/http';
import { useT } from '../../lib/i18n';

const { Paragraph, Text } = Typography;

const recordKey = row => JSON.stringify([row.surface, row.risk_area]);
const gapKey = row => JSON.stringify([row.kind, row.surface, row.risk_area, row.goal_id]);

export default function CoverageLedger({ id, reloadToken, onView }) {
    const t = useT();
    const resource = useResource(() => api.get(`/deep-assessments/${id}/coverage`), [id, reloadToken]);
    const ledger = resource.data;
    const overview = Boolean(onView);
    const coverage = (key, vars) => t(`deepAssessments.coverage.${key}`, vars);
    const surface = { title: coverage('surface'), dataIndex: 'surface', width: 180, render: value => value || '—' };
    const riskArea = { title: coverage('riskArea'), dataIndex: 'risk_area', width: 140 };
    const records = [surface, riskArea,
        { title: coverage('outcome'), dataIndex: 'outcome', width: 150, render: value => coverage(`outcomes.${value}`) },
        { title: coverage('evidence'), dataIndex: 'evidence', width: 180, render: value => value || '—' },
        { title: coverage('reason'), dataIndex: 'reason', render: value => value || '—' }
    ];
    const gaps = [surface, riskArea,
        { title: coverage('gap'), dataIndex: 'kind', render: value => coverage(`gapKinds.${value}`) },
        { title: coverage('goalRef'), dataIndex: 'goal_id', width: 160, render: value => value || '—' }
    ];

    return (
        <SectionCard
            title={coverage('title')}
            extra={overview ? (
                <Button size="small" onClick={onView}>{coverage('view')}</Button>
            ) : (
                <Button size="small" onClick={() => resource.reload()} loading={resource.loading}>{t('common.refresh')}</Button>
            )}
        >
            <ErrorBanner error={resource.error} onRetry={() => resource.reload()} />
            {resource.loading && !ledger ? <Text type="tertiary">{coverage('loading')}</Text> : null}
            {ledger ? (
                <div className="assessment-tab assessment-coverage">
                    <Paragraph>{coverage('summary', { records: ledger.records.length, gaps: ledger.gaps.length })}</Paragraph>
                    {ledger.records.length > 0 ? <Space wrap>
                        {Object.entries(ledger.counts).map(([outcome, count]) => (
                            <Tag key={outcome}>{coverage(`outcomes.${outcome}`)} · {count}</Tag>
                        ))}
                    </Space> : null}
                    {overview ? (
                        <>
                            {ledger.goals.length > 0 ? (
                                <section>
                                    <Text strong>{coverage('goals')}</Text>
                                    <ul>
                                        {ledger.goals.map(goal => (
                                            <li key={goal.goal_id}>{goal.text} {goal.risk_class ? <Tag size="small">{goal.risk_class}</Tag> : null}</li>
                                        ))}
                                    </ul>
                                </section>
                            ) : null}
                            {ledger.constraints.length > 0 ? (
                                <section>
                                    <Text strong>{coverage('constraints')}</Text>
                                    <ul>
                                        {ledger.constraints.map(constraint => (
                                            <li key={constraint.constraint_id}>
                                                <Tag size="small">{coverage(`constraintKinds.${constraint.kind}`)}</Tag>{' '}
                                                {constraint.text} <Text type="tertiary">({constraint.origin})</Text>
                                            </li>
                                        ))}
                                    </ul>
                                </section>
                            ) : null}
                        </>
                    ) : (
                        <>
                            <DataTable columns={records} dataSource={ledger.records} rowKey={recordKey}
                                pagination={ledger.records.length > 10 ? undefined : false} emptyText={coverage('emptyRecords')} />
                            <section>
                                <Text strong>{coverage('gaps')}</Text>
                                <Paragraph type="tertiary">{coverage('gapHint')}</Paragraph>
                                <DataTable columns={gaps} dataSource={ledger.gaps} rowKey={gapKey}
                                    pagination={ledger.gaps.length > 10 ? undefined : false} emptyText={coverage('emptyGaps')} />
                            </section>
                        </>
                    )}
                </div>
            ) : null}
        </SectionCard>
    );
}
