<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import type { ComponentType } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { isAuthenticated, isLoading, checkAuth, logout } from '$lib/stores/auth';
	import { installClientLogger } from '$lib/clientLogger';
	import { Sidebar } from '$lib/components';
	import {
		LayoutDashboard,
		Rss,
		Send,
		Link2,
		Heart,
		Users,
		MessageSquare,
		Megaphone,
		Menu,
		ScrollText,
		AlertTriangle,
		PenSquare
	} from 'lucide-svelte';

	interface NavItem {
		href: string;
		label: string;
		icon: ComponentType;
	}

	const navItems: NavItem[] = [
		{ href: '/dashboard', label: 'Dashboard', icon: LayoutDashboard },
		{ href: '/microblog', label: 'Microblog', icon: PenSquare },
		{ href: '/feeds', label: 'Feeds', icon: Rss },
		{ href: '/publications', label: 'Publications', icon: Send },
		{ href: '/uploads', label: 'Uploads', icon: AlertTriangle },
		{ href: '/connections', label: 'Connections', icon: Link2 },
		{ href: '/interactions', label: 'Interactions', icon: Heart },
		{ href: '/subscribers', label: 'Subscribers', icon: Users },
		{ href: '/webmentions', label: 'Webmentions', icon: MessageSquare },
		{ href: '/broadcast', label: 'Broadcast', icon: Megaphone },
		{ href: '/logs', label: 'Logs', icon: ScrollText }
	];

	let { children } = $props();
	let sidebarOpen = $state(false);

	onMount(() => {
		checkAuth();
	});

	$effect(() => {
		if (!$isLoading && !$isAuthenticated && $page.url.pathname !== '/login') {
			goto('/login');
		}
		if (!$isLoading && $isAuthenticated) {
			installClientLogger();
		}
	});

	function handleLogout() {
		logout();
		goto('/login');
	}
</script>

{#if $isLoading}
	<div class="min-h-screen flex items-center justify-center bg-gray-100 dark:bg-gray-950">
		<div class="text-center">
			<div
				class="w-8 h-8 border-4 border-primary-200 dark:border-primary-900 border-t-primary-600 rounded-full animate-spin mx-auto mb-4"
			></div>
			<p class="text-gray-500 dark:text-gray-400">Loading...</p>
		</div>
	</div>
{:else if $isAuthenticated}
	<div class="min-h-screen flex bg-gray-100 dark:bg-gray-950">
		<Sidebar
			{navItems}
			onLogout={handleLogout}
			open={sidebarOpen}
			onClose={() => (sidebarOpen = false)}
		/>
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
				<h1 class="text-base font-semibold text-primary-700 dark:text-primary-300 truncate">
					HomePageCompanion
				</h1>
			</header>
			<main class="flex-1 p-4 sm:p-8 overflow-x-hidden">
				{@render children()}
			</main>
		</div>
	</div>
{:else}
	{@render children()}
{/if}
