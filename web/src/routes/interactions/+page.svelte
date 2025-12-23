<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Interaction, type InteractionSummary } from '$lib/api';
	import { PageHeader, StatsCard, DataTable, Loading } from '$lib/components';

	let interactions = $state<Interaction[]>([]);
	let summary = $state<InteractionSummary | null>(null);
	let error = $state('');
	let loading = $state(true);
	let platformFilter = $state('');

	const columns = [
		{ key: 'ItemName' as const, label: 'Item' },
		{ key: 'Platform' as const, label: 'Platform' },
		{ key: 'TargetName' as const, label: 'Target' },
		{
			key: 'LikeCount' as const,
			label: 'Likes',
			format: (v: unknown) => (v as number).toLocaleString()
		},
		{
			key: 'UpdatedAt' as const,
			label: 'Last Updated',
			format: (v: unknown) =>
				new Date(v as string).toLocaleDateString('en-US', {
					month: 'short',
					day: 'numeric',
					hour: '2-digit',
					minute: '2-digit'
				})
		}
	];

	const platforms = $derived([...new Set(interactions.map((i) => i.Platform))]);
	const filteredInteractions = $derived(
		platformFilter ? interactions.filter((i) => i.Platform === platformFilter) : interactions
	);

	onMount(async () => {
		try {
			const [ints, sum] = await Promise.all([api.getInteractions(), api.getInteractionsSummary()]);
			interactions = ints;
			summary = sum;
		} catch (e) {
			error = 'Failed to load interactions';
		}
		loading = false;
	});
</script>

<PageHeader title="Interactions" description="Engagement metrics across all platforms" />

{#if error}
	<div class="mb-4 p-3 bg-red-50 text-red-700 rounded-lg">{error}</div>
{/if}

{#if loading}
	<Loading message="Loading interactions..." />
{:else if summary}
	<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
		<StatsCard title="Total Platform Likes" value={summary.totalLikes} icon="L" color="red" />
		<StatsCard title="Native Website Likes" value={summary.totalNativeLikes} icon="N" color="green" />
		<StatsCard
			title="Tracked Items"
			value={new Set(interactions.map((i) => i.ItemName)).size}
			icon="I"
			color="blue"
		/>
	</div>

	<div class="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-8">
		<div class="card">
			<h2 class="text-lg font-semibold mb-4">Likes by Platform</h2>
			{#if Object.keys(summary.platformBreakdown).length > 0}
				<div class="space-y-3">
					{#each Object.entries(summary.platformBreakdown) as [platform, count]}
						<div class="flex items-center justify-between">
							<span class="capitalize font-medium">{platform}</span>
							<div class="flex items-center gap-2">
								<div class="w-32 bg-gray-200 rounded-full h-2">
									<div
										class="bg-primary-600 h-2 rounded-full"
										style="width: {(count / summary.totalLikes) * 100}%"
									></div>
								</div>
								<span class="text-sm font-mono w-16 text-right">{count.toLocaleString()}</span>
							</div>
						</div>
					{/each}
				</div>
			{:else}
				<p class="text-gray-500">No platform data yet</p>
			{/if}
		</div>

		<div class="card">
			<h2 class="text-lg font-semibold mb-4">Top Items</h2>
			{#if summary.topItems && summary.topItems.length > 0}
				<div class="space-y-2">
					{#each summary.topItems.slice(0, 5) as item, i}
						<div class="flex items-center justify-between py-2 border-b last:border-0">
							<div class="flex items-center gap-3">
								<span class="w-6 h-6 bg-gray-100 rounded-full flex items-center justify-center text-xs font-medium">
									{i + 1}
								</span>
								<span class="truncate max-w-[200px]" title={item.itemName}>{item.itemName}</span>
							</div>
							<span class="font-mono text-sm">{item.totalLikes.toLocaleString()} likes</span>
						</div>
					{/each}
				</div>
			{:else}
				<p class="text-gray-500">No items yet</p>
			{/if}
		</div>
	</div>

	<div class="card">
		<div class="flex items-center justify-between mb-4">
			<h2 class="text-lg font-semibold">All Interactions</h2>
			<div class="flex items-center gap-4">
				<select bind:value={platformFilter} class="input w-40">
					<option value="">All platforms</option>
					{#each platforms as platform}
						<option value={platform}>{platform}</option>
					{/each}
				</select>
			</div>
		</div>
		<DataTable {columns} data={filteredInteractions} emptyMessage="No interactions recorded yet" />
	</div>
{/if}
