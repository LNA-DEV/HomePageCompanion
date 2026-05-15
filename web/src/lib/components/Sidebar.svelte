<script lang="ts">
	import { page } from '$app/stores';
	import type { ComponentType } from 'svelte';
	import { LogOut, Sun, Moon } from 'lucide-svelte';
	import { theme, toggleTheme } from '$lib/stores/theme';

	interface NavItem {
		href: string;
		label: string;
		icon: ComponentType;
	}

	interface Props {
		navItems: NavItem[];
		onLogout: () => void;
		open?: boolean;
		onClose?: () => void;
	}

	let { navItems, onLogout, open = false, onClose }: Props = $props();

	function isActive(href: string): boolean {
		return $page.url.pathname.startsWith(href);
	}

	function handleNavClick() {
		onClose?.();
	}
</script>

{#if open && onClose}
	<button
		type="button"
		class="fixed inset-0 bg-black/40 z-30 md:hidden"
		onclick={onClose}
		aria-label="Close menu"
	></button>
{/if}

<aside
	class="bg-white dark:bg-gray-900 shadow-md w-64 flex flex-col border-r border-gray-200 dark:border-gray-800
		fixed inset-y-0 left-0 z-40 transform transition-transform
		md:sticky md:top-0 md:h-screen md:translate-x-0
		{open ? 'translate-x-0' : '-translate-x-full md:translate-x-0'}"
>
	<div class="p-6 border-b border-gray-200 dark:border-gray-800">
		<h1 class="text-xl font-bold text-primary-600 dark:text-primary-400">HomePageCompanion</h1>
		<p class="text-sm text-gray-500 dark:text-gray-400 mt-1">Admin Dashboard</p>
	</div>

	<nav class="flex-1 px-4 py-4 overflow-y-auto">
		{#each navItems as item}
			{@const Icon = item.icon}
			<a
				href={item.href}
				onclick={handleNavClick}
				class="flex items-center gap-3 px-4 py-3 rounded-lg mb-1 transition-colors
					{isActive(item.href)
					? 'bg-primary-50 text-primary-700 font-medium dark:bg-primary-900/30 dark:text-primary-300'
					: 'text-gray-600 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-gray-800'}"
			>
				<Icon size={18} />
				<span>{item.label}</span>
			</a>
		{/each}
	</nav>

	<div class="p-4 border-t border-gray-200 dark:border-gray-800 space-y-1">
		<button
			onclick={toggleTheme}
			class="w-full text-left px-4 py-3 text-gray-600 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-gray-800 rounded-lg transition-colors flex items-center gap-3"
		>
			{#if $theme === 'dark'}
				<Sun size={18} />
				<span>Light mode</span>
			{:else}
				<Moon size={18} />
				<span>Dark mode</span>
			{/if}
		</button>
		<button
			onclick={onLogout}
			class="w-full text-left px-4 py-3 text-gray-600 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-gray-800 rounded-lg transition-colors flex items-center gap-3"
		>
			<LogOut size={18} />
			<span>Logout</span>
		</button>
	</div>
</aside>
