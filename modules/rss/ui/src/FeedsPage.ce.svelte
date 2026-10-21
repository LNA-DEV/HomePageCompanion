<svelte:options customElement={{ tag: 'rss-feeds', shadow: 'none' }} />

<script lang="ts">
	// Feeds: every feed instance with its state; /b/rss/<name> lists one
	// feed's items, searched and paged on the server.
	import { formatDate, stripHtml, timeAgo, connect } from '@companion/ui';
	import { PageHeader } from '@companion/ui/components';

	interface FeedView {
		name: string;
		url: string;
		title: string;
		link: string;
		kind?: string;
		items: number;
		removed: number;
		fetchedAt?: string;
		lastError?: string;
	}
	interface Item {
		id: string;
		kind: string;
		title: string;
		summary?: string;
		link: string;
		categories?: string[];
		published: string;
		image?: { url: string; alt?: string };
		firstSeen?: string;
		removedAt?: string;
	}

	let { path = '' }: { path?: string } = $props();

	const rss = connect('rss');
	let feeds = $state<FeedView[] | null>(null);
	let error = $state('');
	let items = $state<{ items: Item[]; total: number; page: number; limit: number } | null>(null);
	let q = $state('');
	let stateFilter = $state('');
	let kind = $state('');
	let page = $state(1);
	let refreshing = $state(false);
	let timer: ReturnType<typeof setTimeout> | null = null;

	const name = $derived(decodeURIComponent(path));
	const feed = $derived(feeds?.find((f) => f.name === name));
	const pages = $derived(items ? Math.max(1, Math.ceil(items.total / items.limit)) : 1);

	async function loadFeeds() {
		try {
			feeds = await rss.api.get<FeedView[]>('feeds');
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function loadItems() {
		if (!name) return;
		try {
			items = await rss.api.get(`feeds/${encodeURIComponent(name)}/items`, { q, state: stateFilter, kind, page, limit: 30 });
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function refresh() {
		refreshing = true;
		try {
			await rss.api.post(`feeds/${encodeURIComponent(name)}/refresh`);
			rss.toast(`${feed?.title || name} refreshed`, 'success');
			await Promise.all([loadFeeds(), loadItems()]);
		} catch (e) {
			rss.toast(e instanceof Error ? e.message : String(e), 'error');
		}
		refreshing = false;
	}

	function reset() {
		page = 1;
		loadItems();
	}

	function typed() {
		if (timer) clearTimeout(timer);
		timer = setTimeout(reset, 300);
	}

	$effect(() => {
		name;
		items = null;
		loadFeeds();
		loadItems();
	});
</script>

{#if !name}
	<PageHeader title="Feeds" description="The feeds the companion reads" />
	{#if error}<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>{/if}
	{#if !feeds}
		<p class="text-gray-500">Loading…</p>
	{:else if feeds.length === 0}
		<div class="card text-gray-600 dark:text-gray-300">No feed yet: add an <code>rss</code> instance to companion.yaml.</div>
	{:else}
		<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
			{#each feeds as f (f.name)}
				<a href={rss.href(encodeURIComponent(f.name))} class="card hover:shadow-lg transition-shadow block" data-testid="feed-card">
					<div class="flex items-start justify-between gap-2">
						<h3 class="font-semibold text-lg">{f.title || f.name}</h3>
						<span class="w-2.5 h-2.5 mt-2 rounded-full shrink-0 {f.lastError ? 'bg-red-500' : f.fetchedAt ? 'bg-emerald-500' : 'bg-gray-300'}" title={f.lastError || (f.fetchedAt ? 'ok' : 'not fetched yet')}></span>
					</div>
					<p class="text-xs text-gray-500 dark:text-gray-400 truncate mt-1">rss:{f.name} · {f.url}</p>
					<div class="flex flex-wrap items-center gap-2 mt-4 text-sm">
						<span class="bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300 px-2 py-0.5 rounded">{f.items} items</span>
						{#if f.removed}<span class="text-gray-500">{f.removed} removed</span>{/if}
						{#if f.kind}<span class="text-gray-500">{f.kind}</span>{/if}
					</div>
					<p class="text-xs mt-3 {f.lastError ? 'text-red-700 dark:text-red-300' : 'text-gray-500 dark:text-gray-400'} truncate">
						{f.lastError ? f.lastError : f.fetchedAt ? `fetched ${timeAgo(f.fetchedAt)}` : 'not fetched yet'}
					</p>
				</a>
			{/each}
		</div>
	{/if}
{:else}
	<PageHeader title={feed?.title || name} description={feed?.url ?? ''}>
		{#snippet actions()}
			<a href={rss.href()} class="btn-secondary">All feeds</a>
			<button class="btn-primary" disabled={refreshing} onclick={refresh}>{refreshing ? 'Fetching…' : 'Fetch now'}</button>
		{/snippet}
	</PageHeader>
	{#if feed}
		<div class="card mb-6 grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
			<div><p class="text-gray-500 dark:text-gray-400">Items</p><p class="font-semibold mt-1">{feed.items}</p></div>
			<div><p class="text-gray-500 dark:text-gray-400">Removed from the feed</p><p class="font-semibold mt-1">{feed.removed}</p></div>
			<div><p class="text-gray-500 dark:text-gray-400">Last fetched</p><p class="mt-1">{formatDate(feed.fetchedAt)}</p></div>
			<div><p class="text-gray-500 dark:text-gray-400">Website</p>{#if feed.link}<a class="mt-1 block truncate text-primary-700 dark:text-primary-300 hover:underline" href={feed.link} target="_blank" rel="noopener">{feed.link}</a>{:else}<p class="mt-1">–</p>{/if}</div>
			{#if feed.lastError}<p class="col-span-2 md:col-span-4 text-red-700 dark:text-red-300">{feed.lastError}</p>{/if}
		</div>
	{/if}
	{#if error}<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>{/if}
	<div class="card mb-4 flex flex-col md:flex-row gap-3">
		<input class="input md:flex-1" type="search" placeholder="Search title, text, tags…" bind:value={q} oninput={typed} aria-label="Search items" />
		<select class="input md:w-44" bind:value={stateFilter} onchange={reset} aria-label="State">
			<option value="">In the feed</option>
			<option value="removed">Removed</option>
			<option value="all">All</option>
		</select>
		<select class="input md:w-40" bind:value={kind} onchange={reset} aria-label="Kind">
			<option value="">Any kind</option>
			<option value="photo">Photos</option>
			<option value="article">Articles</option>
		</select>
	</div>
	{#if items}
		<div class="space-y-3" data-testid="feed-items">
			{#each items.items as it (it.id)}
				<article class="card flex flex-col sm:flex-row gap-4">
					{#if it.image}<img src={it.image.url} alt={it.image.alt ?? ''} class="w-full sm:w-20 h-40 sm:h-20 object-cover rounded-lg shrink-0" loading="lazy" />{/if}
					<div class="flex-1 min-w-0">
						<h3 class="font-medium truncate">{it.title || it.id}</h3>
						{#if it.summary}<p class="text-sm text-gray-600 dark:text-gray-400 mt-1 line-clamp-2">{stripHtml(it.summary)}</p>{/if}
						<p class="flex flex-wrap gap-x-3 mt-2 text-xs text-gray-500 dark:text-gray-400">
							<span>{formatDate(it.published, false)}</span>
							<span>{it.kind}</span>
							{#if it.categories?.length}<span class="truncate">{it.categories.join(', ')}</span>{/if}
							{#if it.removedAt}<span class="text-amber-700 dark:text-amber-300">removed {formatDate(it.removedAt, false)}</span>{/if}
						</p>
					</div>
					{#if it.link}<a href={it.link} target="_blank" rel="noopener" class="text-sm text-primary-700 dark:text-primary-300 hover:underline self-start shrink-0">View</a>{/if}
				</article>
			{:else}
				<div class="card text-center text-gray-500 py-8">No items match.</div>
			{/each}
		</div>
		<div class="flex items-center justify-between mt-4 text-sm text-gray-600 dark:text-gray-300">
			<span>{items.total} items · page {items.page} of {pages}</span>
			<div class="flex gap-2">
				<button class="btn-secondary btn-sm" disabled={page <= 1} onclick={() => { page--; loadItems(); }}>Previous</button>
				<button class="btn-secondary btn-sm" disabled={page >= pages} onclick={() => { page++; loadItems(); }}>Next</button>
			</div>
		</div>
	{:else}
		<p class="text-gray-500">Loading…</p>
	{/if}
{/if}
