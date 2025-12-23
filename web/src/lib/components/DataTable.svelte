<script lang="ts" generics="T extends Record<string, unknown>">
	interface Column<T> {
		key: keyof T;
		label: string;
		format?: (value: unknown, row: T) => string;
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
			<tr class="bg-gray-50 border-b">
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
					<td colspan={columns.length + (onDelete ? 1 : 0)} class="table-cell text-center text-gray-500 py-8">
						{emptyMessage}
					</td>
				</tr>
			{:else}
				{#each data as row}
					<tr class="border-b hover:bg-gray-50 transition-colors">
						{#each columns as col}
							<td class="table-cell">{getValue(row, col)}</td>
						{/each}
						{#if onDelete}
							<td class="table-cell text-right">
								<button
									onclick={() => onDelete(row)}
									class="text-red-600 hover:text-red-800 text-sm font-medium"
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
