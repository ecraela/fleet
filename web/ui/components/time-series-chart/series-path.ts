export type ChartPoint = readonly [timestamp: number, value: number];
export type ChartColor = "blue" | "teal" | "violet" | "amber" | "rose" | "cyan";
export const chartColors: readonly ChartColor[] = [
  "blue",
  "teal",
  "violet",
  "amber",
  "rose",
  "cyan",
];

/** Keep missing buckets as gaps, including isolated observations. */
export function seriesSegments(
  values: readonly ChartPoint[],
  start: number,
  end: number,
  step: number,
): ChartPoint[][] {
  const points = values
    .filter(
      ([at, value]) =>
        Number.isFinite(at) &&
        Number.isFinite(value) &&
        value >= 0 &&
        at >= start &&
        at <= end,
    )
    .slice()
    .sort((a, b) => a[0] - b[0]);
  const segments: ChartPoint[][] = [];
  for (const point of points) {
    const last = segments[segments.length - 1];
    if (!last || point[0] - last[last.length - 1]![0] > Math.max(1, step) * 1.5)
      segments.push([point]);
    else last.push(point);
  }
  return segments;
}

export function seriesPath(
  points: readonly ChartPoint[],
  x: (at: number) => number,
  y: (value: number) => number,
) {
  return points
    .map(
      ([at, value], index) =>
        `${index ? "L" : "M"}${x(at).toFixed(2)},${y(value).toFixed(2)}`,
    )
    .join(" ");
}

export function seriesArea(
  points: readonly ChartPoint[],
  x: (at: number) => number,
  y: (value: number) => number,
  bottom: number,
) {
  if (points.length < 2) return "";
  return `${seriesPath(points, x, y)} L${x(points[points.length - 1]![0]).toFixed(2)},${bottom} L${x(points[0]![0]).toFixed(2)},${bottom} Z`;
}
