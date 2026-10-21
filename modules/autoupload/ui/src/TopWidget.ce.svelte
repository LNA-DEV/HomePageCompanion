<svelte:options customElement={{ tag: 'autoupload-top', shadow: 'none' }} />

<script lang="ts">
	// Top posts: the five with the most platform likes (autoposting-ui.md §4.5).
	import { onMount } from 'svelte';
	import { au, dashboard } from './data';
	import type { Dashboard } from './types';

	let d = $state<Dashboard | null>(null);
	let error = $state('');

	onMount(() => {
		dashboard().then((r) => (d = r), (e) => (error = e instanceof Error ? e.message : String(e)));
	});
</script>

{#if error}
	<p class="text-sm text-red-700 dark:text-red-300">{error}</p>
{:else if !d}
	<p class="text-sm text-gray-500">Loading…</p>
{:else if !d.top?.length}
	<p class="text-sm text-gray-500">No likes yet.</p>
{:else}
	<ol class="space-y-2 text-sm" data-testid="top-widget">
		{#each d.top as p, i (p.itemId)}
			<li class="flex items-center gap-3">
				<span class="w-4 text-gray-400">{i + 1}</span>
				{#if p.imageUrl}<img src={p.imageUrl} alt="" class="w-9 h-9 rounded object-cover shrink-0" loading="lazy" />{/if}
				<span class="flex-1 truncate">{p.title || p.itemId}</span>
				<span class="font-medium shrink-0">{p.likes}</span>
				{#if p.websiteLikes}<span class="text-xs text-gray-500 shrink-0">+{p.websiteLikes}</span>{/if}
			</li>
		{/each}
	</ol>
	<a class="mt-2 inline-block text-sm text-primary-700 dark:text-primary-300 hover:underline" href={au.href('') + '?sort=likes'}>All posts →</a>
{/if}
