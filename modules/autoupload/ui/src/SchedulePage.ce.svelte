<svelte:options customElement={{ tag: 'autoupload-schedule', shadow: 'none' }} />

<script lang="ts">
	// Schedule: per connection the next run, the post it would choose with a
	// preview, the last run, and "run now" with its real result
	// (autoposting-ui.md §4.3).
	import { onMount } from 'svelte';
	import { formatDate, timeAgo } from '@companion/ui';
	import { PageHeader } from '@companion/ui/components';
	import { au, targetName } from './data';
	import { resultLabel, type Run, type ScheduleRow } from './types';

	let rows = $state<ScheduleRow[]>([]);
	let error = $state('');
	let loading = $state(true);
	let runs = $state<Record<string, Run | 'starting'>>({});
	let showPreview = $state<Record<string, boolean>>({});

	async function load() {
		try {
			rows = (await au.api.get<ScheduleRow[]>('schedule')) ?? [];
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		loading = false;
	}

	async function runNow(r: ScheduleRow) {
		runs[r.connection] = 'starting';
		try {
			let run = await au.api.post<Run & { status?: string }>(`connections/${encodeURIComponent(r.connection)}/run`);
			if (!run.id) {
				// Announcements answer without a run: they go out as they are due.
				au.toast(`${r.connection}: ${run.status ?? 'started'}`, 'info');
				delete runs[r.connection];
				await load();
				return;
			}
			runs[r.connection] = run;
			for (let i = 0; i < 120 && run.result === 'running'; i++) {
				await new Promise((res) => setTimeout(res, 1000));
				run = await au.api.get<Run>(`runs/${run.id}`);
				runs[r.connection] = run;
			}
			await load();
		} catch (e) {
			delete runs[r.connection];
			au.toast(`${r.connection}: ${e instanceof Error ? e.message : e}`, 'error');
		}
	}

	async function resume(r: ScheduleRow) {
		try {
			await au.api.post(`connections/${encodeURIComponent(r.connection)}/resume`);
			au.toast(`${r.connection} resumed`, 'success');
			await load();
		} catch (e) {
			au.toast(e instanceof Error ? e.message : String(e), 'error');
		}
	}

	const resultColor: Record<string, string> = {
		published: 'text-emerald-700 dark:text-emerald-400',
		failed: 'text-red-700 dark:text-red-400',
		paused: 'text-amber-700 dark:text-amber-400',
		interrupted: 'text-amber-700 dark:text-amber-400'
	};

	onMount(load);
</script>

<PageHeader title="Schedule" description="What each connection posts next, and when" />

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}

{#if loading}
	<p class="text-gray-500">Loading…</p>
{:else if rows.length === 0}
	<div class="card text-gray-600 dark:text-gray-300">No connection yet: add an <code>autoupload</code> instance to companion.yaml.</div>
{:else}
	<div class="space-y-4">
		{#each rows as r (r.connection)}
			{@const run = runs[r.connection]}
			<section class="card" data-testid="connection-card">
				<div class="flex flex-wrap items-start justify-between gap-4">
					<div>
						<h2 class="text-lg font-semibold">{r.connection}</h2>
						<p class="text-sm text-gray-600 dark:text-gray-300">
							{r.source} → {targetName(r.target)} · {r.strategy === 'announce' ? 'announcement' : 'rotation'}
							{#if r.cron}· <code class="text-xs">{r.cron}</code>{/if}
						</p>
					</div>
					<div class="flex gap-2">
						{#if r.paused}<button class="btn-secondary btn-sm" onclick={() => resume(r)}>Resume</button>{/if}
						<button class="btn-primary btn-sm" disabled={!!run && (run === 'starting' || run.result === 'running')} onclick={() => runNow(r)}>
							Run now
						</button>
					</div>
				</div>

				{#if r.paused}
					<p class="mt-3 text-sm text-amber-800 dark:text-amber-200 bg-amber-50 dark:bg-amber-900/30 rounded p-2">
						Paused after an account error: {r.pauseReason}. Fix the account, then resume.
					</p>
				{/if}

				<dl class="mt-4 grid grid-cols-1 sm:grid-cols-[9rem_1fr] gap-x-4 gap-y-2 text-sm">
					<dt class="text-gray-500 dark:text-gray-400">Next run</dt>
					<dd>
						{#if r.nextRun}{formatDate(r.nextRun)} ({timeAgo(r.nextRun)}){:else if r.strategy === 'announce'}when a new post is due{:else}by hand only{/if}
						{#if r.strategy === 'announce'}
							<span class="text-gray-500"> · {r.waiting ?? 0} waiting{#if r.window} · window {r.window}{/if}{#if r.delay} · delay {r.delay}{/if}</span>
						{/if}
					</dd>
					<dt class="text-gray-500 dark:text-gray-400">Next post</dt>
					<dd>
						{#if r.next}
							<div class="flex items-center gap-3">
								{#if r.next.imageUrl}<img src={r.next.imageUrl} alt="" class="w-10 h-10 rounded object-cover" />{/if}
								<span>{r.next.title || r.next.id} <span class="text-gray-500">({formatDate(r.next.published, false)})</span></span>
								{#if r.next.preview}
									<button class="text-primary-700 dark:text-primary-300 underline text-xs" onclick={() => (showPreview[r.connection] = !showPreview[r.connection])}>
										{showPreview[r.connection] ? 'hide preview' : 'preview'}
									</button>
								{/if}
							</div>
							{#if showPreview[r.connection] && r.next.preview}
								<div class="mt-2 rounded-lg bg-gray-50 dark:bg-gray-800 p-3 whitespace-pre-wrap" data-testid="schedule-preview">
									{r.next.preview.text}
									{#each r.next.preview.warnings ?? [] as w}<p class="mt-1 text-xs text-amber-700 dark:text-amber-300">⚠ {w}</p>{/each}
								</div>
							{/if}
						{:else if r.error}
							<span class="text-red-700 dark:text-red-300">{r.error}</span>
						{:else}
							<span class="text-gray-500">nothing left to post</span>
						{/if}
					</dd>
					<dt class="text-gray-500 dark:text-gray-400">Last run</dt>
					<dd>
						{#if r.lastRun}
							<span class={resultColor[r.lastRun.result] ?? ''}>{resultLabel[r.lastRun.result] ?? r.lastRun.result}</span>
							{formatDate(r.lastRun.finishedAt ?? r.lastRun.startedAt)}
							{#if r.lastRun.title}· {r.lastRun.title}{/if}
							{#if r.lastRun.postUrl}· <a class="underline" href={r.lastRun.postUrl} target="_blank" rel="noopener">post</a>{/if}
							{#if r.lastRun.error}<p class="text-red-700 dark:text-red-300">{r.lastRun.error}</p>{/if}
						{:else}never{/if}
					</dd>
				</dl>

				{#if run}
					<div class="mt-4 p-3 rounded-lg bg-gray-50 dark:bg-gray-800 text-sm" data-testid="run-result">
						{#if run === 'starting' || run.result === 'running'}
							Running…
						{:else}
							<span class="font-medium {resultColor[run.result] ?? ''}">{resultLabel[run.result] ?? run.result}</span>
							{#if run.title}: {run.title}{/if}
							{#if run.postUrl} – <a class="underline" href={run.postUrl} target="_blank" rel="noopener">{run.postUrl}</a>{/if}
							{#if run.error}<p class="text-red-700 dark:text-red-300">{run.error}</p>{/if}
						{/if}
					</div>
				{/if}
			</section>
		{/each}
	</div>
{/if}
