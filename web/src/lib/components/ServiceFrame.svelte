<script lang="ts">
	// A service's page or widget (docs/concepts/modular-architecture.md §5.7):
	// its UI in <iframe sandbox="allow-scripts allow-forms">, an opaque origin
	// without the shell's cookies, storage or DOM. The shell gets a token valid
	// only for this service and answers the frame's messages (@companion/ui's
	// bridge): API calls to /api/b/<service>/ with that token, navigation,
	// notifications, its height. Nothing else crosses.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import type { UIModule } from '$lib/shell/core';
	import { core } from '$lib/shell/core';
	import { toast } from '$lib/shell/toast';
	import { theme } from '$lib/stores/theme';

	interface Props {
		module: UIModule;
		/** the page (its path in the manifest), its element and the rest of
		 * the path, or a widget's element */
		page?: string;
		element?: string;
		path?: string;
		widget?: string;
		minHeight?: number;
	}

	let { module, page, element = '', path = '', widget = '', minHeight = 120 }: Props = $props();

	let frame = $state<HTMLIFrameElement>();
	let src = $state('');
	let error = $state('');
	let height = $state(0);
	let token = '';

	const methods = new Set(['GET', 'POST', 'PUT', 'PATCH', 'DELETE']);

	async function newToken(): Promise<{ token: string; base: string }> {
		const t = await core.post<{ token: string; base: string }>(`services/${encodeURIComponent(module.id)}/ui-token`);
		token = t.token;
		return t;
	}

	function reply(msg: Record<string, unknown>) {
		frame?.contentWindow?.postMessage(msg, '*');
	}

	async function api(m: Record<string, unknown>) {
		const method = String(m.method ?? 'GET').toUpperCase();
		const rel = String(m.path ?? '').replace(/^\/+/, '');
		// Only this service's API: no way up or out.
		if (!methods.has(method) || /^[a-z][a-z0-9+.-]*:/i.test(rel) || rel.split(/[/?#]/).some((s) => s === '..' || s === '.')) {
			reply({ type: 'api', id: m.id, status: 400, body: { error: 'not a path of this service' } });
			return;
		}
		const send = async () => {
			const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
			let body: BodyInit | undefined;
			if (Array.isArray(m.form)) {
				const fd = new FormData();
				for (const [k, v] of m.form as [string, string | Blob][]) fd.append(k, v);
				body = fd;
			} else if (m.body !== undefined) {
				headers['Content-Type'] = 'application/json';
				body = JSON.stringify(m.body);
			}
			return fetch(`/api/b/${encodeURIComponent(module.id)}/${rel}`, { method, headers, body, credentials: 'omit' });
		};
		try {
			let resp = await send();
			if (resp.status === 401) {
				await newToken();
				resp = await send();
			}
			const text = await resp.text();
			let parsed: unknown = text;
			try {
				parsed = text ? JSON.parse(text) : undefined;
			} catch {
				// not JSON
			}
			reply({ type: 'api', id: m.id, status: resp.status, body: parsed });
		} catch (e) {
			reply({ type: 'api', id: m.id, status: 502, body: { error: e instanceof Error ? e.message : String(e) } });
		}
	}

	function onMessage(e: MessageEvent) {
		if (!frame || e.source !== frame.contentWindow || !e.data || typeof e.data !== 'object') return;
		const m = e.data as Record<string, unknown>;
		switch (m.type) {
			case 'ready':
				reply({ type: 'init', module: module.id, page, element, path, widget, dark: $theme === 'dark', token });
				break;
			case 'api':
				void api(m);
				break;
			case 'navigate': {
				const to = String(m.to ?? '');
				if (to.startsWith('/') && !to.startsWith('//')) void goto(to);
				else if (to && !/^[a-z][a-z0-9+.-]*:/i.test(to)) void goto(`/b/${module.id}/${to.replace(/^\.?\//, '')}`);
				break;
			}
			case 'toast':
				toast(String(m.message ?? '').slice(0, 500), m.kind === 'error' || m.kind === 'success' ? m.kind : 'info');
				break;
			case 'resize': {
				const h = Number(m.height);
				if (h > 0) height = Math.min(Math.ceil(h), 20000);
				break;
			}
		}
	}

	$effect(() => {
		reply({ type: 'theme', dark: $theme === 'dark' });
	});

	onMount(() => {
		window.addEventListener('message', onMessage);
		let renew: ReturnType<typeof setInterval> | undefined;
		newToken()
			.then((t) => {
				const q = new URLSearchParams();
				if (page !== undefined) q.set('page', page);
				if (element) q.set('element', element);
				if (path) q.set('path', path);
				if (widget) q.set('widget', widget);
				src = `${t.base}index.html${q.size ? '?' + q : ''}`;
				// The token lasts an hour; the API calls get a fresh one.
				renew = setInterval(() => void newToken().catch(() => {}), 30 * 60 * 1000);
			})
			.catch((e) => (error = `${module.name}: ${e instanceof Error ? e.message : e}`));
		return () => {
			window.removeEventListener('message', onMessage);
			clearInterval(renew);
		};
	});
</script>

{#if error}
	<div class="p-3 rounded-lg bg-amber-50 text-amber-800 dark:bg-amber-900/30 dark:text-amber-200 text-sm">{error}</div>
{:else if src}
	<iframe
		bind:this={frame}
		{src}
		title={module.name}
		sandbox="allow-scripts allow-forms"
		referrerpolicy="no-referrer"
		class="w-full border-0 block bg-transparent"
		style="height: {Math.max(height, minHeight)}px"
		data-testid="service-frame"
	></iframe>
{/if}
