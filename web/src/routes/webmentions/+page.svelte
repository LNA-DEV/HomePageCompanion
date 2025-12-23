<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Webmention } from '$lib/api';
	import { PageHeader, DataTable, Loading, StatsCard } from '$lib/components';

	let webmentions = $state<Webmention[]>([]);
	let error = $state('');
	let loading = $state(true);

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
			format: (v: unknown) => {
				const url = v as string;
				try {
					const parsed = new URL(url);
					return parsed.pathname || '/';
				} catch {
					return url.substring(0, 40) + '...';
				}
			}
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

	onMount(async () => {
		try {
			webmentions = await api.getWebmentions();
		} catch (e) {
			error = 'Failed to load webmentions';
		}
		loading = false;
	});
</script>

<PageHeader
	title="Webmentions"
	description="IndieWeb webmentions received from other websites"
/>

{#if error}
	<div class="mb-4 p-3 bg-red-50 text-red-700 rounded-lg">{error}</div>
{/if}

{#if loading}
	<Loading message="Loading webmentions..." />
{:else}
	<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
		<StatsCard title="Total Webmentions" value={webmentions.length} icon="W" color="purple" />
		<StatsCard
			title="Unique Sources"
			value={new Set(webmentions.map((w) => new URL(w.Source).hostname)).size}
			icon="U"
			color="blue"
		/>
	</div>

	<div class="card">
		<DataTable {columns} data={webmentions} emptyMessage="No webmentions received yet" />
	</div>

	{#if webmentions.length > 0}
		<div class="card mt-6">
			<h2 class="text-lg font-semibold mb-4">Recent Webmentions</h2>
			<div class="space-y-4">
				{#each webmentions.slice(0, 5) as wm}
					<div class="border rounded-lg p-4">
						<div class="flex items-start justify-between gap-4">
							<div class="flex-1 min-w-0">
								<a
									href={wm.Source}
									target="_blank"
									rel="noopener noreferrer"
									class="text-primary-600 hover:underline font-medium truncate block"
								>
									{wm.Source}
								</a>
								<p class="text-sm text-gray-500 mt-1">
									mentioned
									<a
										href={wm.Target}
										target="_blank"
										rel="noopener noreferrer"
										class="text-gray-700 hover:underline"
									>
										{wm.Target}
									</a>
								</p>
							</div>
							<span class="text-xs text-gray-400 flex-shrink-0">
								{new Date(wm.CreatedAt).toLocaleDateString()}
							</span>
						</div>
					</div>
				{/each}
			</div>
		</div>
	{/if}
{/if}
