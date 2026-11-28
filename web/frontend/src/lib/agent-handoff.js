/**
 * 跨页面将任务一键交接给 CSAI 自身 Agent（对话工作台）。
 * 使用 sessionStorage 暂存待执行提示词，跳转至 /chat 新会话后自动触发或回填。
 */
export const AGENT_HANDOFF_KEY = 'csai-agent-handoff';
const HANDOFF_TTL_MS = 5 * 60 * 1000;

function readHandoff() {
    try {
        const raw = sessionStorage.getItem(AGENT_HANDOFF_KEY);
        if (!raw) return null;
        const parsed = JSON.parse(raw);
        if (!parsed || typeof parsed.prompt !== 'string' || !parsed.prompt.trim()) {
            return null;
        }
        if (parsed.createdAt && Date.now() - Number(parsed.createdAt) > HANDOFF_TTL_MS) {
            sessionStorage.removeItem(AGENT_HANDOFF_KEY);
            return null;
        }
        return parsed;
    } catch {
        return null;
    }
}

export function queueAgentHandoff({ prompt, autoSend = true }) {
    try {
        sessionStorage.setItem(AGENT_HANDOFF_KEY, JSON.stringify({
            prompt: String(prompt || '').trim(),
            autoSend: Boolean(autoSend),
            createdAt: Date.now()
        }));
    } catch {
        /* storage unavailable */
    }
}

export function peekAgentHandoff() {
    return readHandoff();
}

export function consumeAgentHandoff() {
    const entry = readHandoff();
    try {
        sessionStorage.removeItem(AGENT_HANDOFF_KEY);
    } catch {
        /* storage unavailable */
    }
    return entry;
}
