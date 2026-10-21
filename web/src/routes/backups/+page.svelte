<script lang="ts">
	// Backups in the blob store under core/backups/: the whole state in one
	// file. Restoring is "companion restore <file>" with the companion stopped.
	import { onMount } from 'svelte';
	import { PageHeader, Loading } from '$lib/components';
	import { core } from '$lib/shell/core';
	import { toast } from '$lib/shell/toast';

	let names = $state<string[]>([]);
	let loading = $state(true);
	let creating = $state(false);

	async function load() {
		try {
			names = (await core.get<string[]>('backups')) ?? [];
		} catch (e) {
			toast(e instanceof Error ? e.message : String(e), 'error');
		}
		loading = false;
	}

	async function create() {
		creating = true;
		try {
			const r = await core.post<{ name?: string }>('backups');
			toast(`Backup ${r?.name ?? ''} written`, 'success');
			await load();
		} catch (e) {
			toast(e instanceof Error ? e.message : String(e), 'error');
		}
		creating = false;
	}

	onMount(load);
</script>

<PageHeader title="Backups" description="The database areas, the secret store, the blob files and the config in one file">
	{#snippet actions()}
		<button class="btn-primary btn-sm" onclick={create} disabled={creating}>{creating ? 'Writing…' : 'Back up now'}</button>
	{/snippet}
</PageHeader>

{#if loading}
	<Loading message="Loading backups..." />
{:else}
	<div class="card">
		{#if names.length === 0}
			<p class="text-gray-500 dark:text-gray-400">No backup yet. A schedule is set with <code>core.backups</code> in companion.yaml.</p>
		{:else}
			<ul class="divide-y divide-gray-100 dark:divide-gray-800">
				{#each names as n}
					<li class="py-2 flex items-center justify-between gap-3">
						<span class="font-mono text-sm">{n}</span>
						<a class="btn-secondary btn-sm" href={core.url(`backups/${encodeURIComponent(n)}`)} download={n}>Download</a>
					</li>
				{/each}
			</ul>
		{/if}
		<p class="mt-4 text-xs text-gray-500 dark:text-gray-400">
			To restore: stop the companion, then <code>companion restore &lt;file&gt;</code> (<code>--module id</code> for one building block).
		</p>
	</div>
{/if}
