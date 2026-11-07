import {
  countryCenters,
  landPoints,
} from "../ui/components/geography-distribution/world-data";
import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import {
  GeographyDistribution,
  globePoint,
  rotateGlobe,
  globeGrid,
} from "@ecraela/ui";
describe("country distribution", () => {
  it("projects the selected country and hides the far side", () => {
    expect(globePoint(10, 20, [10, 20])).toEqual({
      x: 160,
      y: 160,
      visible: true,
    });
    expect(globePoint(-170, -20, [10, 20]).visible).toBe(false);
  });
  it("keeps unknown requests in the list without inventing a location", () => {
    const html = renderToStaticMarkup(
      <GeographyDistribution
        countries={[{ country: "", requests: 42 }]}
        total={42}
        durationSeconds={900}
      />,
    );
    expect(html).toContain("Unknown country");
    expect(html).toContain("No located requests");
    expect(html).not.toContain("cv-geo-distribution__marker");
    expect(html).toContain("42");
  });
  it("keeps omitted groups in the totals", () => {
    const html = renderToStaticMarkup(
      <GeographyDistribution
        countries={[{ country: "NL", requests: 30 }]}
        total={50}
        durationSeconds={900}
        moreCountries
      />,
    );
    expect(html).toContain("20 requests in other groups");
    expect(html).toContain("outside the top ten");
  });
  it("keeps east/west and north/south aligned, including horizon points", () => {
    expect(globePoint(0, 0, [0, 0])).toEqual({ x: 160, y: 160, visible: true });
    expect(globePoint(90, 0, [0, 0]).x).toBeCloseTo(298);
    expect(globePoint(-90, 0, [0, 0]).x).toBeCloseTo(22);
    expect(globePoint(0, 90, [0, 0]).y).toBeCloseTo(22);
    expect(globePoint(0, -90, [0, 0]).y).toBeCloseTo(298);
    expect(globePoint(180, 0, [0, 0]).visible).toBe(false);
    expect(globePoint(181, 0, [0, 0]).x).toBeCloseTo(
      globePoint(-179, 0, [0, 0]).x,
    );
  });
  it("wraps longitude and clamps tilt so drag and keyboard rotations cannot flip the map", () => {
    expect(rotateGlobe([179, 79], 10, 50)).toEqual([-171, 80]);
    expect(rotateGlobe([-179, -79], -10, -50)).toEqual([171, -80]);
    expect(rotateGlobe([0, 0], 360 * 7, 0)).toEqual([0, 0]);
  });
  it("projects all bundled country centres and land points inside the globe at several camera angles", () => {
    for (const center of [
      [0, 0],
      [24.97, 45.73],
      [-97.48, 39.54],
      [179, -60],
    ] as const) {
      for (const coordinates of [
        ...Object.values(countryCenters),
        ...landPoints,
      ]) {
        expect(Math.abs(coordinates[0])).toBeLessThanOrEqual(180);
        expect(Math.abs(coordinates[1])).toBeLessThanOrEqual(90);
        const p = globePoint(...coordinates, center);
        expect(Number.isFinite(p.x) && Number.isFinite(p.y)).toBe(true);
        expect(Math.hypot(p.x - 160, p.y - 160)).toBeLessThanOrEqual(
          138.000001,
        );
      }
      for (const path of globeGrid(center))
        expect(path).not.toMatch(/NaN|Infinity/);
    }
  });
  it("uses the same country-centre projection for actual markers and preserves unknown totals", () => {
    const html = renderToStaticMarkup(
      <GeographyDistribution
        countries={[
          { country: "RO", requests: 12 },
          { country: "US", requests: 4 },
          { country: "", requests: 7 },
        ]}
        total={23}
        durationSeconds={900}
      />,
    );
    for (const country of ["RO", "US"]) {
      const group =
        html.match(
          new RegExp(`<g data-country="${country}">([\\s\\S]*?)</g>`),
        )?.[1] ?? "";
      const point = globePoint(...countryCenters[country]!, countryCenters.RO!);
      expect(point.visible).toBe(true);
      expect(group).toContain(`cx="${point.x}"`);
      expect(group).toContain(`cy="${point.y}"`);
    }
    expect(html).not.toContain('data-country=""');
    expect(html).toContain("Unknown country");
    expect(html).not.toContain("Rotate globe east");
    expect(html).not.toContain("Reset globe view");
    expect(html).toContain(
      'aria-keyshortcuts="ArrowLeft ArrowRight ArrowUp ArrowDown Home"',
    );
  });
});
