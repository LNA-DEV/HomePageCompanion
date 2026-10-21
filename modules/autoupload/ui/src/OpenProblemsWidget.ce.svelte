<svelte:options customElement={{ tag: 'autoupload-open-problems', shadow: 'none' }} />

<script lang="ts">
	// Open problems by cause; a click leads to the Problems page
	// (autoposting-ui.md §4.5).
	import { onMount } from 'svelte';
	import { au, dashboard } from './data';
	import { causeLabel, type Dashboard } from './types';

	let d = $state<Dashboard | null>(null);
	let error = $state('');

	onMount(() => {
		dashboard().then((r) => (d = r), (e) => (error = e instanceof Error ? e.message : String(e)));
	});

	const total = $derived(d ? Object.values(d.problems ?? {}).reduce((a, b) => a + b, 0) : 0);
	const color: Record<string, string> = {
		account: 'text-red-700 dark:text-red-300',
		content: 'text-amber-700 dark:text-amber-300',
		transient: 'text-gray-600 dark:text-gray-300'
	};
</script>

{#if error}
	<p class="text-sm text-red-700 dark:text-red-300">{error}</p>
{:else if !d}
	<p class="text-sm text-gray-500">Loading…</p>
{:else}
	<a href={au.href('problems')} class="block" data-testid="problems-widget">
		<p class="text-3xl font-semibold {total ? 'text-red-700 dark:text-red-300' : 'text-emerald-700 dark:text-emerald-300'}">{total}</p>
		{#if total}
			<ul class="mt-2 text-sm space-y-0.5">
				{#each ['account', 'content', 'transient'] as c}
					{#if d.problems?.[c]}<li class={color[c]}>{d.problems[c]} {causeLabel[c].toLowerCase()}</li>{/if}
				{/each}
			</ul>
		{:else}
			<p class="mt-1 text-sm text-gray-500">nothing open</p>
		{/if}
	</a>
{/if}
