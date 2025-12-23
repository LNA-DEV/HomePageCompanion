<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api, type Feed, type FeedItem, type PaginatedFeedItems } from '$lib/api';
	import { PageHeader, Loading } from '$lib/components';

	let feed = $state<Feed | null>(null);
	let items = $state<FeedItem[]>([]);
	let total = $state(0);
	let currentPage = $state(1);
	let limit = 20;
	let error = $state('');
	let loading = $state(true);

	const feedId = $derived(Number($page.params.id));

	async function loadData() {
		loading = true;
		try {
			const [feedData, itemsData] = await Promise.all([
				api.getFeed(feedId),
				api.getFeedItems(feedId, currentPage, limit)
			]);
			feed = feedData;
			items = itemsData.items || [];
			total = itemsData.total;
		} catch (e) {
			error = 'Failed to load feed data';
		}
		loading = false;
	}

	onMount(() => {
		loadData();
	});

	function prevPage() {
		if (currentPage > 1) {
			currentPage--;
			loadData();
		}
	}

	function nextPage() {
		if (currentPage * limit < total) {
			currentPage++;
			loadData();
		}
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-US', {
			year: 'numeric',
			month: 'short',
			day: 'numeric'
		});
	}
</script>

{#if loading && !feed}
	<Loading message="Loading feed..." />
{:else if error}
	<div class="card">
		<p class="text-red-600">{error}</p>
		<a href="/feeds" class="text-primary-600 hover:underline mt-2 inline-block">Back to feeds</a>
	</div>
{:else if feed}
	<PageHeader title={feed.Title || feed.FeedName} description={feed.Description}>
		{#snippet actions()}
			<a href="/feeds" class="btn-secondary">Back to Feeds</a>
		{/snippet}
	</PageHeader>

	<div class="card mb-6">
		<div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
			{#if feed.FeedURL}
				<div>
					<span class="text-gray-500">Feed URL</span>
					<p class="truncate font-mono text-xs mt-1">{feed.FeedURL}</p>
				</div>
			{/if}
			{#if feed.Language}
				<div>
					<span class="text-gray-500">Language</span>
					<p class="mt-1">{feed.Language}</p>
				</div>
			{/if}
			{#if feed.Generator}
				<div>
					<span class="text-gray-500">Generator</span>
					<p class="mt-1">{feed.Generator}</p>
				</div>
			{/if}
			<div>
				<span class="text-gray-500">Total Items</span>
				<p class="mt-1 font-semibold">{total}</p>
			</div>
		</div>
	</div>

	<div class="card">
		<div class="flex items-center justify-between mb-4">
			<h2 class="text-lg font-semibold">Feed Items</h2>
			<div class="flex items-center gap-2 text-sm text-gray-500">
				<span>
					Page {currentPage} of {Math.ceil(total / limit) || 1}
				</span>
			</div>
		</div>

		{#if items.length === 0}
			<p class="text-gray-500 py-8 text-center">No items in this feed</p>
		{:else}
			<div class="space-y-4">
				{#each items as item}
					<div class="border rounded-lg p-4 hover:bg-gray-50 transition-colors">
						<div class="flex gap-4">
							{#if item.ImageUrl}
								<img
									src={item.ImageUrl}
									alt={item.Title}
									class="w-20 h-20 object-cover rounded-lg flex-shrink-0"
								/>
							{/if}
							<div class="flex-1 min-w-0">
								<h3 class="font-medium truncate">{item.Title}</h3>
								{#if item.Description}
									<p class="text-sm text-gray-600 mt-1 line-clamp-2">{item.Description}</p>
								{/if}
								<div class="flex items-center gap-3 mt-2 text-xs text-gray-500">
									<span>{formatDate(item.Published)}</span>
									{#if item.ItemType}
										<span class="bg-gray-100 px-2 py-0.5 rounded">{item.ItemType}</span>
									{/if}
									{#if item.Categories && item.Categories.length > 0}
										<span class="truncate">
											{item.Categories.map((c) => c.Name).join(', ')}
										</span>
									{/if}
								</div>
							</div>
							{#if item.Link}
								<a
									href={item.Link}
									target="_blank"
									rel="noopener noreferrer"
									class="text-primary-600 hover:text-primary-800 text-sm flex-shrink-0"
								>
									View
								</a>
							{/if}
						</div>
					</div>
				{/each}
			</div>

			<div class="flex items-center justify-between mt-6 pt-4 border-t">
				<button
					onclick={prevPage}
					disabled={currentPage === 1}
					class="btn-secondary btn-sm disabled:opacity-50"
				>
					Previous
				</button>
				<span class="text-sm text-gray-500">
					Showing {(currentPage - 1) * limit + 1} - {Math.min(currentPage * limit, total)} of {total}
				</span>
				<button
					onclick={nextPage}
					disabled={currentPage * limit >= total}
					class="btn-secondary btn-sm disabled:opacity-50"
				>
					Next
				</button>
			</div>
		{/if}
	</div>
{/if}
