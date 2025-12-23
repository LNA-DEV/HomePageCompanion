<script lang="ts">
	import { page } from '$app/stores';

	interface NavItem {
		href: string;
		label: string;
		icon: string;
	}

	interface Props {
		navItems: NavItem[];
		onLogout: () => void;
	}

	let { navItems, onLogout }: Props = $props();

	function isActive(href: string): boolean {
		return $page.url.pathname.startsWith(href);
	}
</script>

<aside class="w-64 bg-white shadow-md min-h-screen flex flex-col">
	<div class="p-6 border-b">
		<h1 class="text-xl font-bold text-primary-600">HomePageCompanion</h1>
		<p class="text-sm text-gray-500 mt-1">Admin Dashboard</p>
	</div>

	<nav class="flex-1 px-4 py-4">
		{#each navItems as item}
			<a
				href={item.href}
				class="flex items-center gap-3 px-4 py-3 rounded-lg mb-1 transition-colors
					{isActive(item.href)
					? 'bg-primary-50 text-primary-700 font-medium'
					: 'text-gray-600 hover:bg-gray-50'}"
			>
				<span class="w-5 h-5 flex items-center justify-center text-lg">{item.icon}</span>
				<span>{item.label}</span>
			</a>
		{/each}
	</nav>

	<div class="p-4 border-t">
		<button
			onclick={onLogout}
			class="w-full text-left px-4 py-3 text-gray-600 hover:bg-gray-50 rounded-lg transition-colors flex items-center gap-3"
		>
			<span class="w-5 h-5 flex items-center justify-center">X</span>
			<span>Logout</span>
		</button>
	</div>
</aside>
