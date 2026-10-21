// @companion/ui: what a building block's admin UI uses to talk to the
// companion (docs/concepts/modular-architecture.md §5.7). A module's custom
// element calls connect("<id>") and gets an authenticated client for its own
// API under /api/b/<id>/, the shell's navigation and its notifications. The
// session cookie authenticates every call; no element ever sees a key.

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
