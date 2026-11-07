import {
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from "react";
import { Button } from "react-aria-components";
import { CountryLabel } from "../country-flag/country-flag";
import { SegmentedControl } from "../segmented-control/segmented-control";
import { countryCenters, landPoints } from "./world-data";

export interface GeographyDistributionProps {
  countries: readonly { country: string; requests: number }[];
  total: number;
  durationSeconds: number;
  flagBasePath?: string;
  moreCountries?: boolean;
}

/** Orthographic projection. Points on the far side are deliberately hidden. */
export function globePoint(
  lon: number,
  lat: number,
  center: readonly [number, number],
) {
  const rad = Math.PI / 180;
  const longitude = (lon - center[0]) * rad,
    latitude = lat * rad,
    tilt = center[1] * rad;
  const visible =
    Math.sin(tilt) * Math.sin(latitude) +
      Math.cos(tilt) * Math.cos(latitude) * Math.cos(longitude) >=
    0;
  return {
    x: 160 + 138 * Math.cos(latitude) * Math.sin(longitude),
    y:
      160 -
      138 *
        (Math.cos(tilt) * Math.sin(latitude) -
          Math.sin(tilt) * Math.cos(latitude) * Math.cos(longitude)),
    visible,
  };
}

/** Normalized camera coordinates; latitude stops short of the poles. */
export function rotateGlobe(
  center: readonly [number, number],
  longitude: number,
  latitude: number,
): [number, number] {
  return [
    ((((center[0] + longitude + 180) % 360) + 360) % 360) - 180,
    Math.max(-80, Math.min(80, center[1] + latitude)),
  ];
}
/** Grid paths use the same projection and hemisphere clipping as markers. */
export function globeGrid(center: readonly [number, number]): string[] {
  const paths: string[] = [];
  const add = (coordinates: [number, number][]) => {
    let path = "",
      drawing = false;
    for (const [lon, lat] of coordinates) {
      const point = globePoint(lon, lat, center);
      if (!point.visible) {
        drawing = false;
        continue;
      }
      path += `${drawing ? "L" : "M"}${point.x.toFixed(2)},${point.y.toFixed(2)}`;
      drawing = true;
    }
    if (path) paths.push(path);
  };
  for (let lon = -180; lon < 180; lon += 30)
    add(Array.from({ length: 61 }, (_, i) => [lon, -90 + i * 3]));
  for (let lat = -60; lat <= 60; lat += 30)
    add(Array.from({ length: 121 }, (_, i) => [-180 + i * 3, lat]));
  return paths;
}

export function GeographyDistribution({
  countries,
  total,
  durationSeconds,
  flagBasePath = "/flags/4x3",
  moreCountries = false,
}: GeographyDistributionProps) {
  const id = useId();
  const [mode, setMode] = useState("count");
  const [camera, setCamera] = useState<[number, number] | null>(null);
  const [dragging, setDragging] = useState(false);
  const drag = useRef<{
    id: number;
    x: number;
    y: number;
    center: readonly [number, number];
    scale: number;
  } | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const active =
    countries.find((row) => row.country === selected) ??
    countries.find((row) => countryCenters[row.country]);
  const center: readonly [number, number] = camera ??
    countryCenters[active?.country ?? ""] ?? [10, 20];
  const rotate = (longitude: number, latitude = 0) =>
    setCamera(rotateGlobe(center, longitude, latitude));
  const reset = () => {
    setCamera(null);
    setSelected(null);
  };
  const startDrag = (event: PointerEvent<SVGSVGElement>) => {
    if (!event.isPrimary || event.button !== 0) return;
    const width = event.currentTarget.getBoundingClientRect().width || 320;
    drag.current = {
      id: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      center,
      scale: 180 / width,
    };
    event.currentTarget.setPointerCapture(event.pointerId);
    setDragging(true);
  };
  const moveDrag = (event: PointerEvent<SVGSVGElement>) => {
    const start = drag.current;
    if (!start || start.id !== event.pointerId) return;
    setCamera(
      rotateGlobe(
        start.center,
        (start.x - event.clientX) * start.scale,
        (event.clientY - start.y) * start.scale,
      ),
    );
  };
  const endDrag = (event: PointerEvent<SVGSVGElement>) => {
    if (drag.current?.id !== event.pointerId) return;
    drag.current = null;
    setDragging(false);
    if (event.currentTarget.hasPointerCapture(event.pointerId))
      event.currentTarget.releasePointerCapture(event.pointerId);
  };
  const keyRotate = (event: KeyboardEvent<HTMLDivElement>) => {
    const movement: Record<string, [number, number]> = {
      ArrowLeft: [-15, 0],
      ArrowRight: [15, 0],
      ArrowUp: [0, 15],
      ArrowDown: [0, -15],
    };
    if (event.key === "Home") {
      event.preventDefault();
      reset();
    } else if (movement[event.key]) {
      event.preventDefault();
      rotate(...movement[event.key]!);
    }
  };
  const maximum = Math.max(1, ...countries.map((row) => row.requests));
  const format = (requests: number) =>
    new Intl.NumberFormat(undefined, {
      maximumFractionDigits: mode === "rps" ? 2 : 0,
    }).format(
      mode === "rps" ? requests / Math.max(1, durationSeconds) : requests,
    );
  const name = (code: string) =>
    code
      ? (new Intl.DisplayNames(undefined, { type: "region" }).of(code) ?? code)
      : "Unknown country";
  const unlisted = Math.max(
    0,
    total - countries.reduce((sum, row) => sum + row.requests, 0),
  );
  return (
    <section className="cv-geo-distribution" aria-labelledby={id}>
      <header>
        <div>
          <h3 id={id}>Request distribution</h3>
          <p>
            Recorded client countries
            {mode === "rps" ? " – average requests per second" : ""}
          </p>
        </div>
        <SegmentedControl
          aria-label="Distribution values"
          size="sm"
          selectedKey={mode}
          onSelectionChange={(key) => setMode(String(key))}
          options={[
            { id: "count", label: "Count" },
            { id: "rps", label: "RPS" },
          ]}
        />
      </header>
      <div className="cv-geo-distribution__content">
        <div className="cv-geo-distribution__map">
          <div
            className="cv-geo-distribution__viewport"
            tabIndex={0}
            role="group"
            aria-label="Interactive request globe"
            aria-describedby={`${id}-help`}
            aria-keyshortcuts="ArrowLeft ArrowRight ArrowUp ArrowDown Home"
            onKeyDown={keyRotate}
            data-dragging={dragging || undefined}
          >
            <svg
              viewBox="0 0 320 320"
              role="img"
              aria-label="Request countries, positioned at country label centres"
              onPointerDown={startDrag}
              onPointerMove={moveDrag}
              onPointerUp={endDrag}
              onPointerCancel={endDrag}
              onLostPointerCapture={() => {
                drag.current = null;
                setDragging(false);
              }}
            >
              <circle
                className="cv-geo-distribution__ocean"
                cx="160"
                cy="160"
                r="138"
              />
              <g className="cv-geo-distribution__grid" aria-hidden="true">
                {globeGrid(center).map((path, index) => (
                  <path key={index} d={path} />
                ))}
              </g>
              <g className="cv-geo-distribution__land" aria-hidden="true">
                {landPoints.map(([lon, lat], i) => {
                  const point = globePoint(lon, lat, center);
                  return point.visible ? (
                    <rect
                      key={i}
                      x={point.x - 0.8}
                      y={point.y - 0.8}
                      width="1.6"
                      height="1.6"
                      rx=".25"
                    />
                  ) : null;
                })}
              </g>
              {countries
                .filter(
                  (row) => countryCenters[row.country] && row.requests > 0,
                )
                .map((row) => {
                  const point = globePoint(
                    ...countryCenters[row.country]!,
                    center,
                  );
                  const radius = 2.5 + 4 * Math.sqrt(row.requests / maximum);
                  return point.visible ? (
                    <g key={row.country} data-country={row.country}>
                      <title>
                        {name(row.country)}: {format(row.requests)}
                        {mode === "rps" ? " requests/s" : " requests"} (country
                        estimate)
                      </title>
                      <circle
                        className="cv-geo-distribution__halo"
                        cx={point.x}
                        cy={point.y}
                        r={radius + 4}
                      />
                      <circle
                        className="cv-geo-distribution__marker"
                        cx={point.x}
                        cy={point.y}
                        r={radius}
                        data-selected={row === active || undefined}
                      />
                    </g>
                  ) : null;
                })}
            </svg>
          </div>
          <p id={`${id}-help`} className="cv-sr-only">
            Drag to rotate · Arrow keys to explore · Home to reset
          </p>
          <p>
            {active
              ? `${name(active.country)} – ${format(active.requests)} ${mode === "rps" ? "requests/s" : "requests"}`
              : countries.some((row) => row.country && row.requests > 0)
                ? "Countries remain listed; no centres at this map scale"
                : "No located requests"}
          </p>
        </div>
        <div className="cv-geo-distribution__ranking">
          {countries.length ? (
            <ol>
              {countries.map((row) => {
                const content = (
                  <>
                    <CountryLabel code={row.country} basePath={flagBasePath}>
                      <span
                        className="cv-geo-distribution__country-name"
                        title={name(row.country)}
                      >
                        {name(row.country)}
                      </span>
                    </CountryLabel>
                    <span
                      className="cv-geo-distribution__bar"
                      aria-hidden="true"
                    >
                      <span
                        style={{ width: `${(row.requests / maximum) * 100}%` }}
                      />
                    </span>
                    <span>{format(row.requests)}</span>
                  </>
                );
                return (
                  <li key={row.country || "unknown"}>
                    {countryCenters[row.country] ? (
                      <Button
                        className="cv-geo-distribution__row"
                        aria-label={`Focus ${name(row.country)} on the map`}
                        aria-pressed={row === active}
                        onPress={() => {
                          setSelected(row.country);
                          setCamera(null);
                        }}
                      >
                        {content}
                      </Button>
                    ) : (
                      <div className="cv-geo-distribution__row" data-static>
                        {content}
                      </div>
                    )}
                  </li>
                );
              })}
            </ol>
          ) : (
            <p>No recorded requests in this range.</p>
          )}
          {unlisted > 0 && (
            <p>
              {new Intl.NumberFormat().format(unlisted)} requests in other
              groups{moreCountries ? " outside the top ten" : ""}.
            </p>
          )}
          <p>
            Markers show country label centres, not visitor coordinates. Unknown
            countries stay in the totals and have no map marker.
          </p>
        </div>
      </div>
    </section>
  );
}
