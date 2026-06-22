<script lang="ts">
	import { onMount } from 'svelte';
	import type * as LT from 'leaflet';
	import 'leaflet/dist/leaflet.css';
	import { Search, LoaderCircle, ChevronUp, ChevronDown, X as XIcon } from 'lucide-svelte';
	import type { TripWaypoint } from '$lib/api';

	interface Props {
		// The leg runs from the previous stop (origin) to this stop (dest); both are
		// shown as fixed context pins with the route line drawn through the editable
		// via-points between them. A (0,0) endpoint is treated as "not set".
		originLat: number;
		originLng: number;
		destLat: number;
		destLng: number;
		waypoints: TripWaypoint[];
		// Called with the full ordered via-point list whenever it changes.
		onChange: (wps: TripWaypoint[]) => void;
	}

	let { originLat, originLng, destLat, destLng, waypoints, onChange }: Props = $props();

	// Local editable copy of the via-points. Seeded once in onMount from the
	// incoming prop (the component is remounted per leg, like LocationPicker).
	let points = $state<TripWaypoint[]>([]);

	let query = $state('');
	let searching = $state(false);
	let searchError = $state('');
	let results = $state<{ name: string; lat: number; lng: number }[]>([]);

	let mapEl: HTMLDivElement;
	// Leaflet touches `window`, so it's loaded in the browser only — untyped until
	// onMount resolves the dynamic import.
	let L: typeof LT;
	let map: LT.Map;
	let line: LT.Polyline | null = null;
	// Via-point markers are managed imperatively and rebuilt on any structural
	// change (add / remove / reorder) so their numbers stay in sync.
	let wpMarkers: LT.Marker[] = [];

	const originSet = () => originLat !== 0 || originLng !== 0;
	const destSet = () => destLat !== 0 || destLng !== 0;

	const wpIcon = (n: number) =>
		L.divIcon({
			className: '',
			html: `<span style="display:flex;align-items:center;justify-content:center;width:22px;height:22px;border-radius:50%;background:#D85A30;border:2px solid #fff;box-shadow:0 0 0 1px rgba(0,0,0,.25);color:#fff;font:600 11px/1 system-ui,sans-serif">${n}</span>`,
			iconSize: [22, 22],
			iconAnchor: [11, 11]
		});

	const endpointIcon = (color: string) =>
		L.divIcon({
			className: '',
			html: `<span style="display:block;width:16px;height:16px;border-radius:50%;background:${color};border:3px solid #fff;box-shadow:0 0 0 1px rgba(0,0,0,.25)"></span>`,
			iconSize: [16, 16],
			iconAnchor: [8, 8]
		});

	function emit() {
		onChange(points.map((p) => ({ lat: p.lat, lng: p.lng })));
	}

	function lineLatLngs(): [number, number][] {
		const arr: [number, number][] = [];
		if (originSet()) arr.push([originLat, originLng]);
		for (const p of points) arr.push([p.lat, p.lng]);
		if (destSet()) arr.push([destLat, destLng]);
		return arr;
	}

	function redrawLine() {
		const pts = lineLatLngs();
		if (!line) {
			line = L.polyline(pts, { color: '#D85A30', weight: 3, opacity: 0.85, dashArray: '6 6' }).addTo(map);
		} else {
			line.setLatLngs(pts);
		}
	}

	function rebuildWaypointMarkers() {
		for (const m of wpMarkers) m.remove();
		wpMarkers = [];
		points.forEach((_, idx) => {
			const m = L.marker([points[idx].lat, points[idx].lng], { draggable: true, icon: wpIcon(idx + 1) }).addTo(map);
			// Live-update the line while dragging; commit (emit) only on dragend.
			m.on('drag', () => {
				const ll = m.getLatLng();
				points[idx].lat = ll.lat;
				points[idx].lng = ll.lng;
				redrawLine();
			});
			m.on('dragend', () => {
				const ll = m.getLatLng();
				points[idx] = { lat: ll.lat, lng: ll.lng };
				redrawLine();
				emit();
			});
			wpMarkers.push(m);
		});
	}

	function addPoint(la: number, lo: number) {
		points.push({ lat: la, lng: lo });
		rebuildWaypointMarkers();
		redrawLine();
		emit();
	}

	function removePoint(idx: number) {
		points.splice(idx, 1);
		rebuildWaypointMarkers();
		redrawLine();
		emit();
	}

	function movePoint(idx: number, dir: -1 | 1) {
		const j = idx + dir;
		if (j < 0 || j >= points.length) return;
		const [p] = points.splice(idx, 1);
		points.splice(j, 0, p);
		rebuildWaypointMarkers();
		redrawLine();
		emit();
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

	// Picking a search result adds it as a via-point and recenters on it.
	function pickResult(r: { name: string; lat: number; lng: number }) {
		results = [];
		query = '';
		addPoint(r.lat, r.lng);
		map.setView([r.lat, r.lng], Math.max(map.getZoom(), 9));
	}

	onMount(() => {
		let cleanup = () => {};
		(async () => {
			const mod = await import('leaflet');
			// Leaflet ships as UMD; the namespace is on `default` under ESM interop.
			L = ((mod as unknown as { default?: typeof LT }).default ?? mod) as typeof LT;

			points = waypoints.map((w) => ({ lat: w.lat, lng: w.lng }));

			map = L.map(mapEl, { zoomControl: true }).setView([20, 0], 2);
			L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
				attribution: '&copy; OpenStreetMap contributors',
				maxZoom: 19
			}).addTo(map);

			// Fixed context pins for the leg's endpoints (not draggable).
			if (originSet()) {
				L.marker([originLat, originLng], { icon: endpointIcon('#2F855A'), interactive: false })
					.addTo(map)
					.bindTooltip('Start', { permanent: false });
			}
			if (destSet()) {
				L.marker([destLat, destLng], { icon: endpointIcon('#2B6CB0'), interactive: false })
					.addTo(map)
					.bindTooltip('End', { permanent: false });
			}

			rebuildWaypointMarkers();
			redrawLine();

			// Frame everything that's set; fall back to a world view when empty.
			const all = lineLatLngs();
			if (all.length >= 2) map.fitBounds(L.latLngBounds(all), { padding: [40, 40] });
			else if (all.length === 1) map.setView(all[0], 9);

			// Tap anywhere to append a via-point at the end of the route.
			map.on('click', (e: LT.LeafletMouseEvent) => addPoint(e.latlng.lat, e.latlng.lng));

			// The map lives inside a modal that may not have its final size yet;
			// nudge Leaflet to re-measure once laid out and on resize/rotate.
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
			placeholder="Search a place to add as a via-point"
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
		class="w-full h-[50vh] max-h-[420px] min-h-[260px] rounded-lg overflow-hidden border border-gray-200 dark:border-gray-700 z-0"
	></div>

	<div class="flex items-center justify-between gap-2 flex-wrap">
		<p class="text-sm text-gray-500 dark:text-gray-400">
			Tap the map (or search) to add via-points the route passes through. Drag a pin to adjust.
		</p>
		{#if points.length}
			<span class="text-sm font-medium text-gray-600 dark:text-gray-300">{points.length} via-point{points.length === 1 ? '' : 's'}</span>
		{/if}
	</div>

	{#if points.length}
		<ul class="border border-gray-200 dark:border-gray-700 rounded-lg divide-y divide-gray-100 dark:divide-gray-800 max-h-40 overflow-y-auto">
			{#each points as p, i (i)}
				<li class="flex items-center gap-2 px-3 py-1.5 text-sm">
					<span class="inline-flex items-center justify-center w-5 h-5 rounded-full bg-[#D85A30] text-white text-xs font-semibold shrink-0">{i + 1}</span>
					<span class="font-mono flex-1 truncate">{p.lat.toFixed(5)}, {p.lng.toFixed(5)}</span>
					<button type="button" onclick={() => movePoint(i, -1)} disabled={i === 0} class="p-1 disabled:opacity-30 hover:bg-gray-100 dark:hover:bg-gray-800 rounded" aria-label="Move via-point up"><ChevronUp size={14} /></button>
					<button type="button" onclick={() => movePoint(i, 1)} disabled={i === points.length - 1} class="p-1 disabled:opacity-30 hover:bg-gray-100 dark:hover:bg-gray-800 rounded" aria-label="Move via-point down"><ChevronDown size={14} /></button>
					<button type="button" onclick={() => removePoint(i)} class="p-1 text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/30 rounded" aria-label="Remove via-point"><XIcon size={14} /></button>
				</li>
			{/each}
		</ul>
	{/if}
</div>
