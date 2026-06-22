<script lang="ts">
	import { onMount } from 'svelte';
	import type * as LT from 'leaflet';
	import 'leaflet/dist/leaflet.css';
	import { Search, LoaderCircle } from 'lucide-svelte';

	interface Props {
		lat: number;
		lng: number;
		// Called with the picked coordinates whenever they change (tap, drag or
		// search result selection).
		onChange: (lat: number, lng: number) => void;
	}

	let { lat, lng, onChange }: Props = $props();

	// This component is remounted each time the picker opens (keyed by the
	// stop), so the incoming lat/lng are the initial values — read once in
	// onMount. A stop counts as "located" once it has a non-zero coordinate; a
	// freshly added stop is (0,0) → open at a world view with no marker yet.
	let selLat = $state(0);
	let selLng = $state(0);
	let placed = $state(false);

	let query = $state('');
	let searching = $state(false);
	let searchError = $state('');
	let results = $state<{ name: string; lat: number; lng: number }[]>([]);

	let mapEl: HTMLDivElement;
	// Leaflet is loaded only in the browser (it touches `window`), so these are
	// untyped until onMount resolves the dynamic import.
	let L: typeof LT;
	let map: LT.Map;
	let marker: LT.Marker | null = null;

	const pinIcon = () =>
		L.divIcon({
			className: '',
			html: '<span style="display:block;width:18px;height:18px;border-radius:50%;background:#D85A30;border:3px solid #fff;box-shadow:0 0 0 1px rgba(0,0,0,.25)"></span>',
			iconSize: [18, 18],
			iconAnchor: [9, 9]
		});

	function place(la: number, lo: number, recenter = false) {
		selLat = la;
		selLng = lo;
		placed = true;
		if (!marker) {
			marker = L.marker([la, lo], { draggable: true, icon: pinIcon() }).addTo(map);
			marker.on('dragend', () => {
				const p = marker!.getLatLng();
				selLat = p.lat;
				selLng = p.lng;
				onChange(p.lat, p.lng);
			});
		} else {
			marker.setLatLng([la, lo]);
		}
		if (recenter) map.setView([la, lo], Math.max(map.getZoom(), 10));
		onChange(la, lo);
	}

	async function runSearch(e: Event) {
		e.preventDefault();
		const q = query.trim();
		if (!q) return;
		searching = true;
		searchError = '';
		results = [];
		try {
			const res = await fetch(
				`https://nominatim.openstreetmap.org/search?format=jsonv2&limit=6&q=${encodeURIComponent(q)}`,
				{ headers: { Accept: 'application/json' } }
			);
			if (!res.ok) throw new Error('search failed');
			const data: { display_name: string; lat: string; lon: string }[] = await res.json();
			results = data.map((d) => ({
				name: d.display_name,
				lat: parseFloat(d.lat),
				lng: parseFloat(d.lon)
			}));
			if (results.length === 0) searchError = 'No matches found.';
		} catch {
			searchError = 'Search is unavailable right now — tap the map instead.';
		}
		searching = false;
	}

	function pickResult(r: { name: string; lat: number; lng: number }) {
		results = [];
		query = '';
		place(r.lat, r.lng, true);
	}

	onMount(() => {
		let cleanup = () => {};
		(async () => {
			const mod = await import('leaflet');
			// Leaflet ships as UMD; the namespace is on `default` under ESM interop.
			L = ((mod as unknown as { default?: typeof LT }).default ?? mod) as typeof LT;

			const initLat = lat;
			const initLng = lng;
			const hasInitial = initLat !== 0 || initLng !== 0;

			map = L.map(mapEl, { zoomControl: true }).setView(
				hasInitial ? [initLat, initLng] : [20, 0],
				hasInitial ? 11 : 2
			);
			L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
				attribution: '&copy; OpenStreetMap contributors',
				maxZoom: 19
			}).addTo(map);

			if (hasInitial) place(initLat, initLng);

			// Tap / click anywhere to drop or move the marker.
			map.on('click', (e: LT.LeafletMouseEvent) => place(e.latlng.lat, e.latlng.lng));

			// The map is created inside a modal that may not have its final size
			// yet; nudge Leaflet to re-measure once laid out and on resize/rotate.
			const fix = () => map.invalidateSize();
			const t = setTimeout(fix, 120);
			const ro = new ResizeObserver(fix);
			ro.observe(mapEl);
			window.addEventListener('resize', fix);

			cleanup = () => {
				clearTimeout(t);
				ro.disconnect();
				window.removeEventListener('resize', fix);
				map.remove();
			};
		})();
		return () => cleanup();
	});
</script>

<div class="space-y-3">
	<form onsubmit={runSearch} class="flex gap-2">
		<input
			bind:value={query}
			class="input flex-1"
			placeholder="Search a place (e.g. Florence, Italy)"
			autocomplete="off"
			enterkeyhint="search"
			type="search"
		/>
		<button type="submit" class="btn-secondary flex items-center gap-1" disabled={searching}>
			{#if searching}
				<LoaderCircle size={16} class="animate-spin" />
			{:else}
				<Search size={16} />
			{/if}
			<span class="hidden sm:inline">Search</span>
		</button>
	</form>

	{#if searchError}
		<p class="text-sm text-amber-700 dark:text-amber-400">{searchError}</p>
	{/if}

	{#if results.length}
		<ul class="border border-gray-200 dark:border-gray-700 rounded-lg divide-y divide-gray-100 dark:divide-gray-800 max-h-40 overflow-y-auto">
			{#each results as r}
				<li>
					<button
						type="button"
						onclick={() => pickResult(r)}
						class="w-full text-left px-3 py-2 text-sm hover:bg-gray-50 dark:hover:bg-gray-800"
					>
						{r.name}
					</button>
				</li>
			{/each}
		</ul>
	{/if}

	<div
		bind:this={mapEl}
		class="w-full h-[55vh] max-h-[420px] min-h-[260px] rounded-lg overflow-hidden border border-gray-200 dark:border-gray-700 z-0"
	></div>

	<p class="text-sm text-gray-500 dark:text-gray-400">
		{#if placed}
			Selected: <span class="font-mono">{selLat.toFixed(5)}, {selLng.toFixed(5)}</span> — tap the map or drag the pin to adjust.
		{:else}
			Tap the map or search above to set this stop's location.
		{/if}
	</p>
</div>
