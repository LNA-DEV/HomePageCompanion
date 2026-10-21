<svelte:options customElement={{ tag: 'webmention-page', shadow: 'none' }} />

<script lang="ts">
	// Webmentions: who linked to which page. With an rss module the target
	// shows the post's title.
	import { onMount } from 'svelte';
	import { connect, formatDate, safeHref } from '@companion/ui';
	import { PageHeader, Modal } from '@companion/ui/components';

	interface Mention {
		ID: number;
		Source: string;
		Target: string;
		CreatedAt: string;
	}

	const wm = connect('webmention');
	let mentions = $state<Mention[] | null>(null);
	let titles = $state<Record<string, { title: string; image?: { url: string } }>>({});
	let q = $state('');
	let error = $state('');
	let removing = $state<Mention | null>(null);
	let timer: ReturnType<typeof setTimeout> | null = null;

	const sources = $derived(new Set((mentions ?? []).map((m) => host(m.Source))).size);
	const pages = $derived(new Set((mentions ?? []).map((m) => m.Target)).size);

	function host(u: string): string {
		try {
			return new URL(u).hostname;
		} catch {
			return u;
		}
	}

	async function load() {
		try {
			mentions = await wm.api.get<Mention[]>('mentions', { q });
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			return;
		}
		const links = [...new Set(mentions.map((m) => m.Target))].filter((l) => !(l in titles));
		if (links.length) {
			// Optional: without an rss module the URLs stay as they are.
			connect('rss')
				.api.post<typeof titles>('items/lookup', { links })
				.then((r) => (titles = { ...titles, ...r }))
				.catch(() => {});
		}
	}

	function typed() {
		if (timer) clearTimeout(timer);
		timer = setTimeout(load, 300);
	}

	async function remove() {
		if (!removing) return;
		try {
			await wm.api.del(`mentions/${removing.ID}`);
			wm.toast('Webmention deleted', 'success');
			removing = null;
			await load();
		} catch (e) {
			wm.toast(e instanceof Error ? e.message : String(e), 'error');
		}
	}

	onMount(load);
</script>

<PageHeader title="Webmentions" description="Other sites that link to yours" />

{#if error}<div class="mb-4 p-3 bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-300 rounded-lg">{error}</div>{/if}

<div class="grid grid-cols-3 gap-4 mb-6">
	<div class="card"><p class="text-sm text-gray-500 dark:text-gray-400">Mentions</p><p class="text-2xl font-semibold mt-1">{mentions?.length ?? '–'}</p></div>
	<div class="card"><p class="text-sm text-gray-500 dark:text-gray-400">Sites</p><p class="text-2xl font-semibold mt-1">{mentions ? sources : '–'}</p></div>
	<div class="card"><p class="text-sm text-gray-500 dark:text-gray-400">Your pages</p><p class="text-2xl font-semibold mt-1">{mentions ? pages : '–'}</p></div>
</div>

<div class="card mb-4">
	<input class="input" type="search" placeholder="Search source or target…" bind:value={q} oninput={typed} aria-label="Search webmentions" />
</div>

{#if mentions}
	<div class="card p-0 overflow-x-auto">
		<table class="min-w-full text-sm" data-testid="webmentions">
			<thead>
				<tr class="border-b border-gray-200 dark:border-gray-800">
					<th class="table-header">From</th>
					<th class="table-header">To</th>
					<th class="table-header">Received</th>
					<th class="table-header"></th>
				</tr>
			</thead>
			<tbody class="divide-y divide-gray-100 dark:divide-gray-800">
				{#each mentions as m (m.ID)}
					{@const t = titles[m.Target]}
					<tr>
						<td class="px-4 py-2 max-w-xs"><a class="text-primary-700 dark:text-primary-300 hover:underline truncate block" href={safeHref(m.Source)} target="_blank" rel="noopener" title={m.Source}>{host(m.Source)}</a></td>
						<td class="px-4 py-2 max-w-sm">
							<a class="flex items-center gap-2 hover:underline" href={safeHref(m.Target)} target="_blank" rel="noopener" title={m.Target}>
								{#if t?.image}<img src={t.image.url} alt="" class="w-8 h-8 rounded object-cover shrink-0" loading="lazy" />{/if}
								<span class="truncate">{t?.title || m.Target}</span>
							</a>
						</td>
						<td class="px-4 py-2 whitespace-nowrap text-gray-500">{formatDate(m.CreatedAt)}</td>
						<td class="px-4 py-2 text-right"><button class="text-red-600 dark:text-red-400 hover:underline" onclick={() => (removing = m)}>Delete</button></td>
					</tr>
				{:else}
					<tr><td colspan="4" class="px-4 py-8 text-center text-gray-500">{q ? 'No webmentions match.' : 'No webmentions yet.'}</td></tr>
				{/each}
			</tbody>
		</table>
	</div>
{:else if !error}
	<p class="text-gray-500">Loading…</p>
{/if}

<Modal open={removing !== null} title="Delete webmention" onClose={() => (removing = null)}>
	<p class="break-all">{removing?.Source} → {removing?.Target}</p>
	{#snippet actions()}
		<button class="btn-secondary" onclick={() => (removing = null)}>Cancel</button>
		<button class="btn-danger" onclick={remove}>Delete</button>
	{/snippet}
</Modal>
