<script lang="ts">
	import type { Cell } from './types';
	import { short } from './data';

	let { cell }: { cell: Cell | undefined } = $props();
</script>

{#if !cell || cell.state === 'none'}
	<span class="text-gray-300 dark:text-gray-700" title="not connected">·</span>
{:else if cell.state === 'published'}
	<span class="inline-flex items-center gap-1 text-emerald-700 dark:text-emerald-400" title="published {short(cell.time)}">
		<span class="w-2 h-2 rounded-full bg-emerald-500"></span>
		{#if cell.url}
			<a href={cell.url} target="_blank" rel="noopener" class="hover:underline" onclick={(e) => e.stopPropagation()}>{cell.likes ?? 0}</a>
		{:else}
			{cell.likes ?? 0}
		{/if}
	</span>
{:else if cell.state === 'failed'}
	<span class="inline-flex items-center gap-1 text-red-700 dark:text-red-400" title="failed: {cell.reason} ({cell.cause})">✕ {cell.reason}</span>
{:else if cell.state === 'next'}
	<span class="inline-flex items-center gap-1 text-primary-700 dark:text-primary-300" title="scheduled next">◔ {short(cell.time)}</span>
{:else if cell.state === 'skipped'}
	<span class="inline-flex items-center gap-1 text-gray-500 dark:text-gray-400" title="skipped: {cell.reason}">○ {cell.reason?.replace('skipped by ', '').slice(0, 12)}</span>
{:else}
	<span class="text-gray-400 dark:text-gray-500" title="open">–</span>
{/if}
