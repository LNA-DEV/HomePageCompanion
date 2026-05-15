<script lang="ts" generics="T">
	import type { Snippet } from 'svelte';

	interface Column<T> {
		key: keyof T;
		label: string;
		format?: (value: unknown, row: T) => string;
		render?: Snippet<[T]>;
	}

	interface Props {
		columns: Column<T>[];
		data: T[];
		onDelete?: (row: T) => void;
		emptyMessage?: string;
	}

	let { columns, data, onDelete, emptyMessage = 'No data available' }: Props = $props();

	function getValue(row: T, col: Column<T>): string {
		const value = row[col.key];
		if (col.format) {
			return col.format(value, row);
		}
		if (value === null || value === undefined) {
			return '-';
		}
		return String(value);
	}
</script>

<div class="overflow-x-auto">
	<table class="w-full">
		<thead>
			<tr class="bg-gray-50 dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700">
				{#each columns as col}
					<th class="table-header">{col.label}</th>
				{/each}
				{#if onDelete}
					<th class="table-header text-right">Actions</th>
				{/if}
			</tr>
		</thead>
		<tbody>
			{#if data.length === 0}
				<tr>
					<td colspan={columns.length + (onDelete ? 1 : 0)} class="table-cell text-center text-gray-500 dark:text-gray-400 py-8">
						{emptyMessage}
					</td>
				</tr>
			{:else}
				{#each data as row}
					<tr class="border-b border-gray-200 dark:border-gray-700 hover:bg-gray-50 dark:hover:bg-gray-800/50 transition-colors">
						{#each columns as col}
							<td class="table-cell">
								{#if col.render}
									{@render col.render(row)}
								{:else}
									{getValue(row, col)}
								{/if}
							</td>
						{/each}
						{#if onDelete}
							<td class="table-cell text-right">
								<button
									onclick={() => onDelete(row)}
									class="text-red-600 dark:text-red-400 hover:text-red-800 dark:hover:text-red-300 text-sm font-medium"
								>
									Delete
								</button>
							</td>
						{/if}
					</tr>
				{/each}
			{/if}
		</tbody>
	</table>
</div>
