<svelte:options customElement={{ tag: 'webpush-subscribers', shadow: 'none' }} />

<script lang="ts">
	// Subscribers: the browsers that get the website's push notifications,
	// per audience ("default" is the website's).
	import { onMount } from 'svelte';
	import { formatDate } from '@companion/ui';
	import { PageHeader, Modal } from '@companion/ui/components';
	import { wp, audiences, type Audience } from './audiences';

	interface Subscriber {
		id: number;
		instance: string;
		endpoint: string;
		createdAt: string;
	}

	let groups = $state<Audience[]>([]);
	let subs = $state<Subscriber[] | null>(null);
	let audience = $state('');
	let error = $state('');
	let removing = $state<Subscriber | null>(null);
	let busy = $state(false);

	async function load() {
		try {
			[groups, subs] = await Promise.all([audiences(), wp.api.get<Subscriber[]>('subscribers', { instance: audience })]);
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	function host(endpoint: string): string {
		try {
			return new URL(endpoint).hostname;
		} catch {
			return endpoint.slice(0, 50);
		}
	}

	async function remove() {
		if (!removing) return;
		busy = true;
		try {
			await wp.api.del(`subscribers/${removing.id}`);
			wp.toast('Subscriber removed', 'success');
			removing = null;
			await load();
		} catch (e) {
			wp.toast(e instanceof Error ? e.message : String(e), 'error');
		}
		busy = false;
	}

	onMount(load);
</script>

<PageHeader title="Subscribers" description="Browsers that receive push notifications">
	{#snippet actions()}
		<a class="btn-primary" href={wp.href('broadcast')}>Broadcast</a>
	{/snippet}
</PageHeader>

{#if error}<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>{/if}

<div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
	{#each groups as g (g.name)}
		<button
			class="card text-left transition-colors {audience === g.name ? 'ring-2 ring-primary-500' : 'hover:bg-gray-50 dark:hover:bg-gray-800'}"
			onclick={() => {
				audience = audience === g.name ? '' : g.name;
				load();
			}}
		>
			<p class="text-sm text-gray-500 dark:text-gray-400">{g.name === 'default' ? 'Website' : g.name}</p>
			<p class="text-2xl font-semibold mt-1">{g.subscribers}</p>
		</button>
	{/each}
</div>

{#if !subs}
	<p class="text-gray-500">Loading…</p>
{:else}
	<div class="card p-0 overflow-x-auto">
		<table class="min-w-full text-sm" data-testid="subscribers">
			<thead>
				<tr class="border-b border-gray-200 dark:border-gray-800">
					<th class="table-header">Push service</th>
					<th class="table-header">Audience</th>
					<th class="table-header">Subscribed</th>
					<th class="table-header"></th>
				</tr>
			</thead>
			<tbody class="divide-y divide-gray-100 dark:divide-gray-800">
				{#each subs as s (s.id)}
					<tr>
						<td class="px-4 py-2" title={s.endpoint}>{host(s.endpoint)}</td>
						<td class="px-4 py-2">{s.instance}</td>
						<td class="px-4 py-2 whitespace-nowrap">{formatDate(s.createdAt)}</td>
						<td class="px-4 py-2 text-right"><button class="text-red-600 dark:text-red-400 hover:underline" onclick={() => (removing = s)}>Remove</button></td>
					</tr>
				{:else}
					<tr><td colspan="4" class="px-4 py-8 text-center text-gray-500">No subscribers{audience ? ' in this audience' : ''}.</td></tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<Modal open={removing !== null} title="Remove subscriber" onClose={() => (removing = null)}>
	<p>This browser gets no more notifications until it subscribes again.</p>
	{#snippet actions()}
		<button class="btn-secondary" onclick={() => (removing = null)}>Cancel</button>
		<button class="btn-danger" disabled={busy} onclick={remove}>Remove</button>
	{/snippet}
</Modal>
