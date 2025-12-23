<script lang="ts">
	interface Props {
		title: string;
		value: number | string;
		icon?: string;
		color?: 'blue' | 'green' | 'purple' | 'orange' | 'red';
	}

	let { title, value, icon = '#', color = 'blue' }: Props = $props();

	const colorClasses = {
		blue: 'bg-blue-50 text-blue-600',
		green: 'bg-green-50 text-green-600',
		purple: 'bg-purple-50 text-purple-600',
		orange: 'bg-orange-50 text-orange-600',
		red: 'bg-red-50 text-red-600'
	};
</script>

<div class="card">
	<div class="flex items-center justify-between">
		<div>
			<p class="text-sm text-gray-500 uppercase tracking-wider font-medium">{title}</p>
			<p class="text-3xl font-bold mt-2">{value.toLocaleString()}</p>
		</div>
		<div
			class="w-12 h-12 rounded-lg flex items-center justify-center text-xl {colorClasses[color]}"
		>
			{icon}
		</div>
	</div>
</div>
