<svelte:options customElement={{ tag: 'autoupload-problems', shadow: 'none' }} />

<script lang="ts">
	// Problems: open failures grouped by cause, with what fixes them and a
	// retry per group; the Log tab is every attempt (autoposting-ui.md §4.4).
	import { onMount } from 'svelte';
	import { formatDate, timeAgo } from '@companion/ui';
	import { PageHeader } from '@companion/ui/components';
	import DetailPanel from './DetailPanel.svelte';
	import { au, targetName } from './data';
	import { causeHelp, causeLabel, type Attempt, type Problems } from './types';

	type Cause = 'account' | 'content' | 'transient';
	const causes: Cause[] = ['account', 'content', 'transient'];

	let tab = $state<'open' | 'log'>('open');
	let problems = $state<Problems | null>(null);
	let error = $state('');
	let open = $state<string | null>(null);
	let retrying = $state('');

	let log = $state<{ items: Attempt[]; total: number; page: number; limit: number } | null>(null);
	let status = $state('');
	let cause = $state('');
	let q = $state('');
	let page = $state(1);
	let timer: ReturnType<typeof setTimeout> | null = null;

	async function load() {
		try {
			problems = await au.api.get<Problems>('problems');
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function loadLog() {
		try {
			log = await au.api.get('attempts', { status, cause, q, page, limit: 50 });
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function retry(c: Cause | '') {
		retrying = c || 'all';
		try {
			const r = await au.api.post<{ retrying: number }>('problems/retry', { cause: c });
			au.toast(`Retrying ${r.retrying} ${r.retrying === 1 ? 'post' : 'posts'}`, 'info');
			setTimeout(load, 3000);
		} catch (e) {
			au.toast(e instanceof Error ? e.message : String(e), 'error');
		}
		retrying = '';
	}

	function show(t: 'open' | 'log') {
		tab = t;
		if (t === 'log' && !log) loadLog();
	}

	function typed() {
		if (timer) clearTimeout(timer);
		timer = setTimeout(() => {
			page = 1;
			loadLog();
		}, 300);
	}

	const pages = $derived(log ? Math.max(1, Math.ceil(log.total / log.limit)) : 1);

	onMount(load);
</script>

<PageHeader title="Problems" description="What did not go out, why, and what fixes it">
	{#snippet actions()}
		{#if tab === 'open' && problems && problems.total > 0}
			<button class="btn-primary" disabled={retrying !== ''} onclick={() => retry('')}>Retry all</button>
		{/if}
	{/snippet}
</PageHeader>

<div class="flex gap-2 mb-4" role="tablist">
	<button role="tab" aria-selected={tab === 'open'} class="px-3 py-1.5 rounded-full text-sm border {tab === 'open' ? 'bg-primary-600 border-primary-600 text-white' : 'border-gray-300 dark:border-gray-700'}" onclick={() => show('open')}>
		Open {#if problems}<span class="opacity-75">{problems.total}</span>{/if}
	</button>
	<button role="tab" aria-selected={tab === 'log'} class="px-3 py-1.5 rounded-full text-sm border {tab === 'log' ? 'bg-primary-600 border-primary-600 text-white' : 'border-gray-300 dark:border-gray-700'}" onclick={() => show('log')}>Log</button>
</div>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}

{#if tab === 'open'}
	{#if !problems}
		<p class="text-gray-500">Loading…</p>
	{:else if problems.total === 0}
		<div class="card text-emerald-700 dark:text-emerald-300" data-testid="no-problems">Nothing open: every failure has been resolved.</div>
	{:else}
		<div class="space-y-6">
			{#each causes as c}
				{@const list = problems.groups[c] ?? []}
				{#if list.length}
					<section class="card" data-testid="problem-group-{c}">
						<div class="flex flex-wrap items-start justify-between gap-3 mb-3">
							<div>
								<h2 class="text-lg font-semibold">{causeLabel[c]} <span class="text-gray-500 font-normal">{list.length}</span></h2>
								<p class="text-sm text-gray-600 dark:text-gray-300">{causeHelp[c]}</p>
							</div>
							<button class="btn-secondary btn-sm" disabled={retrying !== ''} onclick={() => retry(c)}>Retry {causeLabel[c].toLowerCase()}</button>
						</div>
						<ul class="divide-y divide-gray-100 dark:divide-gray-800">
							{#each list as p (p.itemId + p.target)}
								<li>
									<button class="w-full flex items-center gap-3 py-2 text-left hover:bg-gray-50 dark:hover:bg-gray-800/50 rounded" onclick={() => (open = p.itemId)}>
										{#if p.imageUrl}<img src={p.imageUrl} alt="" class="w-10 h-10 rounded object-cover shrink-0" loading="lazy" />{:else}<div class="w-10 h-10 rounded bg-gray-100 dark:bg-gray-800 shrink-0"></div>{/if}
										<div class="min-w-0 flex-1">
											<p class="font-medium truncate">{p.title || p.itemId}</p>
											<p class="text-xs text-red-700 dark:text-red-300 truncate" title={p.error}>{targetName(p.target)}: {p.error}</p>
										</div>
										<div class="text-xs text-gray-500 text-right shrink-0">
											<p>{p.attempts} {p.attempts === 1 ? 'attempt' : 'attempts'} · {timeAgo(p.lastAt)}</p>
											{#if p.nextRetry}<p>next retry {timeAgo(p.nextRetry)}</p>{:else if c === 'transient'}<p>no more automatic retries</p>{/if}
										</div>
									</button>
								</li>
							{/each}
						</ul>
					</section>
				{/if}
			{/each}
		</div>
	{/if}
{:else}
	<div class="card mb-4 flex flex-col md:flex-row gap-3">
		<input class="input md:flex-1" type="search" placeholder="Search post or error…" bind:value={q} oninput={typed} aria-label="Search log" />
		<select class="input md:w-40" bind:value={status} onchange={() => { page = 1; loadLog(); }} aria-label="Status">
			<option value="">All</option>
			<option value="success">Succeeded</option>
			<option value="failed">Failed</option>
			<option value="open">Open</option>
		</select>
		<select class="input md:w-40" bind:value={cause} onchange={() => { page = 1; loadLog(); }} aria-label="Cause">
			<option value="">Any cause</option>
			{#each causes as c}<option value={c}>{causeLabel[c]}</option>{/each}
		</select>
	</div>
	{#if log}
		<div class="card p-0 overflow-x-auto">
			<table class="min-w-full text-sm" data-testid="attempt-log">
				<thead>
					<tr class="border-b border-gray-200 dark:border-gray-800">
						<th class="table-header">When</th>
						<th class="table-header">Post</th>
						<th class="table-header">Target</th>
						<th class="table-header">Result</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-gray-100 dark:divide-gray-800">
					{#each log.items as a (a.id)}
						<tr class="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer" onclick={() => (open = a.itemId)}>
							<td class="px-4 py-2 whitespace-nowrap text-gray-500">{formatDate(a.createdAt)}</td>
							<td class="px-4 py-2 max-w-xs truncate">{a.title || a.itemId}</td>
							<td class="px-4 py-2 whitespace-nowrap">{targetName(a.target)}</td>
							<td class="px-4 py-2">
								{#if a.success}
									<span class="text-emerald-700 dark:text-emerald-400">published</span>
								{:else}
									<span class={a.resolvedAt ? 'text-gray-500' : 'text-red-700 dark:text-red-300'}>
										{a.cause ? causeLabel[a.cause] + ': ' : ''}{a.error}{a.resolvedAt ? ' (resolved)' : ''}
									</span>
								{/if}
							</td>
						</tr>
					{:else}
						<tr><td colspan="4" class="px-4 py-8 text-center text-gray-500">No attempts match.</td></tr>
					{/each}
				</tbody>
			</table>
		</div>
		<div class="flex items-center justify-between mt-4 text-sm text-gray-600 dark:text-gray-300">
			<span>{log.total} attempts</span>
			<div class="flex gap-2">
				<button class="btn-secondary btn-sm" disabled={page <= 1} onclick={() => { page--; loadLog(); }}>Previous</button>
				<button class="btn-secondary btn-sm" disabled={page >= pages} onclick={() => { page++; loadLog(); }}>Next</button>
			</div>
		</div>
	{:else}
		<p class="text-gray-500">Loading…</p>
	{/if}
{/if}

{#if open}
	<DetailPanel id={open} onClose={() => (open = null)} onChanged={() => { load(); if (log) loadLog(); }} />
{/if}
