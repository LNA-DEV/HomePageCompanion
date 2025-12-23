<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Subscriber } from '$lib/api';
	import { PageHeader, DataTable, Loading, Modal, StatsCard } from '$lib/components';

	let subscribers = $state<Subscriber[]>([]);
	let error = $state('');
	let loading = $state(true);

	let deleteModal = $state(false);
	let itemToDelete = $state<Subscriber | null>(null);
	let deleting = $state(false);

	const columns = [
		{
			key: 'endpoint' as const,
			label: 'Endpoint',
			format: (v: unknown) => {
				const url = v as string;
				try {
					const parsed = new URL(url);
					return parsed.hostname;
				} catch {
					return url.substring(0, 50) + '...';
				}
			}
		},
		{
			key: 'createdAt' as const,
			label: 'Subscribed',
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
			subscribers = await api.getSubscribers();
		} catch (e) {
			error = 'Failed to load subscribers';
		}
		loading = false;
	});

	function openDeleteModal(sub: Subscriber) {
		itemToDelete = sub;
		deleteModal = true;
	}

	async function confirmDelete() {
		if (!itemToDelete) return;
		deleting = true;
		try {
			await api.deleteSubscriber(itemToDelete.id);
			subscribers = subscribers.filter((s) => s.id !== itemToDelete!.id);
			deleteModal = false;
			itemToDelete = null;
		} catch (e) {
			error = 'Failed to delete subscriber';
		}
		deleting = false;
	}
</script>

<PageHeader
	title="Push Subscribers"
	description="Devices subscribed to receive push notifications"
/>

{#if error}
	<div class="mb-4 p-3 bg-red-50 text-red-700 rounded-lg">{error}</div>
{/if}

{#if loading}
	<Loading message="Loading subscribers..." />
{:else}
	<div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
		<StatsCard title="Total Subscribers" value={subscribers.length} icon="S" color="orange" />
	</div>

	<div class="card">
		<DataTable
			{columns}
			data={subscribers}
			onDelete={(row) => openDeleteModal(row as Subscriber)}
			emptyMessage="No subscribers yet"
		/>
	</div>
{/if}

<Modal open={deleteModal} title="Delete Subscriber" onClose={() => (deleteModal = false)}>
	<p>Are you sure you want to remove this subscriber?</p>
	<p class="text-sm text-gray-500 mt-2">They will no longer receive push notifications.</p>
	{#snippet actions()}
		<button onclick={() => (deleteModal = false)} class="btn-secondary">Cancel</button>
		<button onclick={confirmDelete} class="btn-danger" disabled={deleting}>
			{deleting ? 'Deleting...' : 'Delete'}
		</button>
	{/snippet}
</Modal>
