import {
  ArrowDownMini,
  ArrowUpMini,
  ChevronRight,
  ChevronUpDown,
  EllipsisHorizontal,
} from "../../icons";
import {
  createContext,
  useContext,
  useId,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import {
  Button as AriaButton,
  Cell as AriaCell,
  Collection,
  Column as AriaColumn,
  Row as AriaRow,
  Table as AriaTable,
  TableBody as AriaTableBody,
  TableHeader as AriaTableHeader,
  useTableOptions,
  type CellProps as AriaCellProps,
  type ColumnProps as AriaColumnProps,
  type Key,
  type RowProps as AriaRowProps,
  type Selection,
  type SortDescriptor,
  type TableBodyProps as AriaTableBodyProps,
  type TableHeaderProps as AriaTableHeaderProps,
  type TableProps as AriaTableProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import { Button, IconButton } from "../button/button";
import { Checkbox } from "../checkbox/checkbox";
import { Skeleton } from "../feedback/feedback";
import { Menu } from "../menu/menu";
import { Select } from "../select/select";

export type CellAlign = "start" | "center" | "end";

/* Primitives ------------------------------------------------------------ */

export interface TableProps extends Omit<
  AriaTableProps,
  "className" | "style"
> {
  className?: string;
  /**
   * Hangs selection checkboxes in the left gutter so the header pill lines
   * up with the toolbar above, as Resend does on wide screens.
   */
  hangingSelection?: boolean;
  "aria-busy"?: boolean;
}

export function Table({
  className,
  hangingSelection = false,
  "aria-busy": ariaBusy,
  ...props
}: TableProps) {
  return (
    <div
      aria-busy={ariaBusy}
      className={cx(
        "cv-table-container",
        hangingSelection && "cv-table-container--hanging",
      )}
      onKeyDownCapture={(event) => {
        // React Aria's cell navigation runs in capture phase. Keep native
        // editing keys inside expanded forms before that handler sees them.
        if (
          [
            "ArrowLeft",
            "ArrowRight",
            "ArrowUp",
            "ArrowDown",
            "Home",
            "End",
          ].includes(event.key) &&
          event.target instanceof HTMLElement &&
          event.target.closest(".cv-table__details") &&
          event.target.closest('input, textarea, [contenteditable="true"]')
        )
          event.stopPropagation();
      }}
    >
      <AriaTable {...props} className={cx("cv-table", className)} />
    </div>
  );
}

export function TableHeader<T extends object>({
  columns,
  children,
  selectionLabel,
  ...props
}: Omit<AriaTableHeaderProps<T>, "className" | "style"> & {
  selectionLabel?: string;
}) {
  const { selectionBehavior, selectionMode } = useTableOptions();

  return (
    <AriaTableHeader {...props} className="cv-table__header">
      {selectionBehavior === "toggle" && selectionMode !== "none" ? (
        <AriaColumn className="cv-table__column cv-table__column--selection">
          {selectionMode === "multiple" ? (
            <Checkbox
              slot="selection"
              {...(selectionLabel ? { "aria-label": selectionLabel } : {})}
            />
          ) : (
            <span className="cv-sr-only">Select</span>
          )}
        </AriaColumn>
      ) : null}
      <Collection {...(columns !== undefined ? { items: columns } : {})}>
        {children}
      </Collection>
    </AriaTableHeader>
  );
}

export interface ColumnProps extends Omit<
  AriaColumnProps,
  "children" | "className" | "style" | "width"
> {
  children: ReactNode;
  align?: CellAlign;
  width?: CSSProperties["width"];
}

export function Column({
  children,
  align = "start",
  width,
  ...props
}: ColumnProps) {
  return (
    <AriaColumn
      {...props}
      {...(width !== undefined ? { style: { width } } : {})}
      className={cx("cv-table__column", `cv-table__column--${align}`)}
    >
      {({ allowsSorting, sortDirection }) => (
        <span className="cv-table__column-content">
          {children}
          {allowsSorting ? (
            <span className="cv-table__sort" aria-hidden="true">
              {sortDirection === "ascending" ? (
                <ArrowUpMini />
              ) : sortDirection === "descending" ? (
                <ArrowDownMini />
              ) : (
                <ChevronUpDown />
              )}
            </span>
          ) : null}
        </span>
      )}
    </AriaColumn>
  );
}

export function TableBody<T extends object>(
  props: Omit<AriaTableBodyProps<T>, "className" | "style">,
) {
  return <AriaTableBody {...props} className="cv-table__body" />;
}

export interface RowProps<T extends object> extends Omit<
  AriaRowProps<T>,
  "className" | "style"
> {
  className?: string;
}

export function Row<T extends object>({
  columns,
  children,
  className,
  ...props
}: RowProps<T>) {
  const { selectionBehavior, selectionMode } = useTableOptions();

  return (
    <AriaRow {...props} className={cx("cv-table__row", className)}>
      {selectionBehavior === "toggle" && selectionMode !== "none" ? (
        <AriaCell className="cv-table__cell cv-table__cell--selection">
          <Checkbox slot="selection" />
        </AriaCell>
      ) : null}
      <Collection {...(columns !== undefined ? { items: columns } : {})}>
        {children}
      </Collection>
    </AriaRow>
  );
}

export interface CellProps extends Omit<AriaCellProps, "className" | "style"> {
  align?: CellAlign;
}

export function Cell({ align = "start", ...props }: CellProps) {
  return (
    <AriaCell
      {...props}
      className={cx("cv-table__cell", `cv-table__cell--${align}`)}
    />
  );
}

export interface RowLinkProps {
  children: ReactNode;
  /** The row this link belongs to is the one currently open or selected. */
  isCurrent?: boolean;
  onPress: () => void;
  className?: string;
}

/**
 * A row's name as the thing you press to open it: a dashed underline on
 * hover that turns blue while the row is current. Pair it with
 * `rowHighlight="none"` so the row itself stays unpainted.
 */
export function RowLink({
  children,
  isCurrent = false,
  onPress,
  className,
}: RowLinkProps) {
  return (
    <AriaButton
      className={cx("cv-table__link", className)}
      {...(isCurrent ? { "data-current": true, "aria-current": "true" } : {})}
      onPress={onPress}
    >
      {children}
    </AriaButton>
  );
}

/** Icon tile or avatar followed by the resource name. */
export function TableResource({
  icon,
  children,
}: {
  icon?: ReactNode;
  children: ReactNode;
}) {
  return (
    <span className="cv-table__resource">
      {icon}
      <span className="cv-table__resource-name">{children}</span>
    </span>
  );
}

/* Pagination ------------------------------------------------------------ */

interface TablePaginationOptions {
  "aria-label"?: string;
  page: number;
  pageSize?: number;
  /** Plural noun, e.g. "zones". */
  itemLabel: string;
  onPageChange: (page: number) => void;
  onPageSizeChange?: (pageSize: number) => void;
  pageSizeOptions?: readonly number[];
  previousLabel?: string;
  nextLabel?: string;
  isDisabled?: boolean;
  trailingAction?: ReactNode;
}

export type TablePaginationProps = TablePaginationOptions &
  (
    | { totalItems: number; currentPageItems?: never; hasNextPage?: never }
    | { totalItems: null; currentPageItems: number; hasNextPage: boolean }
  );

const numberFormat = new Intl.NumberFormat("en");

export function TablePagination({
  "aria-label": label = "Pagination",
  page,
  pageSize = 10,
  totalItems,
  currentPageItems,
  hasNextPage,
  trailingAction,
  itemLabel,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = [10, 25, 50],
  previousLabel = "Previous",
  nextLabel = "Next",
  isDisabled = false,
}: TablePaginationProps) {
  const pageCount =
    totalItems === null ? null : Math.max(1, Math.ceil(totalItems / pageSize));

  return (
    <nav className="cv-table-pagination" aria-label={label}>
      <span className="cv-table-pagination__summary">
        {totalItems === null ? (
          <>
            Page {page} – {numberFormat.format(currentPageItems)} {itemLabel}
          </>
        ) : (
          <>
            Page {page} of {numberFormat.format(pageCount!)} –{" "}
            {numberFormat.format(totalItems)} {itemLabel} –
          </>
        )}
        {totalItems === null ? null : onPageSizeChange ? (
          <Select
            variant="inline"
            aria-label="Rows per page"
            isDisabled={isDisabled}
            options={pageSizeOptions.map((size) => ({
              id: size,
              label: `${size} rows`,
            }))}
            value={pageSize}
            onChange={(key) => {
              if (key != null) onPageSizeChange(Number(key));
            }}
          />
        ) : (
          ` ${pageSize} items`
        )}
      </span>
      {pageCount === null || pageCount > 1 || trailingAction ? (
        <div className="cv-table-pagination__actions">
          <Button
            size="sm"
            isDisabled={isDisabled || page <= 1}
            onPress={() => onPageChange(page - 1)}
          >
            {previousLabel}
          </Button>
          <Button
            size="sm"
            isDisabled={
              isDisabled ||
              (pageCount === null ? !hasNextPage : page >= pageCount)
            }
            onPress={() => onPageChange(page + 1)}
          >
            {nextLabel}
          </Button>
          {trailingAction}
        </div>
      ) : null}
    </nav>
  );
}

/* Data table ------------------------------------------------------------ */

export interface DataTableColumn<T> {
  id: string;
  header: ReactNode;
  cell: (row: T) => ReactNode;
  align?: CellAlign;
  width?: CSSProperties["width"];
  isRowHeader?: boolean;
  allowsSorting?: boolean;
  /** Plain-text header for sorting announcements when `header` is not a string. */
  textValue?: string;
}

export interface DataTableProps<T extends { id: Key }> {
  "aria-label": string;
  columns: readonly DataTableColumn<T>[];
  rows: readonly T[];
  selectionMode?: "none" | "single" | "multiple";
  /**
   * `replace` selects a row on press and when keyboard focus moves, with no
   * checkbox column: the list form of a master-detail editor.
   */
  selectionBehavior?: "toggle" | "replace";
  selectedKeys?: Selection;
  onSelectionChange?: (keys: Selection) => void;
  /** Describe the selection scope, e.g. "Select all events on this page". */
  selectionLabel?: string;
  disabledKeys?: Iterable<Key>;
  sortDescriptor?: SortDescriptor;
  onSortChange?: (descriptor: SortDescriptor) => void;
  /** Row press handler. Expandable tables toggle the row when this is unset. */
  onRowAction?: (key: Key) => void;
  /** Readable row name used by the expand and actions buttons. */
  rowLabel?: (row: T) => string;
  /** Menu items for the trailing "…" menu on each row. */
  rowActions?: (row: T) => ReactNode;
  rowActionsLabel?: (row: T) => string;
  /**
   * Detail panel rendered under an expanded row. Supplying it adds a leading
   * chevron column; the panel spans the full width of the table.
   */
  renderExpandedRow?: (row: T) => ReactNode;
  expandedKeys?: Iterable<Key>;
  defaultExpandedKeys?: Iterable<Key>;
  onExpandedChange?: (keys: Set<Key>) => void;
  /** Keeps existing rows visible and inert while fetching; skeletons only on first load. */
  isLoading?: boolean;
  loadingRowCount?: number;
  /** Replaces the whole table when there are no rows. */
  emptyState?: ReactNode;
  hangingSelection?: boolean;
  /**
   * `fill` keeps standard row surfaces. `none` leaves rows unpainted
   * for lists whose `RowLink` already shows the current row.
   */
  rowHighlight?: "fill" | "none";
  /** Reduce vertical padding for dense inventories. */
  density?: "default" | "compact";
}

const detailKey = (key: Key) => `${String(key)}::details`;

// React Aria renders rows from a collection snapshot that catches up one render
// after the table's props change. A controlled input in a detail panel built
// from that snapshot is reset to its previous value on every keystroke, which
// moves the caret to the end. Reading the panel through context keeps it current.
const ExpandedRowContext = createContext<(key: Key) => ReactNode>(() => null);

function ExpandedRowContent({ rowKey }: { rowKey: Key }) {
  return useContext(ExpandedRowContext)(rowKey);
}

export function DataTable<T extends { id: Key }>({
  columns,
  rows,
  rowLabel,
  rowActions,
  rowActionsLabel,
  renderExpandedRow,
  expandedKeys,
  defaultExpandedKeys,
  onExpandedChange,
  isLoading = false,
  loadingRowCount = 5,
  emptyState,
  hangingSelection = false,
  rowHighlight = "fill",
  density = "default",
  selectionLabel,
  ...tableProps
}: DataTableProps<T>) {
  const idPrefix = useId();
  const [uncontrolledExpanded, setUncontrolledExpanded] = useState<Set<Key>>(
    () => new Set(defaultExpandedKeys ?? []),
  );
  const expanded =
    expandedKeys !== undefined ? new Set(expandedKeys) : uncontrolledExpanded;

  if (!isLoading && rows.length === 0 && emptyState != null) {
    return <>{emptyState}</>;
  }

  const showSkeletons = isLoading && rows.length === 0;
  const isExpandable = renderExpandedRow !== undefined;
  const hasSelection =
    tableProps.selectionMode !== undefined &&
    tableProps.selectionMode !== "none";
  const spannedColumns =
    columns.length + (isExpandable ? 1 : 0) + (rowActions ? 1 : 0);
  const rowHeaderId =
    columns.find((column) => column.isRowHeader)?.id ?? columns[0]?.id;

  const toggle = (key: Key) => {
    const next = new Set(expanded);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    if (expandedKeys === undefined) setUncontrolledExpanded(next);
    onExpandedChange?.(next);
  };

  const renderDetails = (key: Key) => {
    const row = rows.find((row) => row.id === key);
    return row && renderExpandedRow ? renderExpandedRow(row) : null;
  };

  // Detail rows are real grid rows so keyboard users can reach their
  // content, but they are never selectable and never toggle anything.
  const expandableProps = isExpandable
    ? {
        disabledBehavior: "selection" as const,
        disabledKeys: [
          ...(tableProps.disabledKeys ?? []),
          ...rows
            .filter((row) => expanded.has(row.id))
            .map((row) => detailKey(row.id)),
        ],
        ...(tableProps.onRowAction === undefined
          ? {
              onRowAction: (key: Key) => {
                if (rows.some((row) => row.id === key)) toggle(key);
              },
            }
          : {}),
      }
    : {};

  return (
    <ExpandedRowContext value={renderDetails}>
      {isLoading ? (
        <span className="cv-sr-only" role="status">
          Loading
        </span>
      ) : null}
      <Table
        {...tableProps}
        {...expandableProps}
        {...(showSkeletons ? { disabledBehavior: "all" as const } : {})}
        aria-busy={isLoading}
        inert={isLoading}
        hangingSelection={hangingSelection}
        className={cx(
          rowHighlight === "none" && "cv-table--quiet",
          density === "compact" && "cv-table--compact",
        )}
      >
        <TableHeader {...(selectionLabel ? { selectionLabel } : {})}>
          {isExpandable ? (
            <AriaColumn
              id="expand"
              textValue="Expand"
              className="cv-table__column cv-table__column--selection"
            >
              <span className="cv-sr-only">Expand</span>
            </AriaColumn>
          ) : null}
          {columns.map((column) => (
            <Column
              key={column.id}
              id={column.id}
              isRowHeader={column.id === rowHeaderId}
              allowsSorting={column.allowsSorting ?? false}
              align={column.align ?? "start"}
              {...(column.width !== undefined ? { width: column.width } : {})}
              {...(column.textValue !== undefined
                ? { textValue: column.textValue }
                : {})}
            >
              {column.header}
            </Column>
          ))}
          {rowActions ? (
            <Column id="actions" align="center" width={70} textValue="Actions">
              <span className="cv-sr-only">Actions</span>
            </Column>
          ) : null}
        </TableHeader>
        <TableBody>
          {showSkeletons
            ? Array.from({ length: loadingRowCount }, (_, index) => (
                <Row
                  key={`loading-${index}`}
                  id={`loading-${index}`}
                  isDisabled
                >
                  {isExpandable ? (
                    <AriaCell className="cv-table__cell cv-table__cell--selection" />
                  ) : null}
                  {columns.map((column) => (
                    <Cell key={column.id}>
                      <Skeleton />
                    </Cell>
                  ))}
                  {rowActions ? (
                    <Cell>
                      <Skeleton />
                    </Cell>
                  ) : null}
                </Row>
              ))
            : rows.flatMap((row) => {
                const isOpen = isExpandable && expanded.has(row.id);
                const label = rowLabel?.(row) ?? String(row.id);
                const panelId = `${idPrefix}-details-${String(row.id)}`;
                const actionsLabel =
                  rowActionsLabel?.(row) ??
                  (rowLabel ? `Actions for ${label}` : "Row actions");

                const main = (
                  <Row
                    key={row.id}
                    id={row.id}
                    className={cx(isOpen && "cv-table__row--open")}
                  >
                    {isExpandable ? (
                      <AriaCell className="cv-table__cell cv-table__cell--selection cv-table__cell--center">
                        <IconButton
                          aria-label={`${isOpen ? "Collapse" : "Expand"} ${label}`}
                          aria-expanded={isOpen}
                          {...(isOpen ? { "aria-controls": panelId } : {})}
                          variant="ghost"
                          size="sm"
                          className="cv-table__expand"
                          onPress={() => toggle(row.id)}
                        >
                          <ChevronRight />
                        </IconButton>
                      </AriaCell>
                    ) : null}
                    {columns.map((column) => (
                      <Cell key={column.id} align={column.align ?? "start"}>
                        {column.cell(row)}
                      </Cell>
                    ))}
                    {rowActions ? (
                      <Cell align="center">
                        <Menu
                          aria-label={actionsLabel}
                          trigger={
                            <IconButton
                              aria-label={actionsLabel}
                              variant="ghost"
                              className="cv-table__actions-trigger"
                            >
                              <EllipsisHorizontal />
                            </IconButton>
                          }
                        >
                          {rowActions(row)}
                        </Menu>
                      </Cell>
                    ) : null}
                  </Row>
                );

                if (!isOpen || renderExpandedRow === undefined) return [main];

                return [
                  main,
                  <AriaRow
                    key={detailKey(row.id)}
                    id={detailKey(row.id)}
                    className="cv-table__row cv-table__row--details"
                  >
                    {hasSelection ? (
                      <AriaCell className="cv-table__cell cv-table__cell--selection" />
                    ) : null}
                    <AriaCell
                      colSpan={spannedColumns}
                      className="cv-table__cell cv-table__cell--details"
                    >
                      <div
                        id={panelId}
                        role="region"
                        aria-label={`${label} details`}
                        className="cv-table__details"
                      >
                        <ExpandedRowContent rowKey={row.id} />
                      </div>
                    </AriaCell>
                  </AriaRow>,
                ];
              })}
        </TableBody>
      </Table>
    </ExpandedRowContext>
  );
}
