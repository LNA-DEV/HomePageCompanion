<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type FeedWithCount } from '$lib/api';
	import { PageHeader, Loading } from '$lib/components';

	let feeds = $state<FeedWithCount[]>([]);
	let error = $state('');
	let loading = $state(true);

	onMount(async () => {
		try {
			feeds = await api.getFeeds();
		} catch (e) {
			error = 'Failed to load feeds';
		}
		loading = false;
	});
</script>

<PageHeader title="Feeds" description="RSS feed sources for content publishing" />

{#if loading}
	<Loading message="Loading feeds..." />
{:else if error}
	<div class="card">
		<p class="text-red-600">{error}</p>
	</div>
{:else if feeds.length === 0}
	<div class="card text-center py-12">
		<p class="text-gray-500">No feeds configured yet</p>
	</div>
{:else}
	<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
		{#each feeds as feed}
			<a href="/feeds/{feed.ID}" class="card hover:shadow-lg transition-shadow block">
				<h3 class="font-semibold text-lg mb-2">{feed.Title || feed.FeedName}</h3>
				{#if feed.Description}
					<p class="text-gray-600 text-sm mb-4 line-clamp-2">{feed.Description}</p>
				{/if}
				<div class="flex items-center justify-between text-sm">
					<span class="bg-primary-100 text-primary-700 px-2 py-1 rounded">
						{feed.itemCount} items
					</span>
					{#if feed.ItemTypes}
						<span class="text-gray-500">{feed.ItemTypes}</span>
					{/if}
				</div>
				{#if feed.Link}
					<p class="text-xs text-gray-400 mt-3 truncate">{feed.Link}</p>
				{/if}
			</a>
		{/each}
	</div>
{/if}
