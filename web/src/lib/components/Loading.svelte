<script lang="ts">
	interface Props {
		message?: string;
	}

	let { message = 'Loading...' }: Props = $props();
</script>

<div class="flex items-center justify-center py-12">
	<div class="text-center">
		<div
			class="w-8 h-8 border-4 border-primary-200 border-t-primary-600 rounded-full animate-spin mx-auto mb-4"
		></div>
		<p class="text-gray-500">{message}</p>
	</div>
</div>
