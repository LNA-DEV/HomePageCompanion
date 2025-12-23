<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type DashboardStats } from '$lib/api';
	import { PageHeader, StatsCard, Loading } from '$lib/components';

	let stats = $state<DashboardStats | null>(null);
	let error = $state('');
	let loading = $state(true);

	onMount(async () => {
		try {
			stats = await api.getStats();
		} catch (e) {
			error = 'Failed to load dashboard statistics';
		}
		loading = false;
	});
</script>

<PageHeader title="Dashboard" description="Overview of your HomePageCompanion instance" />

{#if loading}
	<Loading message="Loading statistics..." />
{:else if error}
	<div class="card">
		<p class="text-red-600">{error}</p>
	</div>
{:else if stats}
	<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
		<StatsCard title="Feeds" value={stats.feedCount} icon="R" color="blue" />
		<StatsCard title="Feed Items" value={stats.feedItemCount} icon="I" color="purple" />
		<StatsCard title="Publications" value={stats.publicationCount} icon="P" color="green" />
		<StatsCard title="Total Likes" value={stats.totalLikes} icon="L" color="red" />
	</div>

	<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
		<StatsCard title="Subscribers" value={stats.subscriberCount} icon="S" color="orange" />
		<StatsCard title="Webmentions" value={stats.webmentionCount} icon="W" color="purple" />
		<StatsCard title="Native Likes" value={stats.nativeLikeCount} icon="N" color="green" />
		<StatsCard title="Connections" value={stats.connectionCount} icon="C" color="blue" />
	</div>

	<div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
		<div class="card">
			<h2 class="text-xl font-semibold mb-4">Platform Breakdown</h2>
			{#if Object.keys(stats.platformBreakdown).length > 0}
				<div class="space-y-3">
					{#each Object.entries(stats.platformBreakdown) as [platform, count]}
						<div class="flex items-center justify-between">
							<span class="capitalize font-medium text-gray-700">{platform}</span>
							<span class="bg-gray-100 px-3 py-1 rounded-full text-sm font-mono">
								{count.toLocaleString()} publications
							</span>
						</div>
					{/each}
				</div>
			{:else}
				<p class="text-gray-500">No publications yet</p>
			{/if}
		</div>

		<div class="card">
			<h2 class="text-xl font-semibold mb-4">Quick Actions</h2>
			<div class="space-y-3">
				<a
					href="/feeds"
					class="block p-3 bg-gray-50 rounded-lg hover:bg-gray-100 transition-colors"
				>
					<span class="font-medium">Manage Feeds</span>
					<p class="text-sm text-gray-500 mt-1">View and manage RSS feed sources</p>
				</a>
				<a
					href="/broadcast"
					class="block p-3 bg-gray-50 rounded-lg hover:bg-gray-100 transition-colors"
				>
					<span class="font-medium">Send Notification</span>
					<p class="text-sm text-gray-500 mt-1">Broadcast a push notification to subscribers</p>
				</a>
				<a
					href="/interactions"
					class="block p-3 bg-gray-50 rounded-lg hover:bg-gray-100 transition-colors"
				>
					<span class="font-medium">View Interactions</span>
					<p class="text-sm text-gray-500 mt-1">See engagement metrics across platforms</p>
				</a>
			</div>
		</div>
	</div>
{/if}
