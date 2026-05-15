import { api } from './api';

export interface ClientLogEntry {
	level: 'warn' | 'error';
	source: 'console' | 'window.error' | 'unhandledrejection';
	url?: string;
	time: string;
	message: string;
}

const STORAGE_KEY = 'clientId';
const BUFFER_LIMIT = 500;
const FLUSH_BATCH = 50;
const FLUSH_INTERVAL_MS = 5000;

let installed = false;
let buffer: ClientLogEntry[] = [];
let flushTimer: ReturnType<typeof setTimeout> | null = null;
let cachedClientId: string | null = null;

function safeRandomId(): string {
	if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
		return crypto.randomUUID();
	}
	// Fallback for older environments.
	return 'c-' + Math.random().toString(36).slice(2, 12) + Date.now().toString(36);
}

export function getClientId(): string {
	if (cachedClientId) return cachedClientId;
	if (typeof localStorage === 'undefined') {
		cachedClientId = safeRandomId();
		return cachedClientId;
	}
	let id = localStorage.getItem(STORAGE_KEY);
	if (!id) {
		id = safeRandomId();
		try {
			localStorage.setItem(STORAGE_KEY, id);
		} catch {
			/* private mode etc — keep the in-memory id */
		}
	}
	cachedClientId = id;
	return id;
}

function stringifyArg(arg: unknown): string {
	if (typeof arg === 'string') return arg;
	if (arg instanceof Error) return arg.stack || `${arg.name}: ${arg.message}`;
	try {
		return JSON.stringify(arg);
	} catch {
		return String(arg);
	}
}

function enqueue(entry: ClientLogEntry) {
	if (buffer.length >= BUFFER_LIMIT) {
		// Drop the oldest to keep memory bounded.
		buffer.splice(0, buffer.length - BUFFER_LIMIT + 1);
	}
	buffer.push(entry);
	if (buffer.length >= FLUSH_BATCH) {
		void flush();
	} else if (!flushTimer) {
		flushTimer = setTimeout(() => {
			flushTimer = null;
			void flush();
		}, FLUSH_INTERVAL_MS);
	}
}

async function flush() {
	if (flushTimer) {
		clearTimeout(flushTimer);
		flushTimer = null;
	}
	if (buffer.length === 0) return;
	const batch = buffer.splice(0, buffer.length);
	try {
		await api.sendClientLogs(getClientId(), batch);
	} catch (err) {
		// 401 means we're not authenticated — drop the batch (don't loop).
		if (err instanceof Error && err.message === 'Unauthorized') return;
		// Network or transient — requeue at the front, capped.
		buffer = [...batch, ...buffer].slice(0, BUFFER_LIMIT);
	}
}

function flushBeacon() {
	if (buffer.length === 0) return;
	if (typeof navigator === 'undefined' || typeof navigator.sendBeacon !== 'function') {
		return;
	}
	const apiKey = api.loadApiKey();
	if (!apiKey) return;
	const batch = buffer.splice(0, buffer.length);
	const payload = JSON.stringify({ clientId: getClientId(), entries: batch });
	const url = `/api/admin/client-logs?token=${encodeURIComponent(apiKey)}`;
	try {
		navigator.sendBeacon(url, new Blob([payload], { type: 'application/json' }));
	} catch {
		buffer = [...batch, ...buffer].slice(0, BUFFER_LIMIT);
	}
}

export function installClientLogger() {
	if (installed || typeof window === 'undefined') return;
	installed = true;

	const origWarn = console.warn.bind(console);
	const origError = console.error.bind(console);

	console.warn = (...args: unknown[]) => {
		origWarn(...args);
		enqueue({
			level: 'warn',
			source: 'console',
			url: window.location.href,
			time: new Date().toISOString(),
			message: args.map(stringifyArg).join(' ')
		});
	};

	console.error = (...args: unknown[]) => {
		origError(...args);
		enqueue({
			level: 'error',
			source: 'console',
			url: window.location.href,
			time: new Date().toISOString(),
			message: args.map(stringifyArg).join(' ')
		});
	};

	window.addEventListener('error', (event) => {
		const parts = [event.message];
		if (event.filename) parts.push(`at ${event.filename}:${event.lineno ?? '?'}:${event.colno ?? '?'}`);
		if (event.error && event.error instanceof Error && event.error.stack) {
			parts.push(event.error.stack);
		}
		enqueue({
			level: 'error',
			source: 'window.error',
			url: window.location.href,
			time: new Date().toISOString(),
			message: parts.join(' ')
		});
	});

	window.addEventListener('unhandledrejection', (event) => {
		const reason = event.reason;
		let msg: string;
		if (reason instanceof Error) {
			msg = reason.stack || `${reason.name}: ${reason.message}`;
		} else {
			msg = stringifyArg(reason);
		}
		enqueue({
			level: 'error',
			source: 'unhandledrejection',
			url: window.location.href,
			time: new Date().toISOString(),
			message: msg
		});
	});

	document.addEventListener('visibilitychange', () => {
		if (document.visibilityState === 'hidden') flushBeacon();
	});
	window.addEventListener('pagehide', flushBeacon);
}
