<script lang="ts">
	interface Props {
		title: string;
		value: number | string;
		icon?: string;
		color?: 'blue' | 'green' | 'purple' | 'orange' | 'red';
	}

	let { title, value, icon = '#', color = 'blue' }: Props = $props();

	const colorClasses = {
		blue: 'bg-blue-50 text-blue-600 dark:bg-blue-900/30 dark:text-blue-300',
		green: 'bg-green-50 text-green-600 dark:bg-green-900/30 dark:text-green-300',
		purple: 'bg-purple-50 text-purple-600 dark:bg-purple-900/30 dark:text-purple-300',
		orange: 'bg-orange-50 text-orange-600 dark:bg-orange-900/30 dark:text-orange-300',
		red: 'bg-red-50 text-red-600 dark:bg-red-900/30 dark:text-red-300'
	};
</script>

<div class="card">
	<div class="flex items-center justify-between">
		<div>
			<p class="text-sm text-gray-500 dark:text-gray-400 uppercase tracking-wider font-medium">
				{title}
			</p>
			<p class="text-3xl font-bold mt-2">{value.toLocaleString()}</p>
		</div>
		<div
			class="w-12 h-12 rounded-lg flex items-center justify-center text-xl {colorClasses[color]}"
		>
			{icon}
		</div>
	</div>
</div>
