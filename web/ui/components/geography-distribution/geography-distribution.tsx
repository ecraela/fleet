import { useId, useState } from "react";
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

export function GeographyDistribution({
  countries,
  total,
  durationSeconds,
  flagBasePath = "/flags/4x3",
  moreCountries = false,
}: GeographyDistributionProps) {
  const id = useId();
  const [mode, setMode] = useState("count");
  const [selected, setSelected] = useState<string | null>(null);
  const active =
    countries.find((row) => row.country === selected) ??
    countries.find((row) => countryCenters[row.country]);
  const center = countryCenters[active?.country ?? ""] ?? [10, 20];
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
          <svg
            viewBox="0 0 320 320"
            role="img"
            aria-label={
              active
                ? `Country distribution, focused on ${name(active.country)}`
                : "Country distribution. No located requests."
            }
          >
            <circle
              className="cv-geo-distribution__ocean"
              cx="160"
              cy="160"
              r="138"
            />
            <g className="cv-geo-distribution__land">
              {landPoints.map(([lon, lat], i) => {
                const point = globePoint(lon, lat, center);
                return point.visible ? (
                  <circle key={i} cx={point.x} cy={point.y} r="1.25" />
                ) : null;
              })}
            </g>
            {countries
              .filter((row) => countryCenters[row.country])
              .map((row) => {
                const point = globePoint(
                  ...countryCenters[row.country]!,
                  center,
                );
                return point.visible ? (
                  <circle
                    key={row.country}
                    className="cv-geo-distribution__marker"
                    cx={point.x}
                    cy={point.y}
                    r={row === active ? 5 : 3}
                    data-selected={row === active || undefined}
                  >
                    <title>
                      {name(row.country)}: {format(row.requests)}
                      {mode === "rps" ? " requests/s" : " requests"}
                    </title>
                  </circle>
                ) : null;
              })}
          </svg>
          <p>
            {active
              ? `${name(active.country)} – ${format(active.requests)} ${mode === "rps" ? "requests/s" : "requests"}`
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
                        onPress={() => setSelected(row.country)}
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
            Country estimates, not visitor locations. Unknown countries stay in
            the totals and have no map marker.
          </p>
        </div>
      </div>
    </section>
  );
}
