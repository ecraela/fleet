import { ChevronRight } from "../../icons";
import { Link as AriaLink } from "react-aria-components";
import { cx } from "../../utils/cx";

export interface BreadcrumbItem {
  label: string;
  /** Omit (or pass an empty string) to render this crumb as the current page. */
  href?: string;
}

export interface BreadcrumbsProps {
  items: readonly BreadcrumbItem[];
  className?: string;
}

/** Trail of crumbs above a page's content, as in the console topbar. */
export function Breadcrumbs({ items, className }: BreadcrumbsProps) {
  return (
    <nav aria-label="Breadcrumb" className={cx("cv-breadcrumbs", className)}>
      <ol>
        {items.map((item, index) => (
          <li key={`${index}:${item.label}`}>
            {index > 0 ? <ChevronRight aria-hidden="true" /> : null}
            {item.href ? (
              <AriaLink href={item.href}>
                <span title={item.label}>{item.label}</span>
              </AriaLink>
            ) : (
              <span aria-current="page" title={item.label}>
                {item.label}
              </span>
            )}
          </li>
        ))}
      </ol>
    </nav>
  );
}
