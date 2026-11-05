import { useState } from "react";
import type { Key } from "react-aria-components";
import { DataTable, TablePagination, type DataTableProps } from "./table";

export interface PaginatedDataTableProps<
  T extends { id: Key },
> extends DataTableProps<T> {
  itemLabel: string;
  /** Reset the page when the resource or filter changes, not on live refresh. */
  paginationKey?: string;
}

/** Pagination for a bounded, already-loaded collection. Remote cursors stay with their caller. */
export function PaginatedDataTable<T extends { id: Key }>({
  rows,
  itemLabel,
  paginationKey,
  ...props
}: PaginatedDataTableProps<T>) {
  const [state, setState] = useState({ key: paginationKey, page: 1, size: 10 });
  let current = state;
  if (state.key !== paginationKey) {
    current = { ...state, key: paginationKey, page: 1 };
    setState(current);
  }
  const size = current.size;
  const page = Math.min(
    current.page,
    Math.max(1, Math.ceil(rows.length / size)),
  );
  return (
    <div className="cv-paginated-table">
      <DataTable {...props} rows={rows.slice((page - 1) * size, page * size)} />
      {rows.length > 10 && (
        <TablePagination
          aria-label={`${props["aria-label"]} pagination`}
          page={page}
          pageSize={size}
          totalItems={rows.length}
          itemLabel={itemLabel}
          isDisabled={props.isLoading ?? false}
          onPageChange={(value) =>
            setState({ key: paginationKey, size, page: value })
          }
          onPageSizeChange={(value) =>
            setState({ key: paginationKey, size: value, page: 1 })
          }
        />
      )}
    </div>
  );
}
