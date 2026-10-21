<svelte:options customElement={{ tag: 'trips-page', shadow: 'none' }} />

<script lang="ts">
	// Trips: the list with the routing state of every leg; /b/trips/<id>
	// edits one trip.
	import { PageHeader, Modal } from '@companion/ui/components';
	import TripEditor from './TripEditor.svelte';
	import { tr } from './data';
	import type { RouteStatus, Trip, TripListItem } from './types';

	let { path = '' }: { path?: string } = $props();

	let trips = $state<TripListItem[] | null>(null);
	let routes = $state<RouteStatus | null>(null);
	let error = $state('');
	let createOpen = $state(false);
	let creating = $state(false);
	let createError = $state('');
	let newTitle = $state('');
	let newSlug = $state('');
	let deleteId = $state<number | null>(null);
	let busy = $state(false);

	const editId = $derived(/^\d+$/.test(path) ? Number(path) : null);

	async function load() {
		try {
			const [list, r] = await Promise.all([tr.api.get<{ items: TripListItem[] | null }>('trips'), tr.api.get<RouteStatus>('routes')]);
			trips = list.items ?? [];
			routes = r;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		if (editId === null) load();
	});

	async function create() {
		createError = '';
		if (!newTitle.trim()) {
			createError = 'A title is required.';
			return;
		}
		creating = true;
		try {
			const trip = await tr.api.post<Trip>('trips', { title: newTitle.trim(), slug: newSlug.trim() || undefined });
			createOpen = false;
			newTitle = newSlug = '';
			tr.navigate(String(trip.id));
		} catch (e) {
			createError = e instanceof Error ? e.message : String(e);
		}
		creating = false;
	}

	async function remove() {
		if (deleteId === null) return;
		busy = true;
		try {
			await tr.api.del(`trips/${deleteId}`);
			tr.toast('Trip deleted', 'success');
			deleteId = null;
			await load();
		} catch (e) {
			tr.toast(e instanceof Error ? e.message : String(e), 'error');
		}
		busy = false;
	}

	async function backfill(force: boolean) {
		busy = true;
		try {
			const r = await tr.api.post<{ queued: number }>(`routes/backfill${force ? '?force=1' : ''}`);
			tr.toast(`${r.queued} leg(s) queued for routing`, 'info');
			await load();
		} catch (e) {
			tr.toast(e instanceof Error ? e.message : String(e), 'error');
		}
		busy = false;
	}
</script>

{#if editId !== null}
	{#key editId}<TripEditor id={editId} />{/key}
{:else}
	<PageHeader title="Trips" description="The trip overview the website renders: stops, photos and routes">
		{#snippet actions()}
			<button class="btn-primary" onclick={() => (createOpen = true)}>New trip</button>
		{/snippet}
	</PageHeader>

	{#if error}<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>{/if}

	{#if routes}
		<div class="card mb-6 flex flex-wrap items-center justify-between gap-4" data-testid="route-status">
			<div class="text-sm">
				<p class="font-medium">Routes</p>
				{#if routes.router}
					<p class="text-gray-600 dark:text-gray-300">
						{routes.ok} computed · <span class={routes.failed ? 'text-red-700 dark:text-red-300' : ''}>{routes.failed} failed</span> · {routes.pending} waiting
					</p>
					<p class="text-xs text-gray-500 dark:text-gray-400">Car and train legs are routed once and stored; a failed one is drawn as a straight line.</p>
				{:else}
					<p class="text-amber-700 dark:text-amber-300">No router connected: set <code>routing:</code> in the trips config to draw real tracks.</p>
				{/if}
			</div>
			{#if routes.router}
				<div class="flex gap-2">
					<button class="btn-secondary btn-sm" disabled={busy} onclick={() => backfill(false)}>Route missing and failed</button>
					<button class="btn-secondary btn-sm" disabled={busy} onclick={() => backfill(true)}>Route all again</button>
				</div>
			{/if}
		</div>
	{/if}

	{#if !trips}
		<p class="text-gray-500">Loading…</p>
	{:else if trips.length === 0}
		<div class="card text-center py-12 text-gray-500">No trips yet: start one with New trip.</div>
	{:else}
		<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4" data-testid="trips">
			{#each trips as t (t.id)}
				<div class="card flex flex-col gap-3">
					<div class="flex items-start justify-between gap-2">
						<div class="min-w-0">
							<h2 class="font-medium text-lg truncate">{t.title}</h2>
							<p class="text-xs text-gray-500 dark:text-gray-400 truncate">/{t.slug}</p>
						</div>
						<span class="badge {t.published ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-200' : 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300'}">{t.published ? 'Published' : 'Draft'}</span>
					</div>
					<p class="text-sm text-gray-500 dark:text-gray-400">{t.stopCount} {t.stopCount === 1 ? 'stop' : 'stops'}</p>
					<div class="flex items-center gap-2 mt-auto pt-2">
						<a href={tr.href(String(t.id))} class="btn-secondary btn-sm">Edit</a>
						<button class="ml-auto text-sm text-red-600 dark:text-red-400 hover:underline" onclick={() => (deleteId = t.id)}>Delete</button>
					</div>
				</div>
			{/each}
		</div>
	{/if}
{/if}

<Modal open={createOpen} title="New trip" onClose={() => (createOpen = false)}>
	<div class="space-y-4">
		<div>
			<label for="trip-title" class="label">Title</label>
			<input id="trip-title" class="input" bind:value={newTitle} placeholder="e.g. Europe, mostly by train" />
		</div>
		<div>
			<label for="trip-slug" class="label">Slug (optional)</label>
			<input id="trip-slug" class="input" bind:value={newSlug} placeholder="made from the title" />
			<p class="text-xs text-gray-500 dark:text-gray-400 mt-1">The public URL: /api/trips/&lt;slug&gt;</p>
		</div>
		{#if createError}<div class="p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{createError}</div>{/if}
	</div>
	{#snippet actions()}
		<button class="btn-secondary" disabled={creating} onclick={() => (createOpen = false)}>Cancel</button>
		<button class="btn-primary" disabled={creating} onclick={create}>{creating ? 'Creating…' : 'Create'}</button>
	{/snippet}
</Modal>

<Modal open={deleteId !== null} title="Delete trip" onClose={() => (deleteId = null)}>
	<p>This deletes the trip with all its stops and photo references for good.</p>
	{#snippet actions()}
		<button class="btn-secondary" onclick={() => (deleteId = null)}>Cancel</button>
		<button class="btn-danger" disabled={busy} onclick={remove}>Delete</button>
	{/snippet}
</Modal>
