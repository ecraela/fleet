import { Check, ChevronDown, Plus, Tag } from "../../icons";
import { useId, useState, type ReactNode } from "react";
import {
  Autocomplete,
  Button,
  Menu,
  MenuItem,
  MenuSection,
  MenuTrigger,
  Popover,
  useFilter,
  type Key,
  type Selection,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import { Badge } from "../badge/badge";
import { MenuSeparator } from "../menu/menu";
import { SearchField } from "../text-field/text-field";

export interface TagOption {
  id: Key;
  label: string;
  /** Supporting text shown beneath the label, e.g. what selecting it does. */
  description?: string;
  /** Defaults to a tag icon. */
  icon?: ReactNode;
}

export interface TagPickerProps {
  label?: string;
  "aria-label"?: string;
  description?: string;
  isDisabled?: boolean;
  /**
   * Trigger button content. Defaults to a chip per selected item (falling
   * back to `emptyLabel` when nothing is selected) — pass this to override,
   * e.g. with a count like "2 labels".
   */
  triggerLabel?: ReactNode;
  /** Trigger text shown when nothing is selected. @default "Add labels" */
  emptyLabel?: string;
  items: readonly TagOption[];
  selectedKeys: Selection;
  onSelectionChange: (keys: Selection) => void;
  /**
   * Adds a "Create <query>" row when no option matches the typed text. The
   * caller owns adding the new tag to `items` and to `selectedKeys`.
   */
  onCreate?: (query: string) => void;
  /**
   * Checked against typed text before offering to create it. Return a
   * message to reject the value (shown in place of the create row) or
   * `undefined` to allow it.
   */
  validate?: (query: string) => string | undefined;
  /** Caps how many items can be selected at once. */
  maxSelected?: number;
  /** Shown once `maxSelected` is reached. */
  maxSelectedMessage?: string;
  /** Search input's accessible label. @default "Search labels" */
  searchLabel?: string;
  /** Shown when no items match the search. @default "No labels found." */
  noResultsMessage?: string;
  placeholder?: string;
  className?: string;
  /** Fires when the popover opens or closes, e.g. to save on close. */
  onOpenChange?: (isOpen: boolean) => void;
}

function resolveSelectedIds(
  selection: Selection,
  items: readonly TagOption[],
): Set<Key> {
  return selection === "all"
    ? new Set(items.map((item) => item.id))
    : selection;
}

/**
 * A searchable, multi-select list for labels or tags, with an optional
 * inline "create" affordance for values that don't exist yet.
 */
export function TagPicker({
  label,
  "aria-label": ariaLabel,
  description,
  isDisabled = false,
  triggerLabel,
  emptyLabel = "Add labels",
  items,
  selectedKeys,
  onSelectionChange,
  onCreate,
  validate,
  maxSelected,
  maxSelectedMessage,
  searchLabel = "Search labels",
  noResultsMessage = "No labels found.",
  placeholder = "Search or create a label",
  className,
  onOpenChange,
}: TagPickerProps) {
  const descriptionId = useId();
  const { contains } = useFilter({ sensitivity: "base" });
  const [query, setQuery] = useState("");
  const trimmed = query.trim();
  const hasExactMatch = items.some(
    (item) => item.label.toLowerCase() === trimmed.toLowerCase(),
  );

  const selectedIds = resolveSelectedIds(selectedKeys, items);
  const atMax = maxSelected != null && selectedIds.size >= maxSelected;
  const capMessage =
    maxSelectedMessage ?? `You can select up to ${maxSelected}.`;

  const wantsCreate = onCreate != null && trimmed !== "" && !hasExactMatch;
  const rejection = wantsCreate
    ? atMax
      ? capMessage
      : validate?.(trimmed)
    : undefined;
  const showCreate = wantsCreate && !rejection;

  const selectedItems = items.filter((item) => selectedIds.has(item.id));
  const resolvedTriggerLabel =
    triggerLabel ??
    (selectedItems.length ? (
      <span className="cv-tag-picker__chips">
        {selectedItems.map((item) => (
          <Badge key={item.id} tone="neutral" icon={item.icon ?? <Tag />}>
            {item.label}
          </Badge>
        ))}
      </span>
    ) : (
      emptyLabel
    ));

  const picker = (
    <MenuTrigger
      onOpenChange={(open) => {
        if (!open) setQuery("");
        onOpenChange?.(open);
      }}
    >
      <Button
        {...(ariaLabel || label ? { "aria-label": ariaLabel ?? label } : {})}
        {...(description ? { "aria-describedby": descriptionId } : {})}
        isDisabled={isDisabled}
        className={cx("cv-tag-picker__trigger", className)}
      >
        {resolvedTriggerLabel}
        <ChevronDown aria-hidden="true" className="cv-tag-picker__chevron" />
      </Button>
      <Popover
        placement="bottom start"
        offset={4}
        className="cv-popover cv-tag-picker__popover"
      >
        <Autocomplete
          inputValue={query}
          onInputChange={setQuery}
          filter={(textValue, inputValue) => contains(textValue, inputValue)}
        >
          <div className="cv-tag-picker__search">
            <SearchField aria-label={searchLabel} placeholder={placeholder} />
          </div>
          {atMax && <p className="cv-tag-picker__note">{capMessage}</p>}
          <Menu
            aria-label={ariaLabel ?? "Labels"}
            className="cv-menu cv-tag-picker__menu"
            renderEmptyState={() =>
              showCreate || rejection ? null : (
                <div className="cv-tag-picker__empty">{noResultsMessage}</div>
              )
            }
          >
            <MenuSection
              selectionMode="multiple"
              selectedKeys={selectedKeys}
              onSelectionChange={onSelectionChange}
            >
              {items.map((item) => (
                <MenuItem
                  key={item.id}
                  id={item.id}
                  textValue={item.label}
                  isDisabled={
                    maxSelected != null && atMax && !selectedIds.has(item.id)
                  }
                  className="cv-menu__item cv-tag-picker__item"
                >
                  {({ isSelected }) => (
                    <>
                      <span className="cv-listbox__main">
                        <span className="cv-listbox__icon" aria-hidden="true">
                          {item.icon ?? <Tag />}
                        </span>
                        <span className="cv-listbox__copy">
                          <span className="cv-listbox__label">
                            {item.label}
                          </span>
                          {item.description != null ? (
                            <span className="cv-listbox__description">
                              {item.description}
                            </span>
                          ) : null}
                        </span>
                      </span>
                      <span className="cv-listbox__check" aria-hidden="true">
                        {isSelected ? <Check /> : null}
                      </span>
                    </>
                  )}
                </MenuItem>
              ))}
            </MenuSection>
            {showCreate ? (
              <>
                <MenuSeparator />
                {/* textValue always contains the query, so Autocomplete's
                    filter keeps this row visible exactly when it's needed. */}
                <MenuItem
                  id="__create__"
                  textValue={`Create ${trimmed}`}
                  onAction={() => onCreate?.(trimmed)}
                  className="cv-menu__item cv-tag-picker__item"
                >
                  <span className="cv-listbox__icon" aria-hidden="true">
                    <Plus />
                  </span>
                  <span className="cv-listbox__label">
                    Create &ldquo;{trimmed}&rdquo;
                  </span>
                </MenuItem>
              </>
            ) : null}
            {rejection ? (
              <>
                <MenuSeparator />
                <MenuItem
                  id="__invalid__"
                  textValue={`${trimmed} ${rejection}`}
                  isDisabled
                  className="cv-menu__item cv-tag-picker__item cv-tag-picker__item--invalid"
                >
                  <span className="cv-listbox__label">{rejection}</span>
                </MenuItem>
              </>
            ) : null}
          </Menu>
        </Autocomplete>
      </Popover>
    </MenuTrigger>
  );
  return label ? (
    <div className="cv-field cv-tag-picker--field">
      <span className="cv-field__label">{label}</span>
      {picker}
      {description && (
        <span id={descriptionId} className="cv-field__description">
          {description}
        </span>
      )}
    </div>
  ) : (
    picker
  );
}
