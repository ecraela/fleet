import { useEffect, useId, useRef, useState } from "react";
import { Slider, SliderThumb, SliderTrack } from "react-aria-components";
import {
  chartColors,
  seriesSegments,
  seriesPath,
  seriesArea,
  type ChartColor,
} from "./series-path";

export interface TimeSeries {
  id: string;
  label: string;
  values: ReadonlyArray<readonly [timestamp: number, value: number]>;
  tone?: "neutral" | "muted" | "success" | "warning" | "error" | "info";
  color?: ChartColor;
}

export interface TimeSeriesChartProps {
  title: string;
  unit: string;
  start: number;
  end: number;
  step: number;
  series: readonly TimeSeries[];
  formatValue?: (value: number) => string;
  emptyMessage?: string;
  variant?: "plain" | "panel";
  plot?: "line" | "bars";
}

function compact(value: number): string {
  return new Intl.NumberFormat("en", {
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(value);
}

/** Seconds on the x axis. Missing evaluations break the line rather than imply zero. */
export function TimeSeriesChart({
  title,
  unit,
  start,
  end,
  step,
  series,
  formatValue = compact,
  emptyMessage = "No observations",
  variant = "plain",
  plot = "line",
}: TimeSeriesChartProps) {
  const id = useId();
  const ref = useRef<HTMLElement>(null);
  const [width, setWidth] = useState(480);
  const [inspected, setInspected] = useState<number | null>(null);
  const [inspectionFocused, setInspectionFocused] = useState(false);
  const [pointerY, setPointerY] = useState(48);
  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => {
      if (entry) setWidth(Math.max(180, entry.contentRect.width));
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const left = 64,
    right = width - 12,
    top = 28,
    bottom = 174;
  const lines = series.map((line, index) => ({
    ...line,
    color:
      line.color ??
      (variant === "panel" && (!line.tone || line.tone === "neutral")
        ? chartColors[index % chartColors.length]
        : undefined),
    segments: seriesSegments(line.values, start, end, step),
  }));
  const totals = new Map<number, number>();
  for (const line of lines)
    for (const [at, value] of line.segments.flat())
      totals.set(at, (totals.get(at) ?? 0) + value);
  const maximum =
    Math.max(
      1,
      ...(plot === "bars"
        ? [...totals.values()]
        : lines.flatMap((line) =>
            line.segments.flat().map(([, value]) => value),
          )),
    ) * 1.1;
  const x = (at: number) =>
    left + ((at - start) / Math.max(1, end - start)) * (right - left);
  const y = (value: number) => bottom - (value / maximum) * (bottom - top);
  const count = Math.max(1, Math.round((end - start) / step));
  const latest = Math.max(
    start,
    ...lines.flatMap((line) => line.segments.flat().map(([stamp]) => stamp)),
  );
  const latestIndex = Math.min(
    count,
    Math.max(0, Math.round((latest - start) / step)),
  );
  const index = Math.min(count, inspected ?? latestIndex);
  const at = start + index * step;
  const time = (seconds: number, axis = false) =>
    new Intl.DateTimeFormat(undefined, {
      ...(!axis || end - start < 86400
        ? { hour: "2-digit", minute: "2-digit" }
        : {}),
      ...(end - start >= 86400 ? { day: "numeric", month: "short" } : {}),
    }).format(new Date(seconds * 1000));
  const readouts = lines.map((line) => ({
    ...line,
    value:
      line.segments
        .flat()
        .find(([stamp]) => Math.abs(stamp - at) < step / 2)?.[1] ?? null,
  }));
  const hasData = lines.some((line) => line.segments.length > 0);
  const stacked = new Map<number, number>();
  const barWidth = Math.max(
    1,
    Math.min(24, (step / Math.max(1, end - start)) * (right - left) * 0.8),
  );
  return (
    <figure
      className="cv-time-series"
      data-variant={variant}
      data-plot={plot}
      ref={ref}
    >
      <div className="cv-time-series__header">
        <h3 id={id} className="cv-time-series__title">
          {title}
        </h3>
        <span className="cv-time-series__unit">{unit}</span>
      </div>
      <div className="cv-time-series__canvas">
        <svg
          className="cv-time-series__plot"
          viewBox={`0 0 ${width} 208`}
          role="img"
          aria-labelledby={`${id} ${id}-description`}
          onPointerMove={(event) => {
            const bounds = event.currentTarget.getBoundingClientRect();
            const pointer =
              ((event.clientX - bounds.left) * width) / bounds.width;
            if (pointer < left || pointer > right) {
              setInspected(null);
              return;
            }
            setPointerY(((event.clientY - bounds.top) * 208) / bounds.height);
            setInspected(
              Math.max(
                0,
                Math.min(
                  count,
                  Math.round(((pointer - left) / (right - left)) * count),
                ),
              ),
            );
          }}
          onPointerLeave={() => setInspected(null)}
        >
          <desc id={`${id}-description`}>
            Historical {title.toLowerCase()} from{" "}
            {new Date(start * 1000).toLocaleString()} to{" "}
            {new Date(end * 1000).toLocaleString()}. Gaps mean observations are
            missing. Use the time slider to inspect values.
          </desc>
          {[0, 0.5, 1].map((fraction) => (
            <g key={fraction}>
              <line
                className="cv-time-series__grid"
                x1={left}
                x2={right}
                y1={y(maximum * fraction)}
                y2={y(maximum * fraction)}
              />
              <text
                className="cv-time-series__axis"
                x={left - 8}
                y={y(maximum * fraction) + 4}
                textAnchor="end"
              >
                {compact(maximum * fraction)}
              </text>
            </g>
          ))}
          {[0, 0.5, 1]
            .filter(
              (value) => (width >= 360 && end - start < 86400) || value !== 0.5,
            )
            .map((fraction) => (
              <text
                className="cv-time-series__axis"
                key={fraction}
                x={x(start + (end - start) * fraction)}
                y={199}
                textAnchor={
                  fraction === 0 ? "start" : fraction === 1 ? "end" : "middle"
                }
              >
                {time(start + (end - start) * fraction, true)}
              </text>
            ))}
          <defs>
            <clipPath id={`${id}-clip`}>
              <rect
                x={left - 2}
                y={top}
                width={right - left + 4}
                height={bottom - top + 2}
              />
            </clipPath>
          </defs>
          <g clipPath={`url(#${id}-clip)`}>
            {lines.map((line, index) => (
              <g
                key={line.id}
                className="cv-time-series__line"
                data-tone={line.tone ?? "neutral"}
                data-color={line.color}
              >
                {plot === "bars" ? (
                  line.segments.flat().map(([stamp, value]) => {
                    const base = stacked.get(stamp) ?? 0;
                    stacked.set(stamp, base + value);
                    return value > 0 ? (
                      <rect
                        key={stamp}
                        x={x(stamp) - barWidth / 2}
                        y={y(base + value)}
                        width={barWidth}
                        height={y(base) - y(base + value)}
                        fill="currentColor"
                      />
                    ) : null;
                  })
                ) : (
                  <>
                    {variant === "panel" &&
                      line.segments.map((points, segment) => (
                        <path
                          key={segment}
                          d={seriesArea(points, x, y, bottom)}
                          fill="currentColor"
                          className="cv-time-series__area"
                        />
                      ))}
                    <path
                      d={line.segments
                        .map((points) => seriesPath(points, x, y))
                        .join(" ")}
                      fill="none"
                      stroke="currentColor"
                      strokeWidth={variant === "panel" ? 2 : 1.5}
                      strokeDasharray={
                        index % 3 === 1
                          ? "5 3"
                          : index % 3 === 2
                            ? "2 3"
                            : undefined
                      }
                    />
                    {line.segments
                      .filter((points) => points.length === 1)
                      .map((points, segment) => (
                        <circle
                          key={segment}
                          cx={x(points[0]![0])}
                          cy={y(points[0]![1])}
                          r={2}
                          fill="currentColor"
                        />
                      ))}
                  </>
                )}
              </g>
            ))}
          </g>
          {(inspected !== null || inspectionFocused) && hasData && (
            <g className="cv-time-series__inspection">
              <line
                className="cv-time-series__guide"
                x1={x(at)}
                x2={x(at)}
                y1={top}
                y2={bottom}
                vectorEffect="non-scaling-stroke"
              />
              {plot === "line" &&
                readouts
                  .filter((line) => line.value !== null)
                  .map((line) => (
                    <circle
                      key={line.id}
                      className="cv-time-series__point cv-time-series__line"
                      data-tone={line.tone ?? "neutral"}
                      data-color={line.color}
                      cx={x(at)}
                      cy={y(line.value!)}
                      r={3}
                      fill="currentColor"
                    />
                  ))}
            </g>
          )}
          {!hasData && (
            <text
              className="cv-time-series__axis"
              x={(left + right) / 2}
              y={(top + bottom) / 2}
              textAnchor="middle"
            >
              {emptyMessage}
            </text>
          )}
        </svg>
        {(inspected !== null || inspectionFocused) && hasData && (
          <div
            className="cv-time-series__tooltip"
            aria-hidden="true"
            style={{
              left: `clamp(var(--cv-space-3), calc(${(x(at) / width) * 100}% + var(--cv-space-3)), calc(100% - var(--cv-space-3) - var(--cv-space-8) * 7))`,
              top: Math.max(12, Math.min(90, pointerY + 12)),
            }}
          >
            <time dateTime={new Date(at * 1000).toISOString()}>
              {new Date(at * 1000).toLocaleString(undefined, {
                day: "numeric",
                month: "short",
                hour: "2-digit",
                minute: "2-digit",
                second: "2-digit",
              })}
            </time>
            <dl>
              {readouts.map((line) => (
                <div key={line.id}>
                  <dt>
                    <span
                      className="cv-time-series__dot cv-time-series__line"
                      data-tone={line.tone ?? "neutral"}
                      data-color={line.color}
                    />
                    <span className="cv-time-series__label" title={line.label}>
                      {line.label}
                    </span>
                  </dt>
                  <dd>
                    {line.value === null
                      ? "No observation"
                      : formatValue(line.value)}
                  </dd>
                </div>
              ))}
            </dl>
          </div>
        )}
      </div>
      {hasData && (
        <Slider
          aria-label={`Inspect ${title.toLowerCase()}`}
          className="cv-time-series__slider"
          minValue={0}
          maxValue={count}
          step={1}
          value={index}
          onChange={(value) => setInspected(Number(value))}
        >
          <SliderTrack className="cv-time-series__track">
            <SliderThumb
              className="cv-time-series__thumb"
              onFocus={() => setInspectionFocused(true)}
              onBlur={() => setInspectionFocused(false)}
              aria-describedby={`${id}-readout`}
            />
          </SliderTrack>
        </Slider>
      )}
      <figcaption
        id={`${id}-readout`}
        className="cv-time-series__caption"
        aria-live={inspectionFocused ? "polite" : "off"}
        aria-atomic="true"
      >
        {hasData ? (
          <span>
            {inspected === null ? "Latest observation" : "Selected time"}{" "}
            <time dateTime={new Date(at * 1000).toISOString()}>{time(at)}</time>
          </span>
        ) : (
          <span>{emptyMessage}</span>
        )}
        <dl className="cv-time-series__legend">
          {readouts.map((line, index) => (
            <div key={line.id} className="cv-time-series__legend-item">
              <dt>
                <svg
                  className="cv-time-series__swatch cv-time-series__line"
                  data-tone={line.tone ?? "neutral"}
                  data-color={line.color}
                  viewBox="0 0 18 8"
                  aria-hidden="true"
                >
                  <path
                    d="M0,4 H18"
                    stroke="currentColor"
                    strokeWidth={1.5}
                    strokeDasharray={
                      index % 3 === 1
                        ? "5 3"
                        : index % 3 === 2
                          ? "2 3"
                          : undefined
                    }
                  />
                </svg>
                <span className="cv-time-series__label" title={line.label}>
                  {line.label}
                </span>
              </dt>
              <dd>
                {line.value === null
                  ? "No observation"
                  : formatValue(line.value)}
              </dd>
            </div>
          ))}
        </dl>
      </figcaption>
    </figure>
  );
}
