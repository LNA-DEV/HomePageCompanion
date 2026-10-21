// @companion/ui: what a building block's admin UI uses to talk to the
// companion (docs/concepts/modular-architecture.md §5.7). A module's custom
// element calls connect("<id>") and gets an authenticated client for its own
// API under /api/b/<id>/, the shell's navigation and its notifications. The
// session cookie authenticates every call; no element ever sees a key.
//
// A service's UI runs in a sandboxed iframe of the shell instead, with no
// cookie and no access to the shell. There the same calls go over
// postMessage to the shell, which makes them with a token valid only for
// that service (the bridge, below); the code using connect() is the same.

export class ApiError extends Error {
	constructor(
		public status: number,
		message: string,
		public body?: unknown
	) {
		super(message);
	}
}

export type Query = Record<string, string | number | boolean | undefined | null>;

export interface Api {
	get<T = unknown>(path: string, query?: Query): Promise<T>;
	post<T = unknown>(path: string, body?: unknown): Promise<T>;
	put<T = unknown>(path: string, body?: unknown): Promise<T>;
	del<T = unknown>(path: string): Promise<T>;
	/** upload sends a multipart form (field "file" unless the form names it). */
	upload<T = unknown>(path: string, form: FormData): Promise<T>;
	/** url is the absolute path of an API route, e.g. for <img src>. */
	url(path: string, query?: Query): string;
}

export type ToastKind = 'info' | 'success' | 'error';

/** The shell's side of the bridge; it sets window.companionShell. */
export interface Shell {
	navigate(to: string): void;
	toast(message: string, kind?: ToastKind): void;
	unauthorized(): void;
}

export interface Companion {
	/** The building block's ID, e.g. "autoupload". */
	module: string;
	/** The building block's API, /api/b/<module>/. */
	api: Api;
	/** The core's API, /api/core/. */
	core: Api;
	/** href is the shell URL of one of the module's pages ("" is its first). */
	href(page?: string): string;
	/** navigate goes to a shell URL, or to one of the module's pages. */
	navigate(to: string): void;
	toast(message: string, kind?: ToastKind): void;
}

declare global {
	interface Window {
		companionShell?: Shell;
	}
}

function shell(): Shell {
	if (inFrame()) return frameShell;
	return (
		window.companionShell ?? {
			navigate: (to) => window.location.assign(to),
			toast: (message, kind) => (kind === 'error' ? console.error(message) : console.log(message)),
			unauthorized: () => window.location.assign('/login')
		}
	);
}

function withQuery(path: string, query?: Query): string {
	if (!query) return path;
	const q = new URLSearchParams();
	for (const [k, v] of Object.entries(query)) {
		if (v !== undefined && v !== null && v !== '') q.set(k, String(v));
	}
	const s = q.toString();
	return s ? `${path}${path.includes('?') ? '&' : '?'}${s}` : path;
}

async function call<T>(url: string, init: RequestInit): Promise<T> {
	const resp = await fetch(url, { credentials: 'same-origin', ...init });
	const text = await resp.text();
	let body: unknown = undefined;
	if (text) {
		try {
			body = JSON.parse(text);
		} catch {
			body = text;
		}
	}
	if (resp.status === 401) {
		shell().unauthorized();
		throw new ApiError(401, 'not logged in', body);
	}
	if (!resp.ok) {
		const msg =
			body && typeof body === 'object' && 'error' in body
				? String((body as { error: unknown }).error)
				: `HTTP ${resp.status}`;
		throw new ApiError(resp.status, msg, body);
	}
	return body as T;
}

/** client is an API client under base ("/api/b/trips"). */
export function client(base: string): Api {
	if (inFrame()) return frameClient(base);
	const join = (p: string) => base + (p === '' || p.startsWith('/') || p.startsWith('?') ? p : '/' + p);
	const json = (method: string, path: string, body?: unknown) =>
		call(join(path), {
			method,
			headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
			body: body === undefined ? undefined : JSON.stringify(body)
		});
	return {
		get: <T>(path: string, query?: Query) => call<T>(withQuery(join(path), query), { method: 'GET' }),
		post: <T>(path: string, body?: unknown) => json('POST', path, body) as Promise<T>,
		put: <T>(path: string, body?: unknown) => json('PUT', path, body) as Promise<T>,
		del: <T>(path: string) => call<T>(join(path), { method: 'DELETE' }),
		upload: <T>(path: string, form: FormData) => call<T>(join(path), { method: 'POST', body: form }),
		url: (path: string, query?: Query) => withQuery(join(path), query)
	};
}

/** moduleOf finds the building block an element belongs to: its own
 * data-module, a parent's, or the tag's prefix ("trips-page" → "trips"). */
export function moduleOf(host: Element): string {
	const el = host.closest('[data-module]') as HTMLElement | null;
	if (el?.dataset.module) return el.dataset.module;
	return host.tagName.toLowerCase().split('-')[0];
}

/** connect takes the building block's ID, or an element of it. */
export function connect(module: string | Element): Companion {
	const id = typeof module === 'string' ? module : moduleOf(module);
	return {
		module: id,
		api: client(`/api/b/${id}`),
		core: client('/api/core'),
		href: (page = '') => `/b/${id}${page ? '/' + page.replace(/^\//, '') : ''}`,
		navigate(to: string) {
			shell().navigate(to.startsWith('/') ? to : this.href(to));
		},
		toast: (message, kind = 'info') => shell().toast(message, kind)
	};
}

// ---- the bridge: a service's UI in the shell's sandboxed iframe ----

/** inFrame reports whether the UI runs in the shell's iframe (a service). */
export function inFrame(): boolean {
	return typeof window !== 'undefined' && window.parent !== window;
}

type Reply = { status: number; body: unknown };
const pending = new Map<number, (r: Reply) => void>();
let seq = 0;
let started = false;
let frameToken = '';

function post(msg: Record<string, unknown>): void {
	// The shell's origin is the page's, but this document's is opaque:
	// only '*' reaches it. The shell checks that a message comes from its
	// own iframe.
	window.parent.postMessage({ ...msg, companion: 1 }, '*');
}

function startBridge(): void {
	if (started) return;
	started = true;
	const m = location.pathname.match(/^\/ui\/s\/([^/]+)\//);
	if (m) frameToken = m[1];
	window.addEventListener('message', (e) => {
		if (e.source !== window.parent || !e.data || typeof e.data !== 'object') return;
		const d = e.data as Record<string, unknown>;
		if (d.type === 'api' && typeof d.id === 'number') {
			pending.get(d.id)?.({ status: Number(d.status), body: d.body });
			pending.delete(d.id);
		} else if (d.type === 'init' || d.type === 'theme') {
			document.documentElement.classList.toggle('dark', !!d.dark);
			if (typeof d.token === 'string') frameToken = d.token;
		}
	});
	// Links to the shell's pages navigate the shell, not the iframe.
	document.addEventListener('click', (e) => {
		const a = (e.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null;
		const href = a?.getAttribute('href') ?? '';
		if (a && href.startsWith('/') && !href.startsWith('//') && !href.startsWith('/ui/') && !a.target) {
			e.preventDefault();
			post({ type: 'navigate', to: href });
		}
	});
	const report = () => post({ type: 'resize', height: document.documentElement.scrollHeight });
	new ResizeObserver(report).observe(document.documentElement);
	post({ type: 'ready' });
}

function bridgeCall<T>(method: string, path: string, extra: Record<string, unknown>): Promise<T> {
	startBridge();
	const id = ++seq;
	return new Promise<T>((resolve, reject) => {
		pending.set(id, ({ status, body }) => {
			if (status >= 200 && status < 300) {
				resolve(body as T);
				return;
			}
			const msg =
				body && typeof body === 'object' && 'error' in body
					? String((body as { error: unknown }).error)
					: `HTTP ${status}`;
			reject(new ApiError(status, msg, body));
		});
		post({ type: 'api', id, method, path, ...extra });
	});
}

const frameShell: Shell = {
	navigate: (to) => post({ type: 'navigate', to }),
	toast: (message, kind) => post({ type: 'toast', message, kind }),
	unauthorized: () => {}
};

/** frameClient: the building block's own API through the shell; nothing
 * else is reachable from a service's UI. */
function frameClient(base: string): Api {
	const own = base.match(/^\/api\/b\/([^/]+)/);
	const prefix = own ? base.slice(own[0].length).replace(/^\//, '') : '';
	const join = (p: string) => [prefix, p.replace(/^\//, '')].filter(Boolean).join('/');
	const refuse = <T>(): Promise<T> => Promise.reject(new ApiError(403, `${base} is not reachable from a service's UI`));
	if (!own) {
		return {
			get: refuse,
			post: refuse,
			put: refuse,
			del: refuse,
			upload: refuse,
			url: () => ''
		};
	}
	return {
		get: <T>(path: string, query?: Query) => bridgeCall<T>('GET', withQuery(join(path), query), {}),
		post: <T>(path: string, body?: unknown) => bridgeCall<T>('POST', join(path), { body }),
		put: <T>(path: string, body?: unknown) => bridgeCall<T>('PUT', join(path), { body }),
		del: <T>(path: string) => bridgeCall<T>('DELETE', join(path), {}),
		upload: <T>(path: string, form: FormData) => bridgeCall<T>('POST', join(path), { form: [...form.entries()] }),
		// For <img src>: the token is valid for this service's API only.
		url: (path: string, query?: Query) => {
			startBridge();
			return withQuery(`/api/b/${own[1]}/${join(path)}`, { ...query, uiToken: frameToken });
		}
	};
}

if (inFrame()) startBridge();

/** injectStyles adds a module's CSS to the page once. Module elements live
 * in the light DOM, so the shell's base styles apply to them too. */
export function injectStyles(id: string, css: string): void {
	const key = `companion-styles-${id}`;
	if (document.getElementById(key)) return;
	const style = document.createElement('style');
	style.id = key;
	style.textContent = css;
	document.head.appendChild(style);
}

// ---- small formatting helpers every page needs ----

export function formatDate(value: string | number | Date | null | undefined, withTime = true): string {
	if (!value) return '–';
	const d = new Date(value);
	if (isNaN(d.getTime()) || d.getFullYear() < 1971) return '–';
	return withTime ? d.toLocaleString() : d.toLocaleDateString();
}

export function timeAgo(value: string | number | Date | null | undefined): string {
	if (!value) return '–';
	const d = new Date(value);
	if (isNaN(d.getTime())) return '–';
	const s = Math.round((Date.now() - d.getTime()) / 1000);
	const abs = Math.abs(s);
	const units: [number, string][] = [
		[86400 * 365, 'y'],
		[86400 * 30, 'mo'],
		[86400, 'd'],
		[3600, 'h'],
		[60, 'min']
	];
	for (const [n, u] of units) {
		if (abs >= n) {
			const v = Math.floor(abs / n);
			return s >= 0 ? `${v} ${u} ago` : `in ${v} ${u}`;
		}
	}
	return s >= 0 ? 'just now' : 'in a moment';
}

export { stripHtml, platformColor, safeHref } from '../lib/utils';
