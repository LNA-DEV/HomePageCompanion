<svelte:options customElement={{ tag: 'basemap-page', shadow: 'none' }} />

<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { connect, formatDate } from '@companion/ui';
	import { PageHeader, DataTable, Loading, StatsCard } from '@companion/ui/components';
	import { RefreshCw, Trash2 } from 'lucide-svelte';

	interface Version {
		id: number;
		version: string;
		size: number;
		schemaVersion: string;
		osmTime: string;
		status: 'copying' | 'verifying' | 'active' | 'retained' | 'deleted' | 'failed';
		error?: string;
		activatedAt?: string;
		deleteAfter?: string;
	}
	interface Progress {
		phase: string;
		partsDone: number;
		partsTotal: number;
		bytesDone: number;
		bytesTotal: number;
		bytesThisRun: number;
		startedAt: string;
	}
	interface Status {
		enabled: boolean;
		store?: string;
		source?: string;
		schedule?: string;
		publicUrl?: string;
		retainDays?: number;
		active?: string;
		job?: { running: boolean; progress?: Progress; lastError?: string; lastRun?: string };
		versions?: Version[];
	}

	const c = connect('basemap');

	let status = $state<Status | null>(null);
	let error = $state('');
	let notice = $state('');
	let loading = $state(true);
	let busy = $state(false);
	let timer: ReturnType<typeof setInterval> | null = null;

	const running = $derived(status?.job?.running ?? false);
	const progress = $derived(status?.job?.progress);
	const activeRow = $derived(status?.versions?.find((v) => v.status === 'active'));
	const percent = $derived(progress && progress.bytesTotal > 0 ? (progress.bytesDone / progress.bytesTotal) * 100 : 0);
	// Rate and ETA from what this run copied; a resumed copy starts with the
	// parts it already had, which must not inflate the rate.
	const rate = $derived.by(() => {
		if (!progress) return 0;
		const secs = (Date.now() - new Date(progress.startedAt).getTime()) / 1000;
		return secs > 0 ? progress.bytesThisRun / secs : 0;
	});
	const eta = $derived(progress && rate > 0 ? (progress.bytesTotal - progress.bytesDone) / rate : null);

	function gb(bytes: number): string {
		return (bytes / 1e9).toFixed(bytes >= 1e10 ? 1 : 2) + ' GB';
	}

	function duration(secs: number): string {
		const h = Math.floor(secs / 3600);
		const m = Math.round((secs % 3600) / 60);
		return h > 0 ? `${h} h ${m} min` : `${m} min`;
	}

	const date = (v: unknown) => formatDate(v as string);

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
		{ key: 'error' as const, label: 'Error', format: (v: unknown) => ((v as string) || '').slice(0, 80) || '—' }
	];

	async function load() {
		try {
			status = await c.api.get<Status>('');
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load the basemap status';
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
	onDestroy(() => {
		if (timer) clearInterval(timer);
	});
</script>

{#snippet statusCell(v: Version)}
	<span class="px-2 py-0.5 rounded text-xs font-medium {statusColors[v.status] ?? ''}">{v.status}</span>
{/snippet}

<PageHeader title="Basemap" description="Self-hosted OpenStreetMap vector tiles (Protomaps build) served to the website" />

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}
{#if notice}
	<div class="mb-4 p-3 bg-green-50 dark:bg-green-900/30 text-green-700 dark:text-green-300 rounded-lg">{notice}</div>
{/if}

{#if loading}
	<Loading message="Loading basemap status..." />
{:else if status && !status.enabled}
	<div class="card text-gray-600 dark:text-gray-300">The basemap is not configured: add a <code>basemap</code> block to companion.yaml.</div>
{:else if status}
	<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
		<StatsCard title="Active build" value={status.active || 'none'} icon="B" color="blue" />
		<StatsCard
			title="OSM data as of"
			value={activeRow?.osmTime ? new Date(activeRow.osmTime).toLocaleDateString() : '—'}
			icon="O"
			color="green"
		/>
		<StatsCard title="Schedule" value={status.schedule || 'manual only'} icon="S" color="purple" />
	</div>

	<div class="card mb-8">
		<div class="flex flex-wrap items-center justify-between gap-4 mb-4">
			<h2 class="text-lg font-semibold">Update</h2>
			<div class="flex gap-2">
				<button
					class="btn-primary inline-flex items-center gap-2"
					disabled={busy || running}
					onclick={() => act(() => c.api.post('update'), 'Update started')}
				>
					<RefreshCw size={16} />
					Update now
				</button>
				<button
					class="btn-secondary inline-flex items-center gap-2"
					disabled={busy}
					onclick={() =>
						act(async () => {
							const r = await c.api.post<{ deleted: number; aborted: number }>('cleanup');
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
				{progress.partsDone} / {progress.partsTotal} parts · {gb(progress.bytesDone)} of {gb(progress.bytesTotal)}
				{#if rate > 0}· {(rate / 1e6).toFixed(1)} MB/s{/if}
				{#if eta !== null}· about {duration(eta)} left{/if}
			</p>
		{:else}
			<p class="text-sm text-gray-600 dark:text-gray-300">
				No update running.
				{#if status.job?.lastRun}Last run {formatDate(status.job.lastRun)}.{/if}
			</p>
			{#if status.job?.lastError}
				<p class="mt-2 text-sm text-red-700 dark:text-red-300">Last run failed: {status.job.lastError}</p>
			{/if}
		{/if}
		<p class="mt-4 text-xs text-gray-500 dark:text-gray-400">
			Source {status.source} · stored in <code>{status.store}</code> · previous build kept {status.retainDays} days after a switch
			· tiles at {status.publicUrl}
		</p>
	</div>

	<div class="card">
		<DataTable {columns} data={status.versions ?? []} emptyMessage="No build copied yet" />
	</div>
{/if}
