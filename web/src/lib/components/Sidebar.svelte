<script lang="ts">
	import { page } from '$app/stores';
	import type { ComponentType } from 'svelte';
	import { LogOut, Sun, Moon } from 'lucide-svelte';
	import { theme, toggleTheme } from '$lib/stores/theme';

	export interface NavItem {
		href: string;
		label: string;
		icon: ComponentType;
	}

	export interface NavGroup {
		label: string;
		items: NavItem[];
	}

	interface Props {
		groups: NavGroup[];
		version?: string;
		onLogout: () => void;
		open?: boolean;
		onClose?: () => void;
	}

	let { groups, version = '', onLogout, open = false, onClose }: Props = $props();

	function matches(href: string, p: string): boolean {
		return href === '/' ? p === '/' : p === href || p.startsWith(href + '/');
	}

	// Only the longest matching entry is active: a module's first page
	// (/b/autoupload) must not light up on its others (/b/autoupload/schedule).
	const activeHref = $derived.by(() => {
		const p = $page.url.pathname;
		let best = '';
		for (const g of groups) for (const i of g.items) if (matches(i.href, p) && i.href.length > best.length) best = i.href;
		return best;
	});

	function isActive(href: string): boolean {
		return href === activeHref;
	}
</script>

{#if open && onClose}
	<button type="button" class="fixed inset-0 bg-black/40 z-30 md:hidden" onclick={onClose} aria-label="Close menu"></button>
{/if}

<aside
	class="bg-white dark:bg-gray-900 shadow-md w-64 flex flex-col border-r border-gray-200 dark:border-gray-800
		fixed inset-y-0 left-0 z-40 transform transition-transform
		md:sticky md:top-0 md:h-screen md:translate-x-0
		{open ? 'translate-x-0' : '-translate-x-full md:translate-x-0'}"
>
	<div class="px-6 py-4 border-b border-gray-200 dark:border-gray-800">
		<h1 class="text-lg font-bold text-primary-600 dark:text-primary-400">HomePageCompanion</h1>
		<p class="text-xs text-gray-500 dark:text-gray-400">Admin{version ? ` · ${version}` : ''}</p>
	</div>

	<nav class="flex-1 px-3 py-2 overflow-y-auto" aria-label="Main">
		{#each groups as group}
			{#if group.label}
				<p class="px-3 pt-3 pb-1 text-[11px] font-semibold uppercase tracking-wider text-gray-400 dark:text-gray-500">{group.label}</p>
			{/if}
			{#each group.items as item}
				{@const Icon = item.icon}
				<a
					href={item.href}
					onclick={() => onClose?.()}
					class="flex items-center gap-3 px-3 py-1.5 rounded-lg text-sm transition-colors
						{isActive(item.href)
						? 'bg-primary-50 text-primary-700 font-medium dark:bg-primary-900/30 dark:text-primary-300'
						: 'text-gray-600 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-gray-800'}"
				>
					<Icon size={18} />
					<span>{item.label}</span>
				</a>
			{/each}
		{/each}
	</nav>

	<div class="px-3 py-2 border-t border-gray-200 dark:border-gray-800 flex gap-1">
		<button
			onclick={toggleTheme}
			class="flex-1 px-3 py-2 text-sm text-gray-600 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-gray-800 rounded-lg transition-colors flex items-center gap-2"
		>
			{#if $theme === 'dark'}
				<Sun size={16} /><span>Light</span>
			{:else}
				<Moon size={16} /><span>Dark</span>
			{/if}
		</button>
		<button
			onclick={onLogout}
			class="flex-1 px-3 py-2 text-sm text-gray-600 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-gray-800 rounded-lg transition-colors flex items-center gap-2"
		>
			<LogOut size={16} /><span>Logout</span>
		</button>
	</div>
</aside>
