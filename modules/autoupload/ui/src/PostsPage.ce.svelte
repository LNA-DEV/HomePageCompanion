<svelte:options customElement={{ tag: 'autoupload-posts', shadow: 'none' }} />

<script lang="ts">
	// Posts: one row per post, one column per target, likes on the right
	// (autoposting-ui.md §4.1). Filters, search and paging run on the server.
	import { onMount } from 'svelte';
	import { formatDate } from '@companion/ui';
	import { PageHeader } from '@companion/ui/components';
	import CellView from './CellView.svelte';
	import DetailPanel from './DetailPanel.svelte';
	import { au, targetName } from './data';
	import type { ItemsResponse } from './types';

	const filters = [
		{ id: '', label: 'All', count: 'all' },
		{ id: 'photos', label: 'Photos', count: 'photos' },
		{ id: 'blog', label: 'Blog', count: 'blog' },
		{ id: 'microblog', label: 'Microblog', count: 'microblog' },
		{ id: 'incomplete', label: 'Not everywhere yet', count: '' },
		{ id: 'problems', label: 'With problems', count: 'problems' },
		{ id: 'gone', label: 'No longer in feed', count: 'gone' }
	] as const;

	let data = $state<ItemsResponse | null>(null);
	let error = $state('');
	// A link can preset the view: /b/autoupload?sort=likes&filter=problems.
	const preset = new URLSearchParams(location.search);
	let filter = $state(preset.get('filter') ?? '');
	let target = $state(preset.get('target') ?? '');
	let tag = $state(preset.get('tag') ?? '');
	let q = $state(preset.get('q') ?? '');
	let sort = $state(preset.get('sort') ?? '');
	let page = $state(1);
	let open = $state<string | null>(null);
	let loading = $state(false);
	let timer: ReturnType<typeof setTimeout> | null = null;

	const pages = $derived(data ? Math.max(1, Math.ceil(data.total / data.limit)) : 1);

	async function load() {
		loading = true;
		try {
			data = await au.api.get<ItemsResponse>('items', { filter, target, tag, q, sort, page, limit: 50 });
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		loading = false;
	}

	function reset() {
		page = 1;
		load();
	}

	function typed() {
		if (timer) clearTimeout(timer);
		timer = setTimeout(reset, 300);
	}

	onMount(load);
</script>

<PageHeader title="Posts" description="Every post on every target: where it is out, where not and why, and its likes" />

<div class="flex flex-wrap gap-2 mb-4" role="tablist" aria-label="Filter">
	{#each filters as f}
		<button
			role="tab"
			aria-selected={filter === f.id}
			class="px-3 py-1.5 rounded-full text-sm border transition-colors {filter === f.id
				? 'bg-primary-600 border-primary-600 text-white'
				: 'border-gray-300 dark:border-gray-700 hover:bg-gray-50 dark:hover:bg-gray-800'}"
			onclick={() => {
				filter = f.id;
				reset();
			}}
		>
			{f.label}
			{#if f.count && data?.counts}<span class="opacity-75">{data.counts[f.count]}</span>{/if}
		</button>
	{/each}
</div>

<div class="card mb-4 flex flex-col md:flex-row gap-3 md:items-center">
	<input class="input md:flex-1" type="search" placeholder="Search all posts…" bind:value={q} oninput={typed} aria-label="Search" />
	<select class="input md:w-56" bind:value={target} onchange={reset} aria-label="Target">
		<option value="">All targets</option>
		{#each data?.columns ?? [] as c}<option value={c.id}>{targetName(c.id)}</option>{/each}
	</select>
	<input class="input md:w-40" placeholder="Tag" bind:value={tag} oninput={typed} aria-label="Tag" />
	<select class="input md:w-44" bind:value={sort} onchange={reset} aria-label="Sort">
		<option value="">Newest first</option>
		<option value="likes">Most likes</option>
		<option value="posted">Last posted</option>
	</select>
</div>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}

{#if data}
	<!-- Desktop: the table. The post column stays while the targets scroll. -->
	<div class="card p-0 overflow-x-auto hidden md:block" data-testid="posts-table">
		<table class="min-w-full text-sm">
			<thead>
				<tr class="border-b border-gray-200 dark:border-gray-800">
					<th class="table-header sticky left-0 bg-white dark:bg-gray-900 min-w-[18rem]">Post</th>
					{#each data.columns as c}<th class="table-header whitespace-nowrap">{targetName(c.id)}</th>{/each}
					<th class="table-header text-right">Likes</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-gray-100 dark:divide-gray-800">
				{#each data.items as row (row.id)}
					<tr class="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer" onclick={() => (open = row.id)} data-testid="post-row">
						<td class="px-4 py-2 sticky left-0 bg-white dark:bg-gray-900">
							<div class="flex items-center gap-3">
								{#if row.imageUrl}
									<img src={row.imageUrl} alt="" class="w-12 h-12 object-cover rounded shrink-0" loading="lazy" />
								{:else}
									<div class="w-12 h-12 rounded bg-gray-100 dark:bg-gray-800 shrink-0"></div>
								{/if}
								<div class="min-w-0">
									<p class="font-medium truncate max-w-xs">{row.title || row.id}</p>
									<p class="text-xs text-gray-500 dark:text-gray-400">
										{row.kind === 'photo' ? 'Photo' : row.kind === 'article' ? 'Blog' : 'Microblog'} · {formatDate(row.published, false)}
										{#if !row.inFeed}· <span class="text-amber-700 dark:text-amber-300">no longer in feed</span>{/if}
									</p>
								</div>
							</div>
						</td>
						{#each data.columns as c}<td class="px-4 py-2 whitespace-nowrap"><CellView cell={row.cells[c.id]} /></td>{/each}
						<td class="px-4 py-2 text-right whitespace-nowrap">
							<span class="font-medium">{row.likes || '–'}</span>
							{#if row.websiteLikes}<p class="text-xs text-gray-500">+{row.websiteLikes} website</p>{/if}
						</td>
					</tr>
				{:else}
					<tr><td class="px-4 py-8 text-center text-gray-500" colspan={data.columns.length + 2}>No posts match.</td></tr>
				{/each}
			</tbody>
		</table>
	</div>

	<!-- Phone: tiles with a status dot per target. -->
	<div class="grid grid-cols-2 gap-3 md:hidden" data-testid="posts-tiles">
		{#each data.items as row (row.id)}
			<button class="card p-0 overflow-hidden text-left" onclick={() => (open = row.id)}>
				{#if row.imageUrl}
					<img src={row.imageUrl} alt="" class="w-full aspect-square object-cover" loading="lazy" />
				{:else}
					<div class="p-3 aspect-square flex items-center font-medium">{row.title}</div>
				{/if}
				<div class="p-2">
					<div class="flex gap-1">
						{#each data.columns as c}
							{@const s = row.cells[c.id]?.state}
							<span
								class="w-2.5 h-2.5 rounded-full {s === 'published' ? 'bg-emerald-500' : s === 'failed' ? 'bg-red-500' : s === 'next' ? 'bg-primary-500' : s === 'skipped' ? 'bg-gray-400' : s === 'open' ? 'border border-gray-400' : 'bg-transparent'}"
								title="{targetName(c.id)}: {s}"
							></span>
						{/each}
					</div>
					<p class="text-xs mt-1">{row.likes} likes{row.websiteLikes ? ` +${row.websiteLikes}` : ''}</p>
				</div>
			</button>
		{/each}
	</div>

	<div class="flex items-center justify-between mt-4 text-sm text-gray-600 dark:text-gray-300">
		<span>
			{#if data.total}{(data.page - 1) * data.limit + 1}–{Math.min(data.page * data.limit, data.total)} of {data.total}{:else}0 posts{/if}
			{#if loading}· loading…{/if}
		</span>
		<div class="flex gap-2">
			<button class="btn-secondary btn-sm" disabled={page <= 1} onclick={() => { page--; load(); }}>Previous</button>
			<button class="btn-secondary btn-sm" disabled={page >= pages} onclick={() => { page++; load(); }}>Next</button>
		</div>
	</div>
	<p class="mt-3 text-xs text-gray-500 dark:text-gray-400">
		● published, number = likes · ✕ failed with reason · ◔ scheduled next · ○ skipped · – open · Likes are platform likes; website likes are shown apart.
	</p>
{:else if !error}
	<p class="text-gray-500">Loading posts…</p>
{/if}

{#if open}
	<DetailPanel id={open} onClose={() => (open = null)} onChanged={load} />
{/if}
