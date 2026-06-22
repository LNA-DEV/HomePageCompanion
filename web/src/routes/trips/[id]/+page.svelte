<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import {
		api,
		type Trip,
		type TripStop,
		type TripPhoto,
		type TripWaypoint,
		type TripUpdate
	} from '$lib/api';
	import { PageHeader, Loading, Modal, LocationPicker, RoutePicker } from '$lib/components';
	import { COUNTRIES } from '$lib/countries';
	import {
		ArrowLeft,
		ChevronUp,
		ChevronDown,
		Trash2,
		Plus,
		ImagePlus,
		X as XIcon,
		ExternalLink,
		MapPin,
		Route
	} from 'lucide-svelte';

	let trip = $state<Trip | null>(null);
	let loading = $state(true);
	let error = $state('');
	let saving = $state(false);
	let saveMessage = $state('');
	let uploadBusy = $state<Record<string, boolean>>({});

	const id = $derived(Number($page.params.id));

	// Live preview of the derived header stats (the backend computes the
	// authoritative values the same way for the public payload).
	const stats = $derived.by(() => {
		const stops = trip?.stops ?? [];
		const countries = new Set<string>();
		let distance = 0;
		let hasDistance = false;
		let firstStart: number | null = null;
		for (const s of stops) {
			if (s.country.trim()) countries.add(s.country.trim());
			for (const c of s.transportCountries) if (c.trim()) countries.add(c.trim());
			if (s.distanceKm != null && !Number.isNaN(s.distanceKm)) {
				distance += s.distanceKm;
				hasDistance = true;
			}
			if (s.startDate) {
				const t = Date.parse(s.startDate + 'T00:00:00Z');
				if (!Number.isNaN(t) && (firstStart === null || t < firstStart)) firstStart = t;
			}
		}
		let daysElapsed: number | null = null;
		if (firstStart !== null) {
			const today = new Date();
			const todayUTC = Date.UTC(today.getFullYear(), today.getMonth(), today.getDate());
			daysElapsed = Math.floor((todayUTC - firstStart) / 86400000) + 1;
			if (daysElapsed < 0) daysElapsed = 0;
			const total = trip?.daysTotal ?? null;
			if (total != null && daysElapsed > total) daysElapsed = total;
		}
		return {
			daysElapsed,
			daysTotal: trip?.daysTotal ?? null,
			cities: stops.length,
			countries: countries.size,
			distanceKm: hasDistance ? distance : null
		};
	});

	onMount(async () => {
		try {
			trip = await api.getTrip(id);
		} catch {
			error = 'Failed to load trip';
		}
		loading = false;
	});

	function emptyStop(): TripStop {
		return {
			stopKey: '',
			name: '',
			startDate: '',
			endDate: '',
			lat: 0,
			lng: 0,
			status: 'upcoming',
			note: '',
			country: '',
			transportMode: '',
			transportLabel: '',
			transportDuration: '',
			distanceKm: null,
			transportCountries: [],
			transportWaypoints: [],
			photos: [],
			transportPhotos: []
		};
	}

	function addStop() {
		trip?.stops.push(emptyStop());
	}

	function removeStop(i: number) {
		trip?.stops.splice(i, 1);
	}

	function moveStop(i: number, dir: -1 | 1) {
		if (!trip) return;
		const j = i + dir;
		if (j < 0 || j >= trip.stops.length) return;
		const [s] = trip.stops.splice(i, 1);
		trip.stops.splice(j, 0, s);
	}

	function photosOf(stop: TripStop, kind: 'stop' | 'transport'): TripPhoto[] {
		return kind === 'stop' ? stop.photos : stop.transportPhotos;
	}

	async function handlePhotoUpload(e: Event, stop: TripStop, kind: 'stop' | 'transport') {
		const input = e.target as HTMLInputElement;
		if (!input.files || input.files.length === 0) return;
		const key = `${trip!.stops.indexOf(stop)}-${kind}`;
		uploadBusy[key] = true;
		try {
			const res = await api.uploadTripImage(input.files[0]);
			photosOf(stop, kind).push({ url: res.url, caption: '', alt: '', tint: '' });
		} catch {
			error = 'Image upload failed';
		}
		uploadBusy[key] = false;
		input.value = '';
	}

	function removePhoto(stop: TripStop, kind: 'stop' | 'transport', pi: number) {
		photosOf(stop, kind).splice(pi, 1);
	}

	function movePhoto(stop: TripStop, kind: 'stop' | 'transport', pi: number, dir: -1 | 1) {
		const arr = photosOf(stop, kind);
		const j = pi + dir;
		if (j < 0 || j >= arr.length) return;
		const [p] = arr.splice(pi, 1);
		arr.splice(j, 0, p);
	}

	function addTransportCountry(stop: TripStop) {
		stop.transportCountries.push('');
	}
	function removeTransportCountry(stop: TripStop, idx: number) {
		stop.transportCountries.splice(idx, 1);
	}

	function numOrNull(v: number | null | undefined): number | null {
		if (v === null || v === undefined || Number.isNaN(v)) return null;
		return v;
	}

	// Location picker (one shared modal map, opened per stop). Editing a draft
	// and only committing on "Use this location" avoids mutating the stop while
	// the user is still panning around.
	let pickerStop = $state<TripStop | null>(null);
	let draftLat = $state(0);
	let draftLng = $state(0);

	function openPicker(stop: TripStop) {
		draftLat = stop.lat;
		draftLng = stop.lng;
		pickerStop = stop;
	}
	function applyPicker() {
		if (pickerStop) {
			pickerStop.lat = draftLat;
			pickerStop.lng = draftLng;
		}
		pickerStop = null;
	}

	// Route (via-point) picker — one shared modal map per leg. Like the location
	// picker, edits a draft and only commits on "Use this route". routeOrigin is
	// the previous stop's coordinates, shown as the leg's start for context.
	let routeStop = $state<TripStop | null>(null);
	let routeOrigin = $state<{ lat: number; lng: number }>({ lat: 0, lng: 0 });
	let draftWaypoints = $state<TripWaypoint[]>([]);

	function openRoutePicker(stop: TripStop, index: number) {
		// Waypoints describe the leg into this stop, so the previous stop is the
		// leg's origin (shown as the route's start for context).
		const origin = index > 0 && trip ? trip.stops[index - 1] : undefined;
		routeOrigin = origin ? { lat: origin.lat, lng: origin.lng } : { lat: 0, lng: 0 };
		draftWaypoints = stop.transportWaypoints.map((w) => ({ ...w }));
		routeStop = stop;
	}
	function applyRoutePicker() {
		if (routeStop) routeStop.transportWaypoints = draftWaypoints;
		routeStop = null;
	}

	async function save() {
		if (!trip) return;
		error = '';
		saveMessage = '';
		if (!trip.title.trim()) {
			error = 'Title is required.';
			return;
		}
		saving = true;
		const payload: TripUpdate = {
			slug: trip.slug,
			title: trip.title,
			published: trip.published,
			daysTotal: numOrNull(trip.daysTotal),
			stops: trip.stops.map((s) => ({ ...s, distanceKm: numOrNull(s.distanceKm) }))
		};
		try {
			trip = await api.updateTrip(id, payload);
			saveMessage = 'Saved.';
		} catch (e) {
			error = e instanceof Error ? e.message : 'Save failed.';
		}
		saving = false;
	}
</script>

<PageHeader title={trip ? trip.title || 'Untitled trip' : 'Trip'} description="Edit stops, transport legs and photos. The website fetches the published result.">
	{#snippet actions()}
		<a href="/trips" class="btn-secondary btn-sm flex items-center gap-1"><ArrowLeft size={14} /> Back</a>
		<button onclick={save} class="btn-primary" disabled={saving || !trip}>
			{saving ? 'Saving…' : 'Save'}
		</button>
	{/snippet}
</PageHeader>

{#if error}
	<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>
{/if}
{#if saveMessage}
	<div class="mb-4 p-3 bg-emerald-50 dark:bg-emerald-900/30 text-emerald-700 dark:text-emerald-300 rounded-lg">{saveMessage}</div>
{/if}

{#if loading}
	<Loading message="Loading trip..." />
{:else if !trip}
	<div class="card text-center py-12"><p class="text-gray-500 dark:text-gray-400">Trip not found.</p></div>
{:else}
	<!-- Trip settings -->
	<div class="card space-y-4 mb-6">
		<div class="grid grid-cols-1 md:grid-cols-2 gap-4">
			<div>
				<label for="t-title" class="label">Title</label>
				<input id="t-title" type="text" bind:value={trip.title} class="input" />
			</div>
			<div>
				<label for="t-slug" class="label">Slug</label>
				<input id="t-slug" type="text" bind:value={trip.slug} class="input" />
			</div>
		</div>

		<div>
			<label for="t-dt" class="label">Days total (planned length)</label>
			<input id="t-dt" type="number" min="1" bind:value={trip.daysTotal} class="input md:w-48" placeholder="—" />
			<p class="text-xs text-gray-500 dark:text-gray-400 mt-1">The only manual stat — set it once you know how long the trip runs.</p>
		</div>

		<div>
			<span class="label">Auto-calculated stats (from the stops below)</span>
			<div class="grid grid-cols-2 md:grid-cols-4 gap-3">
				<div class="rounded-lg border border-gray-200 dark:border-gray-700 px-3 py-2">
					<div class="text-lg font-medium leading-tight">
						{stats.daysElapsed ?? '—'}{#if stats.daysTotal} / {stats.daysTotal}{/if}
					</div>
					<div class="text-xs text-gray-500 dark:text-gray-400">days</div>
				</div>
				<div class="rounded-lg border border-gray-200 dark:border-gray-700 px-3 py-2">
					<div class="text-lg font-medium leading-tight">{stats.cities}</div>
					<div class="text-xs text-gray-500 dark:text-gray-400">cities</div>
				</div>
				<div class="rounded-lg border border-gray-200 dark:border-gray-700 px-3 py-2">
					<div class="text-lg font-medium leading-tight">{stats.countries}</div>
					<div class="text-xs text-gray-500 dark:text-gray-400">countries</div>
				</div>
				<div class="rounded-lg border border-gray-200 dark:border-gray-700 px-3 py-2">
					<div class="text-lg font-medium leading-tight">{stats.distanceKm?.toLocaleString() ?? '—'}</div>
					<div class="text-xs text-gray-500 dark:text-gray-400">km</div>
				</div>
			</div>
		</div>

		<div class="flex items-center justify-between">
			<label class="flex items-center gap-2 cursor-pointer">
				<input type="checkbox" bind:checked={trip.published} class="w-4 h-4 rounded" />
				<span class="text-sm font-medium">Published (visible on the public API)</span>
			</label>
			{#if trip.published}
				<a
					href="/api/trips/{trip.slug}"
					target="_blank"
					rel="noopener noreferrer"
					class="text-sm text-primary-700 dark:text-primary-300 hover:underline flex items-center gap-1"
				>
					View public JSON <ExternalLink size={12} />
				</a>
			{/if}
		</div>
	</div>

	<!-- Stops -->
	<div class="flex items-center justify-between mb-3">
		<h2 class="text-lg font-medium">Stops</h2>
		<button onclick={addStop} class="btn-secondary btn-sm flex items-center gap-1"><Plus size={14} /> Add stop</button>
	</div>

	{#if trip.stops.length === 0}
		<div class="card text-center py-8 mb-6">
			<p class="text-gray-500 dark:text-gray-400">No stops yet — add the first city.</p>
		</div>
	{/if}

	<div class="space-y-4">
		{#each trip.stops as stop, i (stop)}
			<div class="card space-y-4">
				<div class="flex items-center gap-2">
					<span class="text-xs font-mono text-gray-400 dark:text-gray-500">#{i + 1}</span>
					<h3 class="font-medium truncate flex-1">{stop.name || 'New stop'}</h3>
					<button onclick={() => moveStop(i, -1)} disabled={i === 0} class="p-1 disabled:opacity-30 hover:bg-gray-100 dark:hover:bg-gray-800 rounded" aria-label="Move up"><ChevronUp size={16} /></button>
					<button onclick={() => moveStop(i, 1)} disabled={i === trip.stops.length - 1} class="p-1 disabled:opacity-30 hover:bg-gray-100 dark:hover:bg-gray-800 rounded" aria-label="Move down"><ChevronDown size={16} /></button>
					<button onclick={() => removeStop(i)} class="p-1 text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/30 rounded" aria-label="Remove stop"><Trash2 size={16} /></button>
				</div>

				<div class="grid grid-cols-1 md:grid-cols-2 gap-4">
					<div>
						<label class="label" for="s-name-{i}">Name</label>
						<input id="s-name-{i}" type="text" bind:value={stop.name} class="input" placeholder="London" />
					</div>
					<div>
						<label class="label" for="s-start-{i}">Arrival</label>
						<input id="s-start-{i}" type="date" bind:value={stop.startDate} class="input" />
					</div>
					<div>
						<label class="label" for="s-end-{i}">Departure (optional)</label>
						<input id="s-end-{i}" type="date" bind:value={stop.endDate} min={stop.startDate || undefined} class="input" />
						<p class="text-xs text-gray-500 dark:text-gray-400 mt-1">Leave empty for an open-ended stay (e.g. the current stop).</p>
					</div>
					<div>
						<label class="label" for="s-status-{i}">Status</label>
						<select id="s-status-{i}" bind:value={stop.status} class="input">
							<option value="visited">Visited</option>
							<option value="current">Current (here now)</option>
							<option value="upcoming">Upcoming</option>
						</select>
					</div>
					<div>
						<label class="label" for="s-country-{i}">Country</label>
						<select
							id="s-country-{i}"
							bind:value={stop.country}
							class="input"
							class:border-amber-400={!stop.country}
						>
							<option value="" disabled>Select a country…</option>
							{#each COUNTRIES as country}
								<option value={country}>{country}</option>
							{/each}
						</select>
					</div>
					<div class="md:col-span-2">
						<span class="label">Location</span>
						<div class="flex items-center gap-3 flex-wrap">
							<button type="button" onclick={() => openPicker(stop)} class="btn-secondary btn-sm flex items-center gap-1">
								<MapPin size={14} />
								{stop.lat || stop.lng ? 'Change location' : 'Set on map'}
							</button>
							{#if stop.lat || stop.lng}
								<span class="text-sm text-gray-500 dark:text-gray-400 font-mono">{stop.lat.toFixed(5)}, {stop.lng.toFixed(5)}</span>
							{:else}
								<span class="text-sm text-gray-400 dark:text-gray-500">No location set</span>
							{/if}
						</div>
					</div>
				</div>

				<div>
					<label class="label" for="s-note-{i}">Note</label>
					<textarea id="s-note-{i}" bind:value={stop.note} class="input min-h-[70px]" placeholder="A short note about this stop"></textarea>
				</div>

				<!-- Stop photos -->
				<div>
					<span class="label">Photos</span>
					{@render photoSection(stop, 'stop', stop.photos)}
				</div>

				<!-- Transport leg -->
				<div class="border-t border-gray-200 dark:border-gray-800 pt-4 space-y-4">
					<p class="text-sm font-medium text-gray-600 dark:text-gray-300">Transport into this stop</p>
					<div class="grid grid-cols-1 md:grid-cols-4 gap-4">
						<div>
							<label class="label" for="s-mode-{i}">Mode</label>
							<select id="s-mode-{i}" bind:value={stop.transportMode} class="input">
								<option value="">None (first stop)</option>
								<option value="train">Train</option>
								<option value="flight">Flight</option>
								<option value="car">Car</option>
							</select>
						</div>
						<div>
							<label class="label" for="s-tlabel-{i}">Label</label>
							<input id="s-tlabel-{i}" type="text" bind:value={stop.transportLabel} class="input" placeholder="Eurostar" disabled={!stop.transportMode} />
						</div>
						<div>
							<label class="label" for="s-tdur-{i}">Duration</label>
							<input id="s-tdur-{i}" type="text" bind:value={stop.transportDuration} class="input" placeholder="2h20" disabled={!stop.transportMode} />
						</div>
						<div>
							<label class="label" for="s-tdist-{i}">Distance (km)</label>
							<input id="s-tdist-{i}" type="number" min="0" bind:value={stop.distanceKm} class="input" placeholder="492" disabled={!stop.transportMode} />
						</div>
					</div>
					{#if stop.transportMode}
						<div>
							<span class="label">Countries this leg passes through (optional)</span>
							<div class="space-y-2">
								{#each stop.transportCountries as _, ci (ci)}
									<div class="flex items-center gap-2">
										<select bind:value={stop.transportCountries[ci]} class="input flex-1">
											<option value="" disabled>Select a country…</option>
											{#each COUNTRIES as country}
												<option value={country}>{country}</option>
											{/each}
										</select>
										<button
											type="button"
											onclick={() => removeTransportCountry(stop, ci)}
											class="p-2 text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/30 rounded"
											aria-label="Remove country"
										><XIcon size={16} /></button>
									</div>
								{/each}
								<button type="button" onclick={() => addTransportCountry(stop)} class="btn-secondary btn-sm flex items-center gap-1">
									<Plus size={14} /> Add country
								</button>
							</div>
						</div>
						<div>
							<span class="label">Route (via-points the trail passes through)</span>
							<div class="flex items-center gap-3 flex-wrap">
								<button type="button" onclick={() => openRoutePicker(stop, i)} class="btn-secondary btn-sm flex items-center gap-1">
									<Route size={14} />
									{stop.transportWaypoints.length ? 'Edit route' : 'Add route'}
								</button>
								{#if stop.transportWaypoints.length}
									<span class="text-sm text-gray-500 dark:text-gray-400">{stop.transportWaypoints.length} via-point{stop.transportWaypoints.length === 1 ? '' : 's'}</span>
								{:else}
									<span class="text-sm text-gray-400 dark:text-gray-500">Straight line (no via-points)</span>
								{/if}
							</div>
							<p class="text-xs text-gray-500 dark:text-gray-400 mt-1">Optional — only needed to correct the drawn trail (e.g. a winding car route).</p>
						</div>
						<div>
							<span class="label">Transport photos</span>
							{@render photoSection(stop, 'transport', stop.transportPhotos)}
						</div>
					{/if}
				</div>
			</div>
		{/each}
	</div>

	<div class="flex justify-end mt-6">
		<button onclick={save} class="btn-primary" disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
	</div>
{/if}

{#if pickerStop}
	<Modal open={true} title="Pick location" size="xl" onClose={() => (pickerStop = null)}>
		<LocationPicker
			lat={draftLat}
			lng={draftLng}
			onChange={(la, lo) => {
				draftLat = la;
				draftLng = lo;
			}}
		/>
		{#snippet actions()}
			<button onclick={() => (pickerStop = null)} class="btn-secondary">Cancel</button>
			<button onclick={applyPicker} class="btn-primary">Use this location</button>
		{/snippet}
	</Modal>
{/if}

{#if routeStop}
	<Modal open={true} title="Edit route" size="xl" onClose={() => (routeStop = null)}>
		<RoutePicker
			originLat={routeOrigin.lat}
			originLng={routeOrigin.lng}
			destLat={routeStop.lat}
			destLng={routeStop.lng}
			waypoints={draftWaypoints}
			onChange={(wps) => (draftWaypoints = wps)}
		/>
		{#snippet actions()}
			<button onclick={() => (routeStop = null)} class="btn-secondary">Cancel</button>
			<button onclick={applyRoutePicker} class="btn-primary">Use this route</button>
		{/snippet}
	</Modal>
{/if}

{#snippet photoSection(stop: TripStop, kind: 'stop' | 'transport', photos: TripPhoto[])}
	<div class="flex flex-wrap gap-3">
		{#each photos as photo, pi (photo)}
			<div class="border border-gray-200 dark:border-gray-700 rounded-lg p-2 w-44 space-y-2">
				<div class="relative">
					{#if photo.url}
						<img src={photo.url} alt={photo.alt || ''} class="w-full h-24 object-cover rounded" />
					{:else}
						<div class="w-full h-24 rounded flex items-center justify-center text-xs text-gray-500" style="background:{photo.tint || '#eee'}">
							{photo.caption || 'no image'}
						</div>
					{/if}
					<button
						onclick={() => removePhoto(stop, kind, pi)}
						class="absolute top-1 right-1 bg-white/80 dark:bg-gray-900/80 rounded-full p-0.5 text-red-600 dark:text-red-400"
						aria-label="Remove photo"
					><XIcon size={14} /></button>
				</div>
				<input bind:value={photo.caption} class="input !py-1 text-xs" placeholder="Caption" />
				<input bind:value={photo.alt} class="input !py-1 text-xs" placeholder="Alt text" />
				<input bind:value={photo.tint} class="input !py-1 text-xs" placeholder="Tint #hex (fallback)" />
				<div class="flex justify-between">
					<button onclick={() => movePhoto(stop, kind, pi, -1)} disabled={pi === 0} class="p-1 disabled:opacity-30" aria-label="Move photo left"><ChevronUp size={14} class="-rotate-90" /></button>
					<button onclick={() => movePhoto(stop, kind, pi, 1)} disabled={pi === photos.length - 1} class="p-1 disabled:opacity-30" aria-label="Move photo right"><ChevronDown size={14} class="-rotate-90" /></button>
				</div>
			</div>
		{/each}

		<label class="border-2 border-dashed border-gray-300 dark:border-gray-700 rounded-lg w-44 h-[8.5rem] flex flex-col items-center justify-center gap-1 cursor-pointer text-sm text-gray-500 hover:bg-gray-50 dark:hover:bg-gray-800">
			<ImagePlus size={18} />
			{uploadBusy[`${trip ? trip.stops.indexOf(stop) : -1}-${kind}`] ? 'Uploading…' : 'Add photo'}
			<input type="file" accept="image/*" class="hidden" onchange={(e) => handlePhotoUpload(e, stop, kind)} />
		</label>
	</div>
{/snippet}
