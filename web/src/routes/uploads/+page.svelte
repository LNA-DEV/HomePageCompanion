<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type UploadAttempt, type MiniFeedItem } from '$lib/api';
	import { PageHeader, DataTable, Loading, SearchInput, PlatformBadge } from '$lib/components';
	import { CheckCircle2, XCircle, ExternalLink } from 'lucide-svelte';

	let items = $state<UploadAttempt[]>([]);
	let total = $state(0);
	let page = $state(1);
	let limit = 50;
	let lookup = $state<Record<string, MiniFeedItem>>({});
	let loading = $state(true);
	let error = $state('');

	let statusFilter = $state<'all' | 'failed' | 'success'>('failed');
	let platformFilter = $state('');
	let query = $state('');

	const columns = [
		{ key: 'itemId' as const, label: 'Item', render: itemCell },
		{ key: 'platform' as const, label: 'Platform', render: platformCell },
		{ key: 'targetName' as const, label: 'Target' },
		{ key: 'success' as const, label: 'Status', render: statusCell },
		{ key: 'errorMessage' as const, label: 'Error', render: errorCell },
		{
			key: 'createdAt' as const,
			label: 'When',
			format: (v: unknown) =>
				new Date(v as string).toLocaleDateString('en-US', {
					month: 'short',
					day: 'numeric',
					hour: '2-digit',
					minute: '2-digit'
				})
		}
	];

	const platforms = $derived([...new Set(items.map((i) => i.platform))]);
	const filtered = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q) return items;
		return items.filter((i) => {
			const title = lookup[i.itemId]?.title ?? '';
			const hay = (title + ' ' + i.itemId + ' ' + i.platform + ' ' + i.targetName + ' ' + (i.errorMessage ?? '')).toLowerCase();
			return hay.includes(q);
		});
	});

	async function load() {
		loading = true;
		try {
			const resp = await api.getUploadAttempts({
				status: statusFilter,
				platform: platformFilter || undefined,
				page,
				limit
			});
			items = resp.items ?? [];
			total = resp.total;
			const guids = [...new Set(items.map((i) => i.itemId).filter(Boolean))];
			if (guids.length > 0) {
				lookup = await api.getFeedItemsLookup(guids);
			} else {
				lookup = {};
			}
			error = '';
		} catch (e) {
			error = 'Failed to load upload attempts';
		}
		loading = false;
	}

	onMount(load);

	function changeStatus(v: 'all' | 'failed' | 'success') {
		statusFilter = v;
		page = 1;
		load();
	}
	function changePlatform(v: string) {
		platformFilter = v;
		page = 1;
		load();
	}
</script>

{#snippet itemCell(a: UploadAttempt)}
	{@const mini = lookup[a.itemId]}
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
				<span class="text-xs text-gray-500 dark:text-gray-400 truncate block">{a.itemId}</span>
			{:else}
				<span class="text-sm text-gray-700 dark:text-gray-200 truncate block">{a.itemId}</span>
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

{#snippet platformCell(a: UploadAttempt)}
	<PlatformBadge platform={a.platform} />
{/snippet}

{#snippet statusCell(a: UploadAttempt)}
	{#if a.success}
		<span class="inline-flex items-center gap-1 text-green-700 dark:text-green-400 text-sm font-medium">
			<CheckCircle2 size={14} />
			Success
		</span>
	{:else}
		<span
			class="inline-flex items-center gap-1 text-red-700 dark:text-red-400 text-sm font-medium"
			title={a.errorCode ?? ''}
		>
			<XCircle size={14} />
			{a.errorCode || 'failed'}{a.httpStatus ? ` · ${a.httpStatus}` : ''}
		</span>
	{/if}
{/snippet}

{#snippet errorCell(a: UploadAttempt)}
	{#if a.errorMessage}
		<span
			class="text-xs text-gray-700 dark:text-gray-300 line-clamp-2 max-w-md inline-block"
			title={a.errorMessage}
		>
			{a.errorMessage}
		</span>
	{:else}
		<span class="text-gray-400 dark:text-gray-500">—</span>
	{/if}
{/snippet}

<PageHeader title="Upload Attempts" description="Every attempt to publish an item to a target platform — success or failure" />

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}

<div class="card mb-6">
	<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:flex-wrap">
		<div class="flex-1 min-w-[200px]">
			<SearchInput bind:value={query} onInput={(v) => (query = v)} placeholder="Search title, target, error…" />
		</div>
		<div class="flex items-center gap-2">
			<label for="statusFilter" class="text-sm font-medium text-gray-700 dark:text-gray-300">Status:</label>
			<select
				id="statusFilter"
				value={statusFilter}
				onchange={(e) => changeStatus((e.target as HTMLSelectElement).value as 'all' | 'failed' | 'success')}
				class="input w-full sm:w-36"
			>
				<option value="failed">Failed</option>
				<option value="success">Success</option>
				<option value="all">All</option>
			</select>
		</div>
		<div class="flex items-center gap-2">
			<label for="platformFilter" class="text-sm font-medium text-gray-700 dark:text-gray-300">Platform:</label>
			<select
				id="platformFilter"
				value={platformFilter}
				onchange={(e) => changePlatform((e.target as HTMLSelectElement).value)}
				class="input w-full sm:w-40"
			>
				<option value="">All</option>
				{#each platforms as p}
					<option value={p}>{p}</option>
				{/each}
			</select>
		</div>
		<span class="text-sm text-gray-500 dark:text-gray-400 whitespace-nowrap">
			{filtered.length} of {total}
		</span>
	</div>
</div>

{#if loading}
	<Loading message="Loading upload attempts..." />
{:else}
	<div class="card">
		<DataTable {columns} data={filtered} emptyMessage="No upload attempts match these filters" />
	</div>

	{#if total > limit}
		<div class="flex items-center justify-between mt-6">
			<button
				onclick={() => {
					if (page > 1) {
						page--;
						load();
					}
				}}
				disabled={page === 1}
				class="btn-secondary btn-sm disabled:opacity-50"
			>
				Previous
			</button>
			<span class="text-sm text-gray-500 dark:text-gray-400">
				Page {page} of {Math.ceil(total / limit) || 1}
			</span>
			<button
				onclick={() => {
					if (page * limit < total) {
						page++;
						load();
					}
				}}
				disabled={page * limit >= total}
				class="btn-secondary btn-sm disabled:opacity-50"
			>
				Next
			</button>
		</div>
	{/if}
{/if}
