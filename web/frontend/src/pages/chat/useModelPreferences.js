import { useState } from 'react';
import { useResource } from '../../components/hooks';
import { api } from '../../lib/http';

export const REASONING_MODES = ['default', 'off', 'on', 'auto'];
export const REASONING_EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max'];

export function reasoningSummary(t, mode, effort) {
    if (mode !== 'off' && effort) {
        const labels = { low: '低', medium: '中', high: '高', xhigh: '超高', max: 'ultra' };
        return t(`chat.reasoningEffortLabels.${effort}`, null, labels[effort]);
    }
    const labels = { default: '跟随系统', off: '关闭', on: '开启', auto: '自动' };
    return t(`chat.reasoningMode${mode[0].toUpperCase()}${mode.slice(1)}`, null, labels[mode]);
}

// The legacy composer stored plain strings under these keys.
function useChatPreference(key, fallback) {
    const [value, setValue] = useState(() => {
        try { return localStorage.getItem(key) ?? fallback; } catch { return fallback; }
    });

    return [value, next => {
        setValue(next);
        try { localStorage.setItem(key, next); } catch { /* storage unavailable */ }
    }];
}

function resolveNativeImageMode(channelConfig, imageDefault) {
    if (channelConfig?.image_input === 'enabled') return 'enabled';
    if (channelConfig?.image_input === 'disabled') return 'disabled';
    if (imageDefault === true) return 'enabled';
    if (imageDefault === false) return 'disabled';
    return 'auto';
}

export function useModelPreferences(canReadConfig) {
    const [storedChannel, setChannel] = useChatPreference('cyberstrike-chat-ai-channel', '');
    const [storedMode, setMode] = useChatPreference('cyberstrike-chat-reasoning-mode', 'default');
    const [storedEffort, setEffort] = useChatPreference('cyberstrike-chat-reasoning-effort', '');
    const fullConfig = useResource(({ signal } = {}) => api.get('/config', { include_tools: 0 }, { signal }), [canReadConfig], { skip: !canReadConfig });
    const aiData = fullConfig.data?.ai;
    const config = { ...fullConfig, data: aiData };
    const channels = Object.entries(aiData?.channels || {}).sort(([a], [b]) => a.localeCompare(b));
    const normalizedChannel = storedChannel.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
    const channel = channels.find(([id]) => id === storedChannel)?.[0]
        || channels.find(([id]) => id === normalizedChannel)?.[0] || '';
    const activeId = channel || aiData?.default_channel || '';
    const channelConfig = activeId ? aiData?.channels?.[activeId] : fullConfig.data?.openai;
    const overrideBlocked = channelConfig?.reasoning?.allow_client_reasoning === false;
    const visionModel = String(fullConfig.data?.vision?.model || '').trim();
    const visionReady = Boolean(fullConfig.data?.vision?.enabled && visionModel);
    const nativeImageMode = resolveNativeImageMode(
        channelConfig,
        fullConfig.data?.image_input_defaults?.[activeId]
    );
    const mode = REASONING_MODES.includes(storedMode) ? storedMode : 'default';
    const effort = REASONING_EFFORTS.includes(storedEffort) ? storedEffort : '';
    const reasoning = {
        mode: mode === 'default' ? undefined : mode,
        effort: mode === 'off' ? undefined : effort || undefined
    };

    return {
        channel, channels, setChannel, config, mode, setMode, effort, setEffort, overrideBlocked,
        vision: {
            ready: visionReady,
            model: visionModel,
            nativeMode: nativeImageMode
        },
        waitingForChannel: Boolean(canReadConfig && storedChannel && config.loading),
        request: {
            aiChannelId: canReadConfig ? channel || undefined : undefined,
            reasoning: reasoning.mode || reasoning.effort ? reasoning : undefined
        }
    };
}
