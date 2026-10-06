<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api, type BasemapStatus, type BasemapVersion, type RouteStats } from '$lib/api';
	import { PageHeader, DataTable, Loading, StatsCard } from '$lib/components';
	import { RefreshCw, Trash2, Route } from 'lucide-svelte';

	let status = $state<BasemapStatus | null>(null);
	let routes = $state<RouteStats | null>(null);
	let error = $state('');
	let notice = $state('');
	let loading = $state(true);
	let busy = $state(false);
	let timer: ReturnType<typeof setInterval> | null = null;

	const running = $derived(status?.job?.running ?? false);
	const progress = $derived(status?.job?.progress);
	const activeRow = $derived(status?.versions?.find((v) => v.status === 'active'));
	const percent = $derived(
		progress && progress.bytesTotal > 0 ? (progress.bytesDone / progress.bytesTotal) * 100 : 0
	);
	// Rate and ETA from what this run copied; a resumed copy starts with the
	// parts it already had, which must not inflate the rate.
	const rate = $derived.by(() => {
		if (!progress) return 0;
		const secs = (Date.now() - new Date(progress.startedAt).getTime()) / 1000;
		return secs > 0 ? progress.bytesThisRun / secs : 0;
	});
	const eta = $derived(
		progress && rate > 0 ? (progress.bytesTotal - progress.bytesDone) / rate : null
	);

	function gb(bytes: number): string {
		return (bytes / 1e9).toFixed(bytes >= 1e10 ? 1 : 2) + ' GB';
	}

	function duration(secs: number): string {
		const h = Math.floor(secs / 3600);
		const m = Math.round((secs % 3600) / 60);
		return h > 0 ? `${h} h ${m} min` : `${m} min`;
	}

	function date(v: unknown): string {
		return v ? new Date(v as string).toLocaleString() : '—';
	}

	const statusColors: Record<string, string> = {
		active: 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300',
		retained: 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300',
		copying: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/40 dark:text-yellow-300',
		verifying: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/40 dark:text-yellow-300',
		failed: 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300',
		deleted: 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400'
	};

	const columns = [
		{ key: 'version' as const, label: 'Build' },
		{ key: 'status' as const, label: 'Status', render: statusCell },
		{ key: 'schemaVersion' as const, label: 'Schema' },
		{ key: 'osmTime' as const, label: 'OSM data', format: date },
		{ key: 'size' as const, label: 'Size', format: (v: unknown) => gb(v as number) },
		{ key: 'activatedAt' as const, label: 'Activated', format: date },
		{ key: 'deleteAfter' as const, label: 'Deleted after', format: date },
		{
			key: 'error' as const,
			label: 'Error',
			format: (v: unknown) => ((v as string) || '').slice(0, 80) || '—'
		}
	];

	async function load() {
		try {
			[status, routes] = await Promise.all([api.getBasemap(), api.getRouteStats()]);
			error = '';
		} catch {
			error = 'Failed to load the basemap status';
		}
		loading = false;
		// Poll while a copy runs; the page shows its progress live.
		if (status?.job?.running && !timer) {
			timer = setInterval(load, 3000);
		} else if (!status?.job?.running && timer) {
			clearInterval(timer);
			timer = null;
		}
	}

	async function act(fn: () => Promise<unknown>, done: string) {
		busy = true;
		notice = '';
		try {
			await fn();
			if (done) notice = done;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		busy = false;
		await load();
	}

	onMount(load);
	onDestroy(() => timer && clearInterval(timer));
</script>

{#snippet statusCell(v: BasemapVersion)}
	<span class="px-2 py-0.5 rounded text-xs font-medium {statusColors[v.status] ?? ''}">
		{v.status}
	</span>
{/snippet}

<PageHeader
	title="Basemap"
	description="Self-hosted OpenStreetMap vector tiles (Protomaps build) served to the website"
/>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">
		{error}
	</div>
{/if}
{#if notice}
	<div
		class="mb-4 p-3 bg-green-50 dark:bg-green-900/30 text-green-700 dark:text-green-300 rounded-lg"
	>
		{notice}
	</div>
{/if}

{#if loading}
	<Loading message="Loading basemap status..." />
{:else if status && !status.enabled}
	<div class="card text-gray-600 dark:text-gray-300">
		The basemap is disabled: <code>basemap.bucketUrl</code> is not set in the config.
	</div>
{:else if status}
	<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
		<StatsCard title="Active build" value={status.active || 'none'} icon="B" color="blue" />
		<StatsCard
			title="OSM data as of"
			value={activeRow?.osmTime ? new Date(activeRow.osmTime).toLocaleDateString() : '—'}
			icon="O"
			color="green"
		/>
		<StatsCard
			title="Schedule"
			value={status.schedule || 'manual only'}
			icon="S"
			color="purple"
		/>
	</div>

	<div class="card mb-8">
		<div class="flex flex-wrap items-center justify-between gap-4 mb-4">
			<h2 class="text-lg font-semibold">Update</h2>
			<div class="flex gap-2">
				<button
					class="btn-primary inline-flex items-center gap-2"
					disabled={busy || running}
					onclick={() => act(() => api.startBasemapUpdate(), 'Update started')}
				>
					<RefreshCw size={16} />
					Update now
				</button>
				<button
					class="btn-secondary inline-flex items-center gap-2"
					disabled={busy}
					onclick={() =>
						act(async () => {
							const r = await api.basemapCleanup();
							notice = `Cleanup: ${r.deleted} version(s) deleted, ${r.aborted} stale upload(s) aborted`;
						}, '')}
				>
					<Trash2 size={16} />
					Clean up
				</button>
			</div>
		</div>

		{#if running && progress}
			<p class="text-sm text-gray-600 dark:text-gray-300 mb-2">{progress.phase}</p>
			<div class="w-full h-3 rounded bg-gray-200 dark:bg-gray-700 overflow-hidden mb-2">
				<div class="h-full bg-primary-600 transition-all" style="width: {percent}%"></div>
			</div>
			<p class="text-sm text-gray-600 dark:text-gray-300">
				{progress.partsDone} / {progress.partsTotal} parts · {gb(progress.bytesDone)} of
				{gb(progress.bytesTotal)}
				{#if rate > 0}
					· {(rate / 1e6).toFixed(1)} MB/s
				{/if}
				{#if eta !== null}
					· about {duration(eta)} left
				{/if}
			</p>
		{:else}
			<p class="text-sm text-gray-600 dark:text-gray-300">
				No update running.
				{#if status.job?.lastRun}
					Last run {new Date(status.job.lastRun).toLocaleString()}.
				{/if}
			</p>
			{#if status.job?.lastError}
				<p class="mt-2 text-sm text-red-700 dark:text-red-300">
					Last run failed: {status.job.lastError}
				</p>
			{/if}
		{/if}
		<p class="mt-4 text-xs text-gray-500 dark:text-gray-400">
			Source {status.source} · bucket {status.bucket}, prefix <code>{status.prefix}</code> ·
			previous build kept {status.retainDays} days after a switch
		</p>
	</div>

	<div class="card mb-8">
		<DataTable {columns} data={status.versions ?? []} emptyMessage="No build copied yet" />
	</div>

	{#if routes}
		<div class="card">
			<div class="flex flex-wrap items-center justify-between gap-4">
				<div>
					<h2 class="text-lg font-semibold">Trip routes</h2>
					<p class="text-sm text-gray-600 dark:text-gray-300">
						{routes.ok} computed · {routes.failed} failed · {routes.pending} queued
					</p>
				</div>
				<button
					class="btn-secondary inline-flex items-center gap-2"
					disabled={busy}
					onclick={() =>
						act(async () => {
							const r = await api.backfillRoutes();
							notice = `${r.queued} leg(s) queued`;
						}, '')}
				>
					<Route size={16} />
					Route missing and failed legs
				</button>
			</div>
		</div>
	{/if}
{/if}
