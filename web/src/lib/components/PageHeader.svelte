<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		description?: string;
		actions?: Snippet;
	}

	let { title, description, actions }: Props = $props();
</script>

<div class="mb-8 flex items-start justify-between">
	<div>
		<h1 class="text-3xl font-bold text-gray-900">{title}</h1>
		{#if description}
			<p class="mt-2 text-gray-600">{description}</p>
		{/if}
	</div>
	{#if actions}
		<div class="flex gap-2">
			{@render actions()}
		</div>
	{/if}
</div>
