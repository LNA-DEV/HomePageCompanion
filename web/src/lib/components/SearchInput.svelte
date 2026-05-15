<script lang="ts">
	import { Search, X } from 'lucide-svelte';

	interface Props {
		value: string;
		placeholder?: string;
		onInput: (value: string) => void;
	}

	let { value = $bindable(), placeholder = 'Search…', onInput }: Props = $props();

	function clear() {
		value = '';
		onInput('');
	}
</script>

<div class="relative w-full max-w-sm">
	<span class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-gray-500">
		<Search size={16} />
	</span>
	<input
		type="text"
		bind:value
		oninput={(e) => onInput((e.target as HTMLInputElement).value)}
		{placeholder}
		class="input pl-9 pr-9"
	/>
	{#if value}
		<button
			type="button"
			onclick={clear}
			class="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-gray-400 hover:text-gray-600 dark:text-gray-500 dark:hover:text-gray-300"
			aria-label="Clear search"
		>
			<X size={14} />
		</button>
	{/if}
</div>
