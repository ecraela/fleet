import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { GeographyDistribution, globePoint } from "@ecraela/ui";
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
});
