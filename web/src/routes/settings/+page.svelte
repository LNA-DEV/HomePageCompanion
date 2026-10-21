<script lang="ts">
	// Every configured instance, by building block: status and connections.
	import { onMount } from 'svelte';
	import { PageHeader, Loading } from '$lib/components';
	import { core, type InstanceView } from '$lib/shell/core';
	import { manifest } from '$lib/shell/session';
	import { toast } from '$lib/shell/toast';

	let instances = $state<InstanceView[]>([]);
	let loading = $state(true);
	let reloading = $state(false);

	const byBlock = $derived.by(() => {
		const m = new Map<string, InstanceView[]>();
		for (const i of instances) m.set(i.block, [...(m.get(i.block) ?? []), i]);
		return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
	});
	const name = (block: string) => $manifest?.modules.find((m) => m.id === block)?.name ?? block;

	async function load() {
		try {
			instances = await core.get<InstanceView[]>('instances');
		} catch (e) {
			toast(e instanceof Error ? e.message : String(e), 'error');
		}
		loading = false;
	}

	async function reload() {
		reloading = true;
		try {
			await core.post('reload');
			toast('companion.yaml read again', 'success');
			await load();
		} catch (e) {
			toast(e instanceof Error ? e.message : String(e), 'error');
		}
		reloading = false;
	}

	onMount(load);
</script>

<PageHeader title="Settings" description="The building blocks of companion.yaml and how they are connected">
	{#snippet actions()}
		<button class="btn-secondary btn-sm" onclick={reload} disabled={reloading}>Read companion.yaml again</button>
	{/snippet}
</PageHeader>

{#if loading}
	<Loading message="Loading instances..." />
{:else}
	<div class="space-y-6">
		{#each byBlock as [block, list]}
			<section class="card">
				<h2 class="text-lg font-semibold mb-3">{name(block)} <span class="text-sm font-normal text-gray-500">{block}</span></h2>
				<ul class="divide-y divide-gray-100 dark:divide-gray-800">
					{#each list as i}
						<li class="py-2 flex flex-wrap items-center gap-3">
							<span class="w-2.5 h-2.5 rounded-full {i.running ? 'bg-emerald-500' : 'bg-red-500'}" title={i.running ? 'running' : 'not running'}></span>
							<a class="font-medium text-primary-700 dark:text-primary-300 hover:underline" href="/settings/{encodeURIComponent(i.id)}">{i.name}</a>
							{#if i.provides?.length}
								<span class="text-xs text-gray-500">{i.provides.join(', ')}</span>
							{/if}
							{#each Object.entries(i.slots ?? {}) as [slot, ids]}
								<span class="text-xs text-gray-600 dark:text-gray-300">{slot} → {ids.join(', ')}</span>
							{/each}
							{#if i.error}<span class="text-xs text-red-600 dark:text-red-400">{i.error}</span>{/if}
						</li>
					{/each}
				</ul>
			</section>
		{/each}
	</div>
{/if}
