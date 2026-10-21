<script lang="ts">
	// One instance: status, connections, the building block's actions and its
	// settings, either the block's own element or a form generated from the
	// config schema. Saving writes companion.yaml and swaps the instance.
	import { page } from '$app/stores';
	import { PageHeader, Loading, ModuleElement } from '$lib/components';
	import { core, type Field, type InstanceView } from '$lib/shell/core';
	import { manifest } from '$lib/shell/session';
	import { toast } from '$lib/shell/toast';
	import { client, formatDate } from '@companion/ui';

	const id = $derived(decodeURIComponent($page.params.id ?? ''));
	let view = $state<InstanceView | null>(null);
	let all = $state<InstanceView[]>([]);
	let values = $state<Record<string, unknown>>({});
	let error = $state('');
	let saving = $state(false);
	let busy = $state('');

	const mod = $derived($manifest?.modules.find((m) => m.id === view?.block));

	async function load(target: string) {
		error = '';
		try {
			[view, all] = await Promise.all([
				core.get<InstanceView>(`instances/${encodeURIComponent(target)}`),
				core.get<InstanceView[]>('instances')
			]);
			const v: Record<string, unknown> = { ...(view.config ?? {}) };
			for (const [slot, ids] of Object.entries(view.slots ?? {})) v[slot] = ids;
			values = v;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		if (id) load(id);
	});

	// Back from the platform: /settings/<id>?oauth=connected&account=… or
	// ?oauth=error&message=… (core/oauth.go).
	$effect(() => {
		const q = $page.url.searchParams;
		const result = q.get('oauth');
		if (!result) return;
		if (result === 'connected') toast(`Account connected: ${q.get('account') ?? ''}`, 'success');
		else toast(q.get('message') ?? 'The account was not connected', 'error');
		history.replaceState(history.state, '', $page.url.pathname);
	});

	async function connect() {
		if (!view) return;
		busy = 'oauth';
		try {
			const r = await client(`/api/b/${view.block}`).post<{ url: string }>('oauth/start', { instance: view.name });
			window.location.assign(r.url);
		} catch (err) {
			toast(`Connect: ${err instanceof Error ? err.message : err}`, 'error');
			busy = '';
		}
	}

	async function disconnect() {
		if (!view || !confirm(`Disconnect the account of ${view.id}? Until you connect it again, the token in companion.yaml is used, if there is one.`)) return;
		busy = 'oauth';
		try {
			await client(`/api/b/${view.block}`).post('oauth/disconnect', { instance: view.name });
			toast('Account disconnected', 'success');
			await load(view.id);
		} catch (err) {
			toast(`Disconnect: ${err instanceof Error ? err.message : err}`, 'error');
		}
		busy = '';
	}

	function providers(f: Field): InstanceView[] {
		return all.filter((i) => i.id !== view?.id && (i.provides ?? []).includes(f.slot ?? ''));
	}

	function text(v: unknown): string {
		if (v === undefined || v === null) return '';
		if (typeof v === 'object') return JSON.stringify(v);
		return String(v);
	}

	function setList(name: string, raw: string) {
		values[name] = raw
			.split(/[\n,]/)
			.map((s) => s.trim())
			.filter(Boolean);
	}

	function setObject(name: string, raw: string) {
		try {
			values[name] = raw.trim() ? JSON.parse(raw) : undefined;
		} catch {
			// kept as typed; the save reports it
			values[name] = raw;
		}
	}

	function single(name: string): string {
		const v = values[name];
		return Array.isArray(v) ? (v[0] ?? '') : text(v);
	}

	async function save(e: Event) {
		e.preventDefault();
		if (!view) return;
		saving = true;
		const out: Record<string, unknown> = {};
		for (const f of view.schema ?? []) {
			const v = values[f.name];
			if (v === undefined || v === '') continue;
			if (f.type === 'instance') out[f.name] = Array.isArray(v) ? v[0] : v;
			else if (f.type === 'number') out[f.name] = Number(v);
			else out[f.name] = v;
		}
		try {
			await core.put(`instances/${encodeURIComponent(view.id)}/config`, { values: out });
			toast('Saved; the instance was rebuilt', 'success');
			await load(view.id);
		} catch (err) {
			toast(err instanceof Error ? err.message : String(err), 'error');
		}
		saving = false;
	}

	async function action(endpoint: string, label: string, confirmText?: string) {
		if (!view) return;
		if (confirmText && !confirm(confirmText)) return;
		busy = endpoint;
		try {
			const r = await client(`/api/b/${view.block}`).post<unknown>(endpoint, { instance: view.name });
			const msg = r && typeof r === 'object' && 'status' in r ? String((r as { status: unknown }).status) : 'done';
			toast(`${label}: ${msg}`, 'success');
			await load(view.id);
		} catch (err) {
			toast(`${label}: ${err instanceof Error ? err.message : err}`, 'error');
		}
		busy = '';
	}
</script>

{#if error}
	<div class="card text-red-700 dark:text-red-300">{error}</div>
{:else if !view}
	<Loading message="Loading..." />
{:else}
	<PageHeader title={view.id} description={mod?.name ?? view.block}>
		{#snippet actions()}
			{#each mod?.actions ?? [] as a}
				<button class="btn-secondary btn-sm" disabled={busy !== ''} onclick={() => action(a.endpoint, a.label, a.confirm)}>{a.label}</button>
			{/each}
		{/snippet}
	</PageHeader>

	<div class="card mb-6">
		<dl class="grid grid-cols-1 sm:grid-cols-[10rem_1fr] gap-x-4 gap-y-2 text-sm">
			<dt class="text-gray-500 dark:text-gray-400">Status</dt>
			<dd>
				{#if view.running}<span class="text-emerald-700 dark:text-emerald-400">running</span> since {formatDate(view.startedAt)}
				{:else}<span class="text-red-700 dark:text-red-400">not running</span>{/if}
				{#if view.error}<p class="text-red-700 dark:text-red-300">{view.error}</p>{/if}
			</dd>
			<dt class="text-gray-500 dark:text-gray-400">Provides</dt>
			<dd>{view.provides?.join(', ') || '–'}</dd>
			{#each Object.entries(view.slots ?? {}) as [slot, ids]}
				<dt class="text-gray-500 dark:text-gray-400">{slot}</dt>
				<dd>
					{#each ids as sid, i}{i ? ', ' : ''}<a class="underline" href="/settings/{encodeURIComponent(sid)}">{sid}</a>{/each}
				</dd>
			{/each}
		</dl>
	</div>

	{#if view.oauth}
		{@const o = view.oauth}
		<section class="card mb-6 flex flex-wrap items-start justify-between gap-4" data-testid="oauth-card">
			<div class="text-sm">
				<h2 class="text-lg font-semibold mb-1">Account</h2>
				{#if o.connected}
					<p>Connected as <span class="font-medium">{o.account}</span>{#if o.since}{' '}since {formatDate(o.since, false)}{/if}.</p>
					{#if o.expiresAt}<p class="text-gray-500 dark:text-gray-400">The token is valid until {formatDate(o.expiresAt)}; it is refreshed by itself.</p>{/if}
				{:else}
					<p class="text-gray-600 dark:text-gray-300">Not connected. Connect the account here, or put a token into the settings below.</p>
				{/if}
				{#if o.note}<p class="mt-1 text-amber-700 dark:text-amber-300">{o.note}</p>{/if}
				{#if o.redirectUri}
					<p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
						Callback URL, for apps that need it registered (Instagram, Threads): <code class="select-all break-all">{o.redirectUri}</code>
					</p>
				{:else}
					<p class="mt-2 text-xs text-amber-700 dark:text-amber-300">Set <code>core.publicUrl</code> in companion.yaml to connect accounts: the platform sends you back there.</p>
				{/if}
			</div>
			<div class="flex gap-2">
				<button class="btn-primary btn-sm" disabled={busy !== ''} onclick={connect}>{o.connected ? 'Reconnect' : 'Connect account'}</button>
				{#if o.connected}<button class="btn-secondary btn-sm" disabled={busy !== ''} onclick={disconnect}>Disconnect</button>{/if}
			</div>
		</section>
	{/if}

	{#if mod?.settings && mod.hasUi}
		<div class="card">
			<ModuleElement module={mod} tag={mod.settings} attrs={{ instance: view.name }} />
		</div>
	{:else if view.service}
		<div class="card text-sm text-gray-600 dark:text-gray-300">A service's settings are edited in companion.yaml.</div>
	{:else}
		<form class="card space-y-4" onsubmit={save} data-testid="settings-form">
			<h2 class="text-lg font-semibold">Settings</h2>
			{#each view.schema ?? [] as f}
				<div>
					<label class="label" for="f-{f.name}">
						{f.name}
						{#if f.secret}<span class="text-xs text-gray-500">(secret: shown masked, kept unless changed)</span>{/if}
						{#if f.slot}<span class="text-xs text-gray-500">({f.slot})</span>{/if}
					</label>
					{#if f.type === 'boolean'}
						<input id="f-{f.name}" type="checkbox" checked={values[f.name] === true} onchange={(e) => (values[f.name] = e.currentTarget.checked)} />
					{:else if f.type === 'instance'}
						<select id="f-{f.name}" class="input" value={single(f.name)} onchange={(e) => (values[f.name] = e.currentTarget.value)}>
							<option value="">– not connected –</option>
							{#each providers(f) as p}<option value={p.id}>{p.id}</option>{/each}
						</select>
					{:else if f.type === 'instances'}
						<div class="flex flex-wrap gap-3">
							{#each providers(f) as p}
								{@const list = (values[f.name] as string[] | undefined) ?? []}
								<label class="inline-flex items-center gap-2 text-sm">
									<input
										type="checkbox"
										checked={list.includes(p.id)}
										onchange={(e) => (values[f.name] = e.currentTarget.checked ? [...list, p.id] : list.filter((x) => x !== p.id))}
									/>
									{p.id}
								</label>
							{:else}
								<span class="text-sm text-gray-500">No instance provides {f.slot}.</span>
							{/each}
						</div>
					{:else if f.type === 'list'}
						<textarea id="f-{f.name}" class="input font-mono text-sm" rows="3" value={((values[f.name] as unknown[]) ?? []).join('\n')} oninput={(e) => setList(f.name, e.currentTarget.value)}></textarea>
					{:else if f.type === 'object'}
						<textarea id="f-{f.name}" class="input font-mono text-sm" rows="4" value={text(values[f.name])} oninput={(e) => setObject(f.name, e.currentTarget.value)}></textarea>
					{:else}
						<input
							id="f-{f.name}"
							class="input"
							type={f.secret ? 'password' : f.type === 'number' ? 'number' : 'text'}
							value={text(values[f.name])}
							placeholder={f.type === 'duration' ? 'e.g. 10m, 7d' : ''}
							oninput={(e) => (values[f.name] = e.currentTarget.value)}
						/>
					{/if}
					{#if f.help}<p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{f.help}</p>{/if}
				</div>
			{/each}
			<button class="btn-primary" type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
		</form>
	{/if}
{/if}
