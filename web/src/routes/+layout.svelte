<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { isAuthenticated, isLoading, checkAuth, logout } from '$lib/stores/auth';
	import { Sidebar } from '$lib/components';

	interface NavItem {
		href: string;
		label: string;
		icon: string;
	}

	const navItems: NavItem[] = [
		{ href: '/dashboard', label: 'Dashboard', icon: '#' },
		{ href: '/feeds', label: 'Feeds', icon: 'R' },
		{ href: '/publications', label: 'Publications', icon: 'P' },
		{ href: '/interactions', label: 'Interactions', icon: 'L' },
		{ href: '/subscribers', label: 'Subscribers', icon: 'S' },
		{ href: '/webmentions', label: 'Webmentions', icon: 'W' },
		{ href: '/broadcast', label: 'Broadcast', icon: 'B' }
	];

	let { children } = $props();

	onMount(() => {
		checkAuth();
	});

	$effect(() => {
		if (!$isLoading && !$isAuthenticated && $page.url.pathname !== '/login') {
			goto('/login');
		}
	});

	function handleLogout() {
		logout();
		goto('/login');
	}
</script>

{#if $isLoading}
	<div class="min-h-screen flex items-center justify-center bg-gray-100">
		<div class="text-center">
			<div
				class="w-8 h-8 border-4 border-primary-200 border-t-primary-600 rounded-full animate-spin mx-auto mb-4"
			></div>
			<p class="text-gray-500">Loading...</p>
		</div>
	</div>
{:else if $isAuthenticated}
	<div class="min-h-screen flex bg-gray-100">
		<Sidebar {navItems} onLogout={handleLogout} />
		<main class="flex-1 p-8 overflow-auto">
			{@render children()}
		</main>
	</div>
{:else}
	{@render children()}
{/if}
