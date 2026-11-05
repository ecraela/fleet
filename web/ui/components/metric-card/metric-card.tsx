import { useId, type ReactNode } from "react";
import {
  seriesSegments,
  seriesPath,
  seriesArea,
  type ChartColor,
  type ChartPoint,
} from "../time-series-chart/series-path";

export interface MetricHistory {
  values: readonly ChartPoint[];
  start: number;
  end: number;
  step: number;
  label: string;
}

export function MetricGrid({
  children,
  columns = 4,
}: {
  children: ReactNode;
  columns?: 3 | 4;
}) {
  return (
    <dl className="cv-metric-grid" data-columns={columns}>
      {children}
    </dl>
  );
}

/** A measured value and its recorded history. No synthetic trend or interpolation. */
export function MetricCard({
  label,
  value,
  description,
  color = "blue",
  history,
}: {
  label: string;
  value: string | number | null;
  description?: ReactNode;
  color?: ChartColor;
  history?: MetricHistory | undefined;
}) {
  const id = useId();
  return (
    <div
      className="cv-metric-card"
      data-color={color}
      data-unavailable={value === null || undefined}
    >
      <dt className="cv-metric-card__label" id={id}>
        {label}
      </dt>
      <dd className="cv-metric-card__value">{value ?? "Not observed"}</dd>
      {description != null && (
        <dd className="cv-metric-card__description">{description}</dd>
      )}
      {history && (
        <dd className="cv-metric-card__history">
          <Sparkline history={history} color={color} />
        </dd>
      )}
    </div>
  );
}

/** Recorded history for a card or table cell, with gaps left visible. */
export function Sparkline({
  history,
  color = "blue",
  compact = false,
  summary,
  description,
}: {
  history: MetricHistory;
  color?: ChartColor;
  compact?: boolean;
  summary?: string;
  description?: string | undefined;
}) {
  const segments = seriesSegments(
    history.values,
    history.start,
    history.end,
    history.step,
  );
  const maximum =
    Math.max(1, ...segments.flat().map(([, value]) => value)) * 1.1;
  const x = (at: number) =>
    2 + ((at - history.start) / Math.max(1, history.end - history.start)) * 296;
  const y = (value: number) => 60 - (value / maximum) * 54;
  return (
    <div
      className="cv-sparkline"
      data-color={color}
      data-compact={compact || undefined}
      data-has-summary={summary != null || undefined}
    >
      {summary != null && (
        <span className="cv-sparkline__summary">{summary}</span>
      )}
      {segments.length ? (
        <svg
          viewBox="0 0 300 64"
          preserveAspectRatio="none"
          role="img"
          aria-label={history.label}
        >
          {segments.map((points, index) => (
            <g key={index}>
              <path
                d={seriesArea(points, x, y, 64)}
                className="cv-sparkline__area"
              />
              <path
                d={seriesPath(points, x, y)}
                fill="none"
                stroke="currentColor"
                strokeWidth={compact ? 1.5 : 2}
                vectorEffect="non-scaling-stroke"
              />
              {points.length === 1 && (
                <circle
                  cx={x(points[0]![0])}
                  cy={y(points[0]![1])}
                  r={compact ? 1.5 : 2}
                  fill="currentColor"
                />
              )}
            </g>
          ))}
        </svg>
      ) : (
        <span className="cv-sparkline__empty">
          {compact ? "No history yet" : "Waiting for recorded history"}
        </span>
      )}
      {description && (
        <span className="cv-sparkline__description">{description}</span>
      )}
    </div>
  );
}
