<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api, type LogsResponse } from '$lib/api';
	import { PageHeader, Loading, SearchInput } from '$lib/components';
	import { RefreshCw } from 'lucide-svelte';

	let data = $state<LogsResponse | null>(null);
	let loading = $state(true);
	let error = $state('');
	let file = $state('');
	let tail = $state(200);
	let search = $state('');
	let autoRefresh = $state(false);
	let refreshing = $state(false);
	let timer: ReturnType<typeof setInterval> | null = null;

	async function load() {
		refreshing = true;
		try {
			const resp = await api.getLogs({ file: file || undefined, tail, search: search || undefined });
			data = resp;
			if (!file && resp.file) file = resp.file;
			error = '';
		} catch (e) {
			error = 'Failed to load logs';
		}
		loading = false;
		refreshing = false;
	}

	onMount(load);

	$effect(() => {
		if (timer) {
			clearInterval(timer);
			timer = null;
		}
		if (autoRefresh) {
			timer = setInterval(load, 5000);
		}
	});

	onDestroy(() => {
		if (timer) clearInterval(timer);
	});

	function lineClass(line: string): string {
		const lower = line.toLowerCase();
		if (lower.includes('error') || lower.includes('failed') || lower.includes('panic')) {
			return 'text-red-600 dark:text-red-400';
		}
		if (lower.includes('warn')) {
			return 'text-amber-600 dark:text-amber-400';
		}
		return 'text-gray-800 dark:text-gray-200';
	}

	function labelFor(name: string): string {
		if (name.startsWith('app-')) return 'App · ' + name.slice('app-'.length).replace('.log', '');
		if (name.startsWith('access-'))
			return 'Access · ' + name.slice('access-'.length).replace('.log', '');
		if (name.startsWith('clients/')) {
			const parts = name.slice('clients/'.length).split('/');
			const id = (parts[0] ?? '').slice(0, 8);
			const date = (parts[1] ?? '').replace('.log', '');
			return `Client ${id} · ${date}`;
		}
		return name;
	}

	const grouped = $derived.by(() => {
		const app: string[] = [];
		const access: string[] = [];
		const clients: string[] = [];
		const other: string[] = [];
		for (const f of data?.files ?? []) {
			if (f.startsWith('app-')) app.push(f);
			else if (f.startsWith('access-')) access.push(f);
			else if (f.startsWith('clients/')) clients.push(f);
			else other.push(f);
		}
		return { app, access, clients, other };
	});
</script>

<PageHeader title="Logs" description="Tail of the backend server log file">
	{#snippet actions()}
		<button onclick={load} class="btn-secondary btn-sm" disabled={refreshing}>
			<span class="inline-flex items-center gap-1.5">
				<RefreshCw size={14} class={refreshing ? 'animate-spin' : ''} />
				Refresh
			</span>
		</button>
	{/snippet}
</PageHeader>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}

<div class="card mb-4">
	<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:flex-wrap">
		<div class="flex-1 min-w-[200px]">
			<SearchInput
				bind:value={search}
				onInput={(v) => {
					search = v;
					load();
				}}
				placeholder="Filter lines…"
			/>
		</div>
		<div class="flex items-center gap-2">
			<label for="logFile" class="text-sm font-medium text-gray-700 dark:text-gray-300">File:</label>
			<select
				id="logFile"
				bind:value={file}
				onchange={load}
				class="input w-full sm:w-64"
				disabled={!data || data.files.length === 0}
			>
				{#if grouped.app.length > 0}
					<optgroup label="App">
						{#each grouped.app as f}
							<option value={f}>{labelFor(f)}</option>
						{/each}
					</optgroup>
				{/if}
				{#if grouped.access.length > 0}
					<optgroup label="Access">
						{#each grouped.access as f}
							<option value={f}>{labelFor(f)}</option>
						{/each}
					</optgroup>
				{/if}
				{#if grouped.clients.length > 0}
					<optgroup label="Clients">
						{#each grouped.clients as f}
							<option value={f}>{labelFor(f)}</option>
						{/each}
					</optgroup>
				{/if}
				{#if grouped.other.length > 0}
					<optgroup label="Other">
						{#each grouped.other as f}
							<option value={f}>{f}</option>
						{/each}
					</optgroup>
				{/if}
			</select>
		</div>
		<div class="flex items-center gap-2">
			<label for="logTail" class="text-sm font-medium text-gray-700 dark:text-gray-300">Tail:</label>
			<select
				id="logTail"
				bind:value={tail}
				onchange={load}
				class="input w-full sm:w-28"
			>
				<option value={100}>100</option>
				<option value={200}>200</option>
				<option value={500}>500</option>
				<option value={1000}>1000</option>
			</select>
		</div>
		<label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
			<input type="checkbox" bind:checked={autoRefresh} class="rounded" />
			Auto-refresh
		</label>
	</div>
</div>

{#if loading}
	<Loading message="Loading logs..." />
{:else if !data || data.lines.length === 0}
	<div class="card text-center py-12">
		<p class="text-gray-500 dark:text-gray-400">
			{data && data.files.length === 0 ? 'No log files yet — they appear after the server writes its first log line.' : 'No lines match your filter.'}
		</p>
	</div>
{:else}
	<div class="card p-0 overflow-hidden">
		<pre
			class="overflow-auto text-xs leading-relaxed font-mono p-4 max-h-[70vh] bg-gray-50 dark:bg-gray-900 whitespace-pre"
		>{#each data.lines as line, i}<div class="px-2 -mx-2 py-px {i % 2 === 0 ? 'bg-transparent' : 'bg-white/40 dark:bg-gray-800/40'} {lineClass(line)}">{line || ' '}</div>{/each}</pre>
		<div class="px-4 py-2 text-xs text-gray-500 dark:text-gray-400 border-t border-gray-200 dark:border-gray-800 flex justify-between flex-wrap gap-2">
			<span>{data.lines.length} line{data.lines.length === 1 ? '' : 's'} from {data.file}</span>
			{#if autoRefresh}
				<span>Auto-refreshing every 5s</span>
			{/if}
		</div>
	</div>
{/if}
