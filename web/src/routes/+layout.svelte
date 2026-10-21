<script lang="ts">
	import '../app.css';
	import { onMount, type ComponentType } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { LayoutDashboard, Menu, Settings, ListChecks, ScrollText, DatabaseBackup } from 'lucide-svelte';
	import { Sidebar, Toasts } from '$lib/components';
	import type { NavGroup } from '$lib/components/Sidebar.svelte';
	import { authenticated, checkSession, loadManifest, logout, manifest } from '$lib/shell/session';
	import { installBridge } from '$lib/shell/bridge';
	import { icon } from '$lib/shell/icons';
	import { installClientLogger } from '$lib/clientLogger';

	let { children } = $props();
	let sidebarOpen = $state(false);

	// The sidebar: the shell's own entries, then every building block's nav
	// entries, grouped and ordered as they declare.
	const groups = $derived.by((): NavGroup[] => {
		const top: NavGroup = { label: '', items: [{ href: '/', label: 'Dashboard', icon: LayoutDashboard }] };
		const byGroup = new Map<string, { order: number; items: { href: string; label: string; icon: ComponentType; order: number }[] }>();
		for (const m of $manifest?.modules ?? []) {
			for (const n of m.nav ?? []) {
				const g = n.group || m.name;
				const entry = byGroup.get(g) ?? { order: n.order ?? 100, items: [] };
				entry.order = Math.min(entry.order, n.order ?? 100);
				entry.items.push({ href: `/b/${m.id}${n.page ? '/' + n.page : ''}`, label: n.label, icon: icon(n.icon), order: n.order ?? 100 });
				byGroup.set(g, entry);
			}
		}
		const modules = [...byGroup.entries()]
			.sort((a, b) => a[1].order - b[1].order || a[0].localeCompare(b[0]))
			.map(([label, g]) => ({ label, items: g.items.sort((a, b) => a.order - b.order) }));
		const system: NavGroup = {
			label: 'System',
			items: [
				{ href: '/settings', label: 'Settings', icon: Settings },
				{ href: '/jobs', label: 'Jobs', icon: ListChecks },
				{ href: '/logs', label: 'Logs', icon: ScrollText },
				{ href: '/backups', label: 'Backups', icon: DatabaseBackup }
			]
		};
		return [top, ...modules, system];
	});

	onMount(async () => {
		installBridge();
		if (await checkSession()) {
			installClientLogger();
			await loadManifest().catch(() => undefined);
		}
	});

	$effect(() => {
		if ($authenticated === false && $page.url.pathname !== '/login') goto('/login');
	});

	async function handleLogout() {
		await logout();
		goto('/login');
	}
</script>

<Toasts />
{#if $authenticated === null}
	<div class="min-h-screen flex items-center justify-center bg-gray-100 dark:bg-gray-950">
		<div class="w-8 h-8 border-4 border-primary-200 dark:border-primary-900 border-t-primary-600 rounded-full animate-spin"></div>
	</div>
{:else if $authenticated && $page.url.pathname !== '/login'}
	<div class="min-h-screen flex bg-gray-100 dark:bg-gray-950">
		<Sidebar {groups} version={$manifest?.version} onLogout={handleLogout} open={sidebarOpen} onClose={() => (sidebarOpen = false)} />
		<div class="flex-1 min-w-0 flex flex-col">
			<header
				class="md:hidden sticky top-0 z-20 flex items-center gap-3 bg-white dark:bg-gray-900 border-b border-gray-200 dark:border-gray-800 px-4 py-3 shadow-sm"
			>
				<button
					type="button"
					onclick={() => (sidebarOpen = true)}
					class="p-2 -ml-2 text-gray-700 dark:text-gray-200 hover:bg-gray-100 dark:hover:bg-gray-800 rounded-lg"
					aria-label="Open menu"
				>
					<Menu size={22} />
				</button>
				<h1 class="text-base font-semibold text-primary-700 dark:text-primary-300 truncate">HomePageCompanion</h1>
			</header>
			<main class="flex-1 p-4 sm:p-8 overflow-x-hidden">
				{@render children()}
			</main>
		</div>
	</div>
{:else if $page.url.pathname === '/login'}
	{@render children()}
{/if}
