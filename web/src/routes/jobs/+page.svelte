<script lang="ts">
	// Every job of the core and the building blocks: schedule, next and last
	// run, and "run now" with its result.
	import { onMount, onDestroy } from 'svelte';
	import { PageHeader, Loading } from '$lib/components';
	import { core, type JobInfo, type JobRun } from '$lib/shell/core';
	import { toast } from '$lib/shell/toast';
	import { formatDate, timeAgo } from '@companion/ui';

	let jobs = $state<JobInfo[]>([]);
	let loading = $state(true);
	let running = $state<Record<string, boolean>>({});
	let timer: ReturnType<typeof setInterval> | null = null;

	async function load() {
		try {
			jobs = (await core.get<JobInfo[]>('jobs')) ?? [];
		} catch (e) {
			toast(e instanceof Error ? e.message : String(e), 'error');
		}
		loading = false;
	}

	async function run(j: JobInfo) {
		running[j.key] = true;
		try {
			const r = await core.post<JobRun>('jobs/run', { key: j.key, wait: true });
			if (r.ok) toast(`${j.key}: done`, 'success');
			else toast(`${j.key}: ${r.error || 'failed'}`, 'error');
		} catch (e) {
			toast(`${j.key}: ${e instanceof Error ? e.message : e}`, 'error');
		}
		running[j.key] = false;
		await load();
	}

	onMount(() => {
		load();
		timer = setInterval(load, 10000);
	});
	onDestroy(() => {
		if (timer) clearInterval(timer);
	});
</script>

<PageHeader title="Jobs" description="Background work of the core and every building block" />

{#if loading}
	<Loading message="Loading jobs..." />
{:else}
	<div class="card overflow-x-auto p-0">
		<table class="min-w-full divide-y divide-gray-200 dark:divide-gray-800">
			<thead>
				<tr>
					<th class="table-header">Job</th>
					<th class="table-header">Schedule</th>
					<th class="table-header">Next</th>
					<th class="table-header">Last run</th>
					<th class="table-header"></th>
				</tr>
			</thead>
			<tbody class="divide-y divide-gray-100 dark:divide-gray-800">
				{#each jobs as j (j.key)}
					<tr>
						<td class="table-cell font-medium">{j.key}</td>
						<td class="table-cell font-mono text-xs">{j.schedule || 'by hand'}</td>
						<td class="table-cell text-sm">{j.schedule && j.next ? timeAgo(j.next) : '–'}</td>
						<td class="table-cell text-sm">
							{#if j.running}
								<span class="text-primary-700 dark:text-primary-300">running</span>
							{:else if j.lastRun}
								<span class={j.lastRun.ok ? 'text-emerald-700 dark:text-emerald-400' : 'text-red-700 dark:text-red-400'}>
									{j.lastRun.ok ? 'ok' : 'failed'}
								</span>
								<span class="text-gray-500" title={formatDate(j.lastRun.startedAt)}>{timeAgo(j.lastRun.startedAt)}</span>
								{#if j.lastRun.error}<p class="text-xs text-red-600 dark:text-red-400 max-w-md truncate" title={j.lastRun.error}>{j.lastRun.error}</p>{/if}
							{:else}
								<span class="text-gray-500">never</span>
							{/if}
						</td>
						<td class="table-cell text-right">
							<button class="btn-secondary btn-sm" disabled={j.running || running[j.key]} onclick={() => run(j)}>
								{running[j.key] ? 'Running…' : 'Run now'}
							</button>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}
