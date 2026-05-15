<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		description?: string;
		actions?: Snippet;
	}

	let { title, description, actions }: Props = $props();
</script>

<div class="mb-8 flex flex-col sm:flex-row sm:items-start sm:justify-between gap-3">
	<div class="min-w-0">
		<h1 class="text-2xl sm:text-3xl font-bold text-gray-900 dark:text-white">{title}</h1>
		{#if description}
			<p class="mt-2 text-gray-600 dark:text-gray-400">{description}</p>
		{/if}
	</div>
	{#if actions}
		<div class="flex gap-2 flex-wrap">
			{@render actions()}
		</div>
	{/if}
</div>
