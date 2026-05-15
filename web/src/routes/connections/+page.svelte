<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Connection } from '$lib/api';
	import { PageHeader, DataTable, Loading, StatsCard, PlatformBadge } from '$lib/components';
	import { ExternalLink } from 'lucide-svelte';

	let connections = $state<Connection[]>([]);
	let error = $state('');
	let loading = $state(true);

	const columns = [
		{ key: 'name' as const, label: 'Name' },
		{ key: 'sourceName' as const, label: 'Source', render: sourceCell },
		{ key: 'targetName' as const, label: 'Target', render: targetCell },
		{ key: 'platform' as const, label: 'Platform', render: platformCell },
		{
			key: 'caption' as const,
			label: 'Caption',
			format: (v: unknown) => {
				const s = (v as string) ?? '';
				return s.length > 60 ? s.substring(0, 57) + '…' : s || '-';
			}
		},
		{
			key: 'cron' as const,
			label: 'Schedule',
			format: (v: unknown) => (v as string | null) ?? 'manual'
		}
	];

	const scheduledCount = $derived(connections.filter((c) => c.cron !== null).length);
	const platformCount = $derived(new Set(connections.map((c) => c.platform)).size);

	onMount(async () => {
		try {
			connections = await api.getConnections();
		} catch (e) {
			error = 'Failed to load connections';
		}
		loading = false;
	});
</script>

{#snippet sourceCell(conn: Connection)}
	{#if conn.sourceFeedId}
		<a
			href="/feeds/{conn.sourceFeedId}"
			class="text-primary-700 dark:text-primary-300 hover:underline font-medium"
		>
			{conn.sourceName}
		</a>
	{:else}
		<span>{conn.sourceName}</span>
	{/if}
{/snippet}

{#snippet targetCell(conn: Connection)}
	{#if conn.targetUrl}
		<a
			href={conn.targetUrl}
			target="_blank"
			rel="noopener noreferrer"
			class="inline-flex items-center gap-1 text-primary-700 dark:text-primary-300 hover:underline"
		>
			{conn.targetName}
			<ExternalLink size={12} />
		</a>
	{:else}
		<span>{conn.targetName}</span>
	{/if}
{/snippet}

{#snippet platformCell(conn: Connection)}
	{#if conn.platform}
		<PlatformBadge platform={conn.platform} />
	{:else}
		<span class="text-gray-400 dark:text-gray-500">—</span>
	{/if}
{/snippet}

<PageHeader
	title="Connections"
	description="Configured publishing connections between sources and targets"
/>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">
		{error}
	</div>
{/if}

{#if loading}
	<Loading message="Loading connections..." />
{:else}
	<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
		<StatsCard title="Total Connections" value={connections.length} icon="C" color="blue" />
		<StatsCard title="Scheduled" value={scheduledCount} icon="S" color="green" />
		<StatsCard title="Platforms" value={platformCount} icon="P" color="purple" />
	</div>

	<div class="card">
		<DataTable {columns} data={connections} emptyMessage="No connections configured" />
	</div>
{/if}
