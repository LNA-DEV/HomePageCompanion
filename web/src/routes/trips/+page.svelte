<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api, type TripListItem } from '$lib/api';
	import { PageHeader, Loading, Modal } from '$lib/components';
	import { MapPin, Trash2, Pencil, Eye, EyeOff } from 'lucide-svelte';

	let trips = $state<TripListItem[]>([]);
	let loading = $state(true);
	let error = $state('');

	let createOpen = $state(false);
	let creating = $state(false);
	let createError = $state('');
	let newTitle = $state('');
	let newSlug = $state('');

	let deleteId = $state<number | null>(null);
	let deleting = $state(false);

	async function load() {
		loading = true;
		try {
			const resp = await api.getTrips();
			trips = resp.items ?? [];
			error = '';
		} catch {
			error = 'Failed to load trips';
		}
		loading = false;
	}

	onMount(load);

	async function create() {
		createError = '';
		const title = newTitle.trim();
		if (!title) {
			createError = 'Title is required.';
			return;
		}
		creating = true;
		try {
			const trip = await api.createTrip({ title, slug: newSlug.trim() || undefined });
			createOpen = false;
			newTitle = '';
			newSlug = '';
			goto(`/trips/${trip.id}`);
		} catch (e) {
			createError = e instanceof Error ? e.message : 'Failed to create trip.';
		}
		creating = false;
	}

	async function confirmDelete() {
		if (deleteId === null) return;
		deleting = true;
		try {
			await api.deleteTrip(deleteId);
			deleteId = null;
			await load();
		} catch {
			error = 'Delete failed';
		}
		deleting = false;
	}
</script>

<PageHeader title="Trips" description="Build the trip overview the website renders — stops, photos and routes.">
	{#snippet actions()}
		<button onclick={() => (createOpen = true)} class="btn-primary">New Trip</button>
	{/snippet}
</PageHeader>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}

{#if loading}
	<Loading message="Loading trips..." />
{:else if trips.length === 0}
	<div class="card text-center py-12">
		<p class="text-gray-500 dark:text-gray-400">No trips yet — click New Trip to start one.</p>
	</div>
{:else}
	<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
		{#each trips as trip (trip.id)}
			<div class="card flex flex-col gap-3">
				<div class="flex items-start justify-between gap-2">
					<div class="min-w-0">
						<h2 class="font-medium text-lg truncate">{trip.title}</h2>
						<p class="text-xs text-gray-500 dark:text-gray-400 truncate">/{trip.slug}</p>
					</div>
					{#if trip.published}
						<span class="badge bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-200">
							<Eye size={12} /> Published
						</span>
					{:else}
						<span class="badge bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300">
							<EyeOff size={12} /> Draft
						</span>
					{/if}
				</div>
				<p class="text-sm text-gray-500 dark:text-gray-400 flex items-center gap-1.5">
					<MapPin size={14} />
					{trip.stopCount}
					{trip.stopCount === 1 ? 'stop' : 'stops'}
				</p>
				<div class="flex items-center gap-2 mt-auto pt-2">
					<a href="/trips/{trip.id}" class="btn-secondary btn-sm flex items-center gap-1">
						<Pencil size={14} /> Edit
					</a>
					<button
						onclick={() => (deleteId = trip.id)}
						class="text-red-600 dark:text-red-400 hover:text-red-800 dark:hover:text-red-300 text-sm flex items-center gap-1 ml-auto"
					>
						<Trash2 size={14} /> Delete
					</button>
				</div>
			</div>
		{/each}
	</div>
{/if}

<Modal open={createOpen} title="New Trip" onClose={() => (createOpen = false)}>
	<div class="space-y-4">
		<div>
			<label for="trip-title" class="label">Title</label>
			<input id="trip-title" type="text" bind:value={newTitle} class="input" placeholder="e.g. Europe, mostly by train" />
		</div>
		<div>
			<label for="trip-slug" class="label">Slug (optional)</label>
			<input id="trip-slug" type="text" bind:value={newSlug} class="input" placeholder="auto-generated from title" />
			<p class="text-xs text-gray-500 dark:text-gray-400 mt-1">Used in the public URL: /api/trips/&lt;slug&gt;</p>
		</div>
		{#if createError}
			<div class="p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{createError}</div>
		{/if}
	</div>
	{#snippet actions()}
		<button onclick={() => (createOpen = false)} class="btn-secondary" disabled={creating}>Cancel</button>
		<button onclick={create} class="btn-primary" disabled={creating}>
			{creating ? 'Creating…' : 'Create'}
		</button>
	{/snippet}
</Modal>

<Modal open={deleteId !== null} title="Delete Trip" onClose={() => (deleteId = null)}>
	<p>This permanently deletes the trip, all its stops and photo references.</p>
	{#snippet actions()}
		<button onclick={() => (deleteId = null)} class="btn-secondary">Cancel</button>
		<button onclick={confirmDelete} class="btn-danger" disabled={deleting}>
			{deleting ? 'Deleting…' : 'Delete'}
		</button>
	{/snippet}
</Modal>
