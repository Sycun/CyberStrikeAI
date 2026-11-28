/** 通用异步数据装载：统一 loading / error / reload 语义，支持卸载自动取消与静默轮询防重入。 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { runWithAbortSignal } from '../lib/http';

export function useResource(loader, deps = [], { initial = null, skip = false } = {}) {
    const [data, setData] = useState(initial);
    const [loading, setLoading] = useState(!skip);
    const [error, setError] = useState(null);
    const seq = useRef(0);
    const abortRef = useRef(null);
    const inFlightRef = useRef(false);

    const reload = useCallback(async (options = {}) => {
        if (skip) return null;
        // 静默轮询若上一次请求仍在途中则跳过，避免高延迟链路下堆叠请求占满 HTTP/1.1 连接槽。
        if (options.silent && !options.force && inFlightRef.current) return null;

        abortRef.current?.abort();
        const controller = new AbortController();
        abortRef.current = controller;
        inFlightRef.current = true;

        const current = ++seq.current;
        if (!options.silent) setLoading(true);
        setError(null);
        try {
            const result = await runWithAbortSignal(controller.signal, () => loader({ signal: controller.signal }));
            if (!controller.signal.aborted && current === seq.current) setData(result);
            return result;
        } catch (err) {
            if (controller.signal.aborted) return null;
            if (current === seq.current) setError(err);
            return null;
        } finally {
            if (abortRef.current === controller) {
                abortRef.current = null;
                inFlightRef.current = false;
            }
            if (!controller.signal.aborted && current === seq.current) setLoading(false);
        }
    }, deps); // eslint-disable-line react-hooks/exhaustive-deps

    useEffect(() => {
        if (skip) {
            abortRef.current?.abort();
            abortRef.current = null;
            inFlightRef.current = false;
            setLoading(false);
            return undefined;
        }
        reload();
        return () => {
            seq.current += 1;
            abortRef.current?.abort();
            abortRef.current = null;
            inFlightRef.current = false;
        };
    }, [reload, skip]);

    return { data, setData, loading, error, reload };
}

export function useDebounced(value, delay = 300) {
    const [debounced, setDebounced] = useState(value);
    useEffect(() => {
        const timer = setTimeout(() => setDebounced(value), delay);
        return () => clearTimeout(timer);
    }, [value, delay]);
    return debounced;
}

export function useInterval(callback, delay) {
    const saved = useRef(callback);
    useEffect(() => { saved.current = callback; }, [callback]);
    useEffect(() => {
        if (!delay) return undefined;
        const timer = setInterval(() => saved.current(), delay);
        return () => clearInterval(timer);
    }, [delay]);
}

export function useLocalStorage(key, initial) {
    const [value, setValue] = useState(() => {
        try {
            const raw = localStorage.getItem(key);
            return raw === null ? initial : JSON.parse(raw);
        } catch (error) {
            return initial;
        }
    });
    useEffect(() => {
        try { localStorage.setItem(key, JSON.stringify(value)); } catch (error) { /* ignore */ }
    }, [key, value]);
    return [value, setValue];
}
