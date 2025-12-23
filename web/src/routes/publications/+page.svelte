<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type AutoUploadItem, type Connection } from '$lib/api';
	import { PageHeader, DataTable, Loading, Modal } from '$lib/components';

	let publications = $state<AutoUploadItem[]>([]);
	let connections = $state<Connection[]>([]);
	let error = $state('');
	let loading = $state(true);
	let platformFilter = $state('');

	let deleteModal = $state(false);
	let itemToDelete = $state<AutoUploadItem | null>(null);
	let deleting = $state(false);

	let triggerModal = $state(false);
	let triggering = $state(false);
	let triggerSuccess = $state('');

	const columns = [
		{ key: 'ItemName' as const, label: 'Item Name' },
		{ key: 'Platform' as const, label: 'Platform' },
		{
			key: 'PostUrl' as const,
			label: 'Post URL',
			format: (v: unknown) => (v ? 'View' : '-')
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
	const filteredPublications = $derived(
		platformFilter ? publications.filter((p) => p.Platform === platformFilter) : publications
	);

	onMount(async () => {
		try {
			const [pubs, conns] = await Promise.all([api.getPublications(), api.getConnections()]);
			publications = pubs;
			connections = conns;
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
</script>

<PageHeader title="Publications" description="Content published to social media platforms">
	{#snippet actions()}
		<button onclick={() => (triggerModal = true)} class="btn-primary">Trigger Upload</button>
	{/snippet}
</PageHeader>

{#if triggerSuccess}
	<div class="mb-4 p-3 bg-green-50 text-green-700 rounded-lg">{triggerSuccess}</div>
{/if}

{#if error}
	<div class="mb-4 p-3 bg-red-50 text-red-700 rounded-lg">{error}</div>
{/if}

{#if loading}
	<Loading message="Loading publications..." />
{:else}
	<div class="card mb-6">
		<div class="flex items-center gap-4">
			<label for="platformFilter" class="text-sm font-medium text-gray-700">Filter by platform:</label>
			<select id="platformFilter" bind:value={platformFilter} class="input w-48">
				<option value="">All platforms</option>
				{#each platforms as platform}
					<option value={platform}>{platform}</option>
				{/each}
			</select>
			<span class="text-sm text-gray-500">
				{filteredPublications.length} of {publications.length} publications
			</span>
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
		<strong>{itemToDelete?.ItemName}</strong>?
	</p>
	<p class="text-sm text-gray-500 mt-2">
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
		<p class="text-gray-500">No connections configured</p>
	{:else}
		<div class="space-y-2">
			{#each connections as conn}
				<button
					onclick={() => triggerUpload(conn.name)}
					disabled={triggering}
					class="w-full text-left p-3 border rounded-lg hover:bg-gray-50 transition-colors disabled:opacity-50"
				>
					<span class="font-medium">{conn.name}</span>
					<span class="text-sm text-gray-500 ml-2">
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
