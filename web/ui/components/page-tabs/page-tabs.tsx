import type { ReactNode } from "react";
import { Link } from "react-aria-components";
import { cx } from "../../utils/cx";

export interface PageTab {
  id: string;
  label: string;
  /** Route for this view. Wrap the app in React Aria's `RouterProvider` for client-side routing. */
  href: string;
  /** Short state shown after the label, e.g. a `Badge` reading "Drift". */
  status?: ReactNode;
}

export interface PageTabsProps {
  "aria-label": string;
  items: readonly PageTab[];
  currentId: string;
  className?: string;
}

/**
 * Navigation between the views of one resource (for example Configuration,
 * Compare, Review and Deploy). Each tab is a link to its own URL; the current
 * view is marked with `aria-current="page"`. Use `ContentTabs` instead for
 * panels that switch within one page.
 */
export function PageTabs({
  "aria-label": label,
  items,
  currentId,
  className,
}: PageTabsProps) {
  return (
    <nav aria-label={label} className={cx("cv-page-tabs", className)}>
      <ul className="cv-page-tabs__list">
        {items.map((item) => (
          <li key={item.id}>
            <Link
              href={item.href}
              className="cv-page-tabs__tab"
              aria-current={item.id === currentId ? "page" : undefined}
            >
              <span>{item.label}</span>
              {item.status}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}
