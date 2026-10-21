// The shell's view of the core: the API under /api/core and the UI
// manifest every building block contributes to
// (docs/concepts/modular-architecture.md §5.7).
import { client } from '@companion/ui';

export const core = client('/api/core');

export interface NavEntry {
	label: string;
	icon?: string;
	page: string;
	group?: string;
	order?: number;
}

export interface Page {
	path: string;
	element: string;
	title?: string;
}

export interface Widget {
	element: string;
	size: 'S' | 'M' | 'L';
	title?: string;
}

export interface Action {
	label: string;
	endpoint: string;
	confirm?: string;
}

export interface UIModule {
	id: string;
	name: string;
	service?: boolean;
	hasUi: boolean;
	entry?: string;
	instances: string[];
	nav?: NavEntry[];
	pages?: Page[];
	widgets?: Widget[];
	settings?: string;
	actions?: Action[];
}

export interface Manifest {
	modules: UIModule[];
	version: string;
}

export interface Field {
	name: string;
	type: 'string' | 'number' | 'boolean' | 'duration' | 'list' | 'object' | 'instance' | 'instances';
	secret?: boolean;
	slot?: string;
	help?: string;
}

export interface InstanceView {
	id: string;
	block: string;
	name: string;
	service?: boolean;
	running: boolean;
	error?: string;
	startedAt?: string;
	provides: string[] | null;
	slots: Record<string, string[]> | null;
	config?: Record<string, unknown>;
	schema?: Field[];
	// Set for an instance whose account is connected through OAuth.
	oauth?: OAuthState;
}

export interface OAuthState {
	connected: boolean;
	account?: string;
	since?: string;
	expiresAt?: string;
	note?: string;
}

export interface JobRun {
	id: number;
	job: string;
	trigger: string;
	startedAt: string;
	finishedAt?: string;
	ok: boolean;
	error?: string;
}

export interface JobInfo {
	key: string;
	owner: string;
	name: string;
	schedule?: string;
	next?: string;
	running: boolean;
	lastRun?: JobRun;
}

export interface Overview {
	version: string;
	uptime: string;
	startedAt: string;
	instances: number;
	failed: InstanceView[];
	events: Record<string, number>;
	jobsFailing: JobInfo[];
	goVersion: string;
}

export interface MetricsPoint {
	time: string;
	requests: number;
	errors: number;
	jobsOk: number;
	jobsFailed: number;
}

/** pageFor finds a module's page for a sub path: an exact path, or one
 * ending in "*" that takes the rest. */
export function pageFor(m: UIModule, path: string): { page: Page; rest: string } | null {
	const pages = m.pages ?? [];
	const exact = pages.find((p) => p.path === path);
	if (exact) return { page: exact, rest: '' };
	for (const p of pages) {
		if (!p.path.endsWith('*')) continue;
		const prefix = p.path.slice(0, -1).replace(/\/$/, '');
		if (prefix === '' || path === prefix || path.startsWith(prefix + '/')) {
			return { page: p, rest: prefix === '' ? path : path.slice(prefix.length + 1) };
		}
	}
	return null;
}
