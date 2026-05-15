<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Webmention, type MiniFeedItem } from '$lib/api';
	import { PageHeader, DataTable, Loading, StatsCard, SearchInput } from '$lib/components';
	import { ExternalLink } from 'lucide-svelte';

	let webmentions = $state<Webmention[]>([]);
	let lookup = $state<Record<string, MiniFeedItem>>({});
	let error = $state('');
	let loading = $state(true);
	let query = $state('');

	const columns = [
		{
			key: 'Source' as const,
			label: 'Source',
			format: (v: unknown) => {
				const url = v as string;
				try {
					return new URL(url).hostname;
				} catch {
					return url.substring(0, 40) + '...';
				}
			}
		},
		{
			key: 'Target' as const,
			label: 'Target',
			render: targetCell
		},
		{
			key: 'CreatedAt' as const,
			label: 'Received',
			format: (v: unknown) =>
				new Date(v as string).toLocaleDateString('en-US', {
					year: 'numeric',
					month: 'short',
					day: 'numeric',
					hour: '2-digit',
					minute: '2-digit'
				})
		}
	];

	const filtered = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q) return webmentions;
		return webmentions.filter((w) => {
			const title = lookup[w.Target]?.title ?? '';
			const hay = (w.Source + ' ' + w.Target + ' ' + title).toLowerCase();
			return hay.includes(q);
		});
	});

	onMount(async () => {
		try {
			webmentions = await api.getWebmentions();
			const targets = [...new Set(webmentions.map((w) => w.Target).filter(Boolean))];
			if (targets.length > 0) {
				lookup = await api.getFeedItemsLookupByLink(targets);
			}
		} catch (e) {
			error = 'Failed to load webmentions';
		}
		loading = false;
	});

	function uniqueSources(): number {
		return new Set(
			webmentions
				.map((w) => {
					try {
						return new URL(w.Source).hostname;
					} catch {
						return w.Source;
					}
				})
				.filter(Boolean)
		).size;
	}
</script>

{#snippet targetCell(w: Webmention)}
	{@const mini = lookup[w.Target]}
	{#if mini}
		<a
			href="/feeds/{mini.feedId}"
			class="text-primary-700 dark:text-primary-300 hover:underline truncate inline-block max-w-xs"
			title={mini.title}
		>
			{mini.title}
		</a>
	{:else}
		<span class="text-gray-700 dark:text-gray-200 text-sm truncate inline-block max-w-xs" title={w.Target}>
			{(() => {
				try {
					return new URL(w.Target).pathname || '/';
				} catch {
					return w.Target;
				}
			})()}
		</span>
	{/if}
{/snippet}

<PageHeader
	title="Webmentions"
	description="IndieWeb webmentions received from other websites"
/>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">
		{error}
	</div>
{/if}

{#if loading}
	<Loading message="Loading webmentions..." />
{:else}
	<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
		<StatsCard title="Total Webmentions" value={webmentions.length} icon="W" color="purple" />
		<StatsCard title="Unique Sources" value={uniqueSources()} icon="U" color="blue" />
	</div>

	<div class="card mb-6">
		<SearchInput bind:value={query} onInput={(v) => (query = v)} placeholder="Search source, target, or item…" />
	</div>

	<div class="card">
		<DataTable {columns} data={filtered} emptyMessage="No webmentions received yet" />
	</div>

	{#if filtered.length > 0}
		<div class="card mt-6">
			<h2 class="text-lg font-semibold mb-4">Recent Webmentions</h2>
			<div class="space-y-4">
				{#each filtered.slice(0, 5) as wm}
					{@const mini = lookup[wm.Target]}
					<div class="border border-gray-200 dark:border-gray-700 rounded-lg p-4">
						<div class="flex items-start justify-between gap-4">
							<div class="flex-1 min-w-0">
								<a
									href={wm.Source}
									target="_blank"
									rel="noopener noreferrer"
									class="text-primary-600 dark:text-primary-400 hover:underline font-medium truncate block"
								>
									<ExternalLink size={14} class="inline mr-1" />
									{wm.Source}
								</a>
								<p class="text-sm text-gray-500 dark:text-gray-400 mt-1">
									mentioned
									{#if mini}
										<a
											href="/feeds/{mini.feedId}"
											class="text-primary-700 dark:text-primary-300 hover:underline"
										>
											{mini.title}
										</a>
									{:else}
										<a
											href={wm.Target}
											target="_blank"
											rel="noopener noreferrer"
											class="text-gray-700 dark:text-gray-300 hover:underline"
										>
											{wm.Target}
										</a>
									{/if}
								</p>
							</div>
							<span class="text-xs text-gray-400 dark:text-gray-500 flex-shrink-0">
								{new Date(wm.CreatedAt).toLocaleDateString()}
							</span>
						</div>
					</div>
				{/each}
			</div>
		</div>
	{/if}
{/if}
