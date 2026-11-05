import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, useNavigate } from "react-router-dom";
import { RouterProvider } from "react-aria-components";
import { Link } from "@ecraela/ui";
import { useManagerHref } from "./router";
function Links() {
  return (
    <RouterProvider navigate={useNavigate()} useHref={useManagerHref}>
      <Link href="/applications/c/a/traffic?range=1h">Traffic</Link>
      <Link href="https://github.com/ecraela/fleet" isExternal>
        Source
      </Link>
    </RouterProvider>
  );
}
it("resolves deep internal links and preserves external documentation URLs", () => {
  const html = renderToStaticMarkup(
    <MemoryRouter initialEntries={["/infrastructure"]}>
      <Links />
    </MemoryRouter>,
  );
  expect(html).toContain('href="/applications/c/a/traffic?range=1h"');
  expect(html).toContain('href="https://github.com/ecraela/fleet"');
  expect(html).not.toContain("/infrastructure/https:");
});
