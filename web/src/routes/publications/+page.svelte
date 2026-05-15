<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type AutoUploadItem, type Connection, type MiniFeedItem } from '$lib/api';
	import { PageHeader, DataTable, Loading, Modal, SearchInput, PlatformBadge } from '$lib/components';
	import { ExternalLink } from 'lucide-svelte';

	let publications = $state<AutoUploadItem[]>([]);
	let connections = $state<Connection[]>([]);
	let lookup = $state<Record<string, MiniFeedItem>>({});
	let error = $state('');
	let loading = $state(true);
	let platformFilter = $state('');
	let query = $state('');

	let deleteModal = $state(false);
	let itemToDelete = $state<AutoUploadItem | null>(null);
	let deleting = $state(false);

	let triggerModal = $state(false);
	let triggering = $state(false);
	let triggerSuccess = $state('');

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
			key: 'PostUrl' as const,
			label: 'Post URL',
			render: postUrlCell
		},
		{
			key: 'CreatedAt' as const,
			label: 'Published',
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

	const platforms = $derived([...new Set(publications.map((p) => p.Platform))]);
	const filteredPublications = $derived.by(() => {
		const q = query.trim().toLowerCase();
		return publications.filter((p) => {
			if (platformFilter && p.Platform !== platformFilter) return false;
			if (!q) return true;
			const title = lookup[p.ItemID]?.title ?? '';
			const hay = (title + ' ' + p.ItemID + ' ' + p.Platform).toLowerCase();
			return hay.includes(q);
		});
	});

	onMount(async () => {
		try {
			const [pubs, conns] = await Promise.all([api.getPublications(), api.getConnections()]);
			publications = pubs;
			connections = conns;
			const guids = [...new Set(pubs.map((p) => p.ItemID).filter(Boolean))];
			if (guids.length > 0) {
				lookup = await api.getFeedItemsLookup(guids);
			}
		} catch (e) {
			error = 'Failed to load publications';
		}
		loading = false;
	});

	function openDeleteModal(pub: AutoUploadItem) {
		itemToDelete = pub;
		deleteModal = true;
	}

	async function confirmDelete() {
		if (!itemToDelete) return;
		deleting = true;
		try {
			await api.deletePublication(itemToDelete.ID);
			publications = publications.filter((p) => p.ID !== itemToDelete!.ID);
			deleteModal = false;
			itemToDelete = null;
		} catch (e) {
			error = 'Failed to delete publication';
		}
		deleting = false;
	}

	async function triggerUpload(connectionName: string) {
		triggering = true;
		triggerSuccess = '';
		try {
			await api.triggerUpload(connectionName);
			triggerSuccess = `Upload triggered for ${connectionName}`;
			setTimeout(() => {
				triggerSuccess = '';
			}, 3000);
		} catch (e) {
			error = `Failed to trigger upload for ${connectionName}`;
		}
		triggering = false;
		triggerModal = false;
	}

	function titleFor(pub: AutoUploadItem): string {
		return lookup[pub.ItemID]?.title ?? pub.ItemID;
	}
</script>

{#snippet itemCell(pub: AutoUploadItem)}
	{@const mini = lookup[pub.ItemID]}
	<div class="flex items-center gap-3 min-w-0">
		{#if mini?.imageUrl}
			<img src={mini.imageUrl} alt="" class="w-10 h-10 object-cover rounded flex-shrink-0" />
		{:else}
			<div
				class="w-10 h-10 rounded bg-gray-100 dark:bg-gray-800 flex-shrink-0 flex items-center justify-center text-gray-400 text-xs"
			>
				?
			</div>
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
				<span class="text-xs text-gray-500 dark:text-gray-400 truncate block">{pub.ItemID}</span>
			{:else}
				<span class="text-sm text-gray-700 dark:text-gray-200 truncate block">{pub.ItemID}</span>
			{/if}
		</div>
	</div>
{/snippet}

{#snippet platformCell(pub: AutoUploadItem)}
	<PlatformBadge platform={pub.Platform} />
{/snippet}

{#snippet postUrlCell(pub: AutoUploadItem)}
	{#if pub.PostUrl}
		<a
			href={pub.PostUrl}
			target="_blank"
			rel="noopener noreferrer"
			class="inline-flex items-center gap-1 text-primary-600 dark:text-primary-400 hover:underline"
		>
			<ExternalLink size={14} />
			Open
		</a>
	{:else}
		<span class="text-gray-400 dark:text-gray-500">—</span>
	{/if}
{/snippet}

<PageHeader title="Publications" description="Content published to social media platforms">
	{#snippet actions()}
		<button onclick={() => (triggerModal = true)} class="btn-primary">Trigger Upload</button>
	{/snippet}
</PageHeader>

{#if triggerSuccess}
	<div class="mb-4 p-3 bg-green-50 dark:bg-green-900/30 text-green-700 dark:text-green-300 rounded-lg">
		{triggerSuccess}
	</div>
{/if}

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">
		{error}
	</div>
{/if}

{#if loading}
	<Loading message="Loading publications..." />
{:else}
	<div class="card mb-6">
		<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
			<SearchInput bind:value={query} onInput={(v) => (query = v)} placeholder="Search by title or platform…" />
			<div class="flex items-center gap-3">
				<label for="platformFilter" class="text-sm font-medium text-gray-700 dark:text-gray-300 whitespace-nowrap">
					Platform:
				</label>
				<select id="platformFilter" bind:value={platformFilter} class="input w-full sm:w-40">
					<option value="">All</option>
					{#each platforms as platform}
						<option value={platform}>{platform}</option>
					{/each}
				</select>
				<span class="text-sm text-gray-500 dark:text-gray-400 whitespace-nowrap">
					{filteredPublications.length} of {publications.length}
				</span>
			</div>
		</div>
	</div>

	<div class="card">
		<DataTable
			{columns}
			data={filteredPublications}
			onDelete={(row) => openDeleteModal(row as AutoUploadItem)}
			emptyMessage="No publications found"
		/>
	</div>
{/if}

<Modal open={deleteModal} title="Delete Publication" onClose={() => (deleteModal = false)}>
	<p>
		Are you sure you want to delete the publication record for
		<strong>{itemToDelete ? titleFor(itemToDelete) : ''}</strong>?
	</p>
	<p class="text-sm text-gray-500 dark:text-gray-400 mt-2">
		This will only remove the record from the database, not the actual post on the platform.
	</p>
	{#snippet actions()}
		<button onclick={() => (deleteModal = false)} class="btn-secondary">Cancel</button>
		<button onclick={confirmDelete} class="btn-danger" disabled={deleting}>
			{deleting ? 'Deleting...' : 'Delete'}
		</button>
	{/snippet}
</Modal>

<Modal open={triggerModal} title="Trigger Upload" onClose={() => (triggerModal = false)}>
	<p class="mb-4">Select a connection to trigger an upload:</p>
	{#if connections.length === 0}
		<p class="text-gray-500 dark:text-gray-400">No connections configured</p>
	{:else}
		<div class="space-y-2">
			{#each connections as conn}
				<button
					onclick={() => triggerUpload(conn.name)}
					disabled={triggering}
					class="w-full text-left p-3 border border-gray-200 dark:border-gray-700 rounded-lg hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors disabled:opacity-50"
				>
					<span class="font-medium">{conn.name}</span>
					<span class="text-sm text-gray-500 dark:text-gray-400 ml-2">
						{conn.sourceName} &rarr; {conn.targetName} ({conn.platform})
					</span>
				</button>
			{/each}
		</div>
	{/if}
	{#snippet actions()}
		<button onclick={() => (triggerModal = false)} class="btn-secondary">Close</button>
	{/snippet}
</Modal>
