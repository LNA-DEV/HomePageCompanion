<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { isAuthenticated, isLoading } from '$lib/stores/auth';

	onMount(() => {
		const unsubscribe = isLoading.subscribe((loading) => {
			if (!loading) {
				isAuthenticated.subscribe((authed) => {
					if (authed) {
						goto('/dashboard');
					} else {
						goto('/login');
					}
				})();
			}
		});

		return unsubscribe;
	});
</script>

<div class="min-h-screen flex items-center justify-center bg-gray-100">
	<div class="text-center">
		<div
			class="w-8 h-8 border-4 border-primary-200 border-t-primary-600 rounded-full animate-spin mx-auto mb-4"
		></div>
		<p class="text-gray-500">Redirecting...</p>
	</div>
</div>
