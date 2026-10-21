<script lang="ts">
	import { onMount } from 'svelte';
	import { PageHeader, ModuleElement, Loading } from '$lib/components';
	import { core, type Overview, type MetricsPoint, type UIModule, type Widget } from '$lib/shell/core';
	import { manifest } from '$lib/shell/session';
	import { toast } from '$lib/shell/toast';
	import { formatDate } from '@companion/ui';

	let overview = $state<Overview | null>(null);
	let history = $state<MetricsPoint[]>([]);
	let loading = $state(true);

	const widgets = $derived.by(() => {
		const out: { module: UIModule; widget: Widget }[] = [];
		for (const m of $manifest?.modules ?? []) {
			for (const w of m.widgets ?? []) out.push({ module: m, widget: w });
		}
		return out;
	});

	const span = { S: 'md:col-span-1', M: 'md:col-span-2', L: 'md:col-span-4' };

	// The last day as 48 half-hour bars: requests, errors, failed jobs.
	const bars = $derived.by(() => {
		const buckets: { requests: number; errors: number; jobsFailed: number }[] = [];
		const now = Date.now();
		for (let i = 0; i < 48; i++) buckets.push({ requests: 0, errors: 0, jobsFailed: 0 });
		for (const p of history) {
			const age = (now - new Date(p.time).getTime()) / 60000;
			const i = 47 - Math.floor(age / 30);
			if (i < 0 || i > 47) continue;
			buckets[i].requests += p.requests;
			buckets[i].errors += p.errors;
			buckets[i].jobsFailed += p.jobsFailed;
		}
		const max = Math.max(1, ...buckets.map((b) => b.requests));
		return { buckets, max };
	});
	const totals = $derived(
		history.reduce(
			(t, p) => ({ requests: t.requests + p.requests, errors: t.errors + p.errors, jobsOk: t.jobsOk + p.jobsOk, jobsFailed: t.jobsFailed + p.jobsFailed }),
			{ requests: 0, errors: 0, jobsOk: 0, jobsFailed: 0 }
		)
	);

	async function load() {
		try {
			const [o, h] = await Promise.all([core.get<Overview>('overview'), core.get<MetricsPoint[] | null>('metrics/history')]);
			overview = o;
			history = h ?? [];
		} catch (e) {
			toast(e instanceof Error ? e.message : String(e), 'error');
		}
		loading = false;
	}

	async function retryEvents() {
		try {
			const r = await core.post<{ requeued: number }>('events/retry');
			toast(`${r.requeued} event delivery(ies) queued again`, 'success');
			await load();
		} catch (e) {
			toast(e instanceof Error ? e.message : String(e), 'error');
		}
	}

	onMount(load);
</script>

<PageHeader title="Dashboard" description="The companion and its building blocks at a glance" />

{#if loading}
	<Loading message="Loading..." />
{:else}
	<div class="grid grid-cols-1 md:grid-cols-4 gap-6 mb-6">
		<div class="card md:col-span-2" data-testid="core-card">
			<h2 class="text-sm font-semibold uppercase tracking-wider text-gray-500 dark:text-gray-400 mb-3">Companion</h2>
			{#if overview}
				<dl class="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
					<dt class="text-gray-500 dark:text-gray-400">Version</dt>
					<dd>{overview.version} ({overview.goVersion})</dd>
					<dt class="text-gray-500 dark:text-gray-400">Running since</dt>
					<dd>{formatDate(overview.startedAt)} · {overview.uptime}</dd>
					<dt class="text-gray-500 dark:text-gray-400">Instances</dt>
					<dd>
						{overview.instances}
						{#if overview.failed.length}
							· <a href="/settings" class="text-red-600 dark:text-red-400 underline">{overview.failed.length} not running</a>
						{/if}
					</dd>
					<dt class="text-gray-500 dark:text-gray-400">Events</dt>
					<dd>
						{overview.events.done ?? 0} delivered · {overview.events.pending ?? 0} pending
						{#if overview.events.failed}
							· <button class="text-red-600 dark:text-red-400 underline" onclick={retryEvents}>{overview.events.failed} failed, retry</button>
						{/if}
					</dd>
				</dl>
				{#if overview.jobsFailing.length}
					<div class="mt-4 text-sm">
						<p class="font-medium text-red-700 dark:text-red-300 mb-1">Jobs whose last run failed</p>
						<ul class="space-y-1">
							{#each overview.jobsFailing as j}
								<li><a class="underline" href="/jobs">{j.key}</a>: {j.lastRun?.error}</li>
							{/each}
						</ul>
					</div>
				{/if}
			{/if}
		</div>
		<div class="card md:col-span-2" data-testid="metrics-card">
			<h2 class="text-sm font-semibold uppercase tracking-wider text-gray-500 dark:text-gray-400 mb-3">Last 24 hours</h2>
			<p class="text-sm mb-3">
				{totals.requests} requests · <span class={totals.errors ? 'text-red-600 dark:text-red-400' : ''}>{totals.errors} errors</span> ·
				{totals.jobsOk} jobs ok · <span class={totals.jobsFailed ? 'text-red-600 dark:text-red-400' : ''}>{totals.jobsFailed} failed</span>
			</p>
			<div class="flex items-end gap-px h-20" aria-label="Requests per half hour">
				{#each bars.buckets as b}
					<div class="flex-1 flex flex-col justify-end h-full" title="{b.requests} requests, {b.errors} errors">
						<div class="bg-red-500" style="height: {(b.errors / bars.max) * 100}%"></div>
						<div class="bg-primary-500/70" style="height: {((b.requests - b.errors) / bars.max) * 100}%"></div>
					</div>
				{/each}
			</div>
			<p class="mt-2 text-xs text-gray-500 dark:text-gray-400">All values in Prometheus format at <code>/metrics</code>.</p>
		</div>
	</div>

	{#if widgets.length}
		<div class="grid grid-cols-1 md:grid-cols-4 gap-6">
			{#each widgets as { module, widget } (module.id + widget.element)}
				<section class="card {span[widget.size] ?? 'md:col-span-2'}">
					{#if widget.title}
						<h2 class="text-sm font-semibold uppercase tracking-wider text-gray-500 dark:text-gray-400 mb-3">{widget.title}</h2>
					{/if}
					<ModuleElement {module} tag={widget.element} />
				</section>
			{/each}
		</div>
	{/if}
{/if}
