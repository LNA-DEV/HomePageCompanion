<script lang="ts">
	import { onMount } from 'svelte';
	import {
		api,
		type Interaction,
		type InteractionSummary,
		type MiniFeedItem
	} from '$lib/api';
	import {
		PageHeader,
		StatsCard,
		DataTable,
		Loading,
		SearchInput,
		PlatformBadge
	} from '$lib/components';
	import { ExternalLink } from 'lucide-svelte';

	let interactions = $state<Interaction[]>([]);
	let summary = $state<InteractionSummary | null>(null);
	let lookup = $state<Record<string, MiniFeedItem>>({});
	let error = $state('');
	let loading = $state(true);
	let platformFilter = $state('');
	let query = $state('');

	const columns = [
		{
			key: 'ItemID' as const,
			label: 'Item',
			render: itemCell
		},
		{
			key: 'Platform' as const,
			label: 'Platform',
			render: platformCell
		},
		{
			key: 'TargetName' as const,
			label: 'Target'
		},
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
	const filteredInteractions = $derived.by(() => {
		const q = query.trim().toLowerCase();
		return interactions.filter((i) => {
			if (platformFilter && i.Platform !== platformFilter) return false;
			if (!q) return true;
			const title = lookup[i.ItemID]?.title ?? '';
			const hay = (title + ' ' + i.ItemID + ' ' + i.Platform + ' ' + i.TargetName).toLowerCase();
			return hay.includes(q);
		});
	});

	onMount(async () => {
		try {
			const [ints, sum] = await Promise.all([api.getInteractions(), api.getInteractionsSummary()]);
			interactions = ints;
			summary = sum;
			const guidSet = new Set<string>();
			for (const i of ints) if (i.ItemID) guidSet.add(i.ItemID);
			for (const t of sum.topItems ?? []) if (t.itemId) guidSet.add(t.itemId);
			if (guidSet.size > 0) {
				lookup = await api.getFeedItemsLookup([...guidSet]);
			}
		} catch (e) {
			error = 'Failed to load interactions';
		}
		loading = false;
	});
</script>

{#snippet itemCell(i: Interaction)}
	{@const mini = lookup[i.ItemID]}
	<div class="flex items-center gap-3 min-w-0">
		{#if mini?.imageUrl}
			<img src={mini.imageUrl} alt="" class="w-10 h-10 object-cover rounded flex-shrink-0" />
		{:else}
			<div class="w-10 h-10 rounded bg-gray-100 dark:bg-gray-800 flex-shrink-0"></div>
		{/if}
		<div class="min-w-0 flex-1">
			{#if mini}
				<a
					href="/feeds/{mini.feedId}"
					class="font-medium text-primary-700 dark:text-primary-300 hover:underline truncate block"
					title={mini.title}
				>
					{mini.title}
				</a>
				<span class="text-xs text-gray-500 dark:text-gray-400 truncate block">{i.ItemID}</span>
			{:else}
				<span class="text-sm text-gray-700 dark:text-gray-200 truncate block">{i.ItemID}</span>
			{/if}
		</div>
		{#if mini?.link}
			<a
				href={mini.link}
				target="_blank"
				rel="noopener noreferrer"
				class="text-primary-600 dark:text-primary-400 hover:text-primary-800 dark:hover:text-primary-300"
				title="Open external"
			>
				<ExternalLink size={14} />
			</a>
		{/if}
	</div>
{/snippet}

{#snippet platformCell(i: Interaction)}
	<PlatformBadge platform={i.Platform} />
{/snippet}

<PageHeader title="Interactions" description="Engagement metrics across all platforms" />

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">
		{error}
	</div>
{/if}

{#if loading}
	<Loading message="Loading interactions..." />
{:else if summary}
	<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
		<StatsCard title="Total Platform Likes" value={summary.totalLikes} icon="L" color="red" />
		<StatsCard title="Native Website Likes" value={summary.totalNativeLikes} icon="N" color="green" />
		<StatsCard
			title="Tracked Items"
			value={new Set(interactions.map((i) => i.ItemID)).size}
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
							<PlatformBadge {platform} />
							<div class="flex items-center gap-2">
								<div class="w-32 bg-gray-200 dark:bg-gray-700 rounded-full h-2">
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
				<p class="text-gray-500 dark:text-gray-400">No platform data yet</p>
			{/if}
		</div>

		<div class="card">
			<h2 class="text-lg font-semibold mb-4">Top Items</h2>
			{#if summary.topItems && summary.topItems.length > 0}
				<div class="space-y-2">
					{#each summary.topItems.slice(0, 5) as item, i}
						{@const mini = lookup[item.itemId]}
						<div class="flex items-center gap-3 py-2 border-b border-gray-200 dark:border-gray-700 last:border-0">
							<span
								class="w-6 h-6 bg-gray-100 dark:bg-gray-800 rounded-full flex items-center justify-center text-xs font-medium flex-shrink-0"
							>
								{i + 1}
							</span>
							{#if mini?.imageUrl}
								<img src={mini.imageUrl} alt="" class="w-8 h-8 object-cover rounded flex-shrink-0" />
							{/if}
							<div class="flex-1 min-w-0">
								{#if mini}
									<a
										href="/feeds/{mini.feedId}"
										class="text-sm font-medium text-primary-700 dark:text-primary-300 hover:underline truncate block"
										title={mini.title}
									>
										{mini.title}
									</a>
								{:else}
									<span class="text-sm truncate block" title={item.itemId}>{item.itemId}</span>
								{/if}
							</div>
							<span class="font-mono text-sm flex-shrink-0">{item.totalLikes.toLocaleString()} likes</span>
						</div>
					{/each}
				</div>
			{:else}
				<p class="text-gray-500 dark:text-gray-400">No items yet</p>
			{/if}
		</div>
	</div>

	<div class="card">
		<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between mb-4">
			<h2 class="text-lg font-semibold">All Interactions</h2>
			<div class="flex items-center gap-3 flex-wrap">
				<SearchInput
					bind:value={query}
					onInput={(v) => (query = v)}
					placeholder="Search items…"
				/>
				<select bind:value={platformFilter} class="input w-full sm:w-40">
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
