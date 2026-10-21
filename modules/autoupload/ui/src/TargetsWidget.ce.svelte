<svelte:options customElement={{ tag: 'autoupload-targets', shadow: 'none' }} />

<script lang="ts">
	// Targets: is every network healthy, how much went out, when last
	// (autoposting-ui.md §4.5). Only account problems mark a target down.
	import { onMount } from 'svelte';
	import { timeAgo } from '@companion/ui';
	import { au, dashboard, targetName } from './data';
	import type { Dashboard } from './types';

	let d = $state<Dashboard | null>(null);
	let error = $state('');

	onMount(() => {
		dashboard().then((r) => (d = r), (e) => (error = e instanceof Error ? e.message : String(e)));
	});

	const dot = { ok: 'bg-emerald-500', account: 'bg-red-500', paused: 'bg-amber-500' };
</script>

{#if error}
	<p class="text-sm text-red-700 dark:text-red-300">{error}</p>
{:else if !d}
	<p class="text-sm text-gray-500">Loading…</p>
{:else if !d.targets?.length}
	<p class="text-sm text-gray-500">No target yet.</p>
{:else}
	<ul class="divide-y divide-gray-100 dark:divide-gray-800 text-sm" data-testid="targets-widget">
		{#each d.targets as t (t.id)}
			<li class="py-2 flex items-center gap-3">
				<span class="w-2.5 h-2.5 rounded-full shrink-0 {dot[t.status]}" title={t.status}></span>
				<div class="min-w-0 flex-1">
					<p class="font-medium">{targetName(t.id)}</p>
					{#if t.error}<p class="text-xs text-red-700 dark:text-red-300 truncate" title={t.error}>{t.status === 'paused' ? 'paused: ' : ''}{t.error}</p>{/if}
				</div>
				<div class="text-right text-xs text-gray-500 shrink-0">
					<p>{t.published} posted</p>
					{#if t.lastPublished}<p>last {timeAgo(t.lastPublished)}</p>{/if}
				</div>
			</li>
		{/each}
	</ul>
	<a class="mt-2 inline-block text-sm text-primary-700 dark:text-primary-300 hover:underline" href={au.href('schedule')}>Schedule →</a>
{/if}
