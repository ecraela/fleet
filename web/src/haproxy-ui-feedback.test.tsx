import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  AccountMenu,
  DataTable,
  TablePagination,
  LoadingStatus,
  PaginatedDataTable,
  Section,
  InlineTip,
} from "@ecraela/ui";
describe("HAProxy feedback and bounded tables", () => {
  it("keeps already-loaded table rows while a refresh is pending", () => {
    const html = renderToStaticMarkup(
      <DataTable
        aria-label="Nodes"
        rows={[{ id: "edge-a" }]}
        columns={[{ id: "name", header: "Node", cell: (r) => r.id }]}
        isLoading
      />,
    );
    expect(html).toContain("edge-a");
    expect(html).not.toContain('data-key="loading-');
    expect(html).toContain('aria-busy="true"');
  });
  it("limits long local tables to ten initial rows and names their pagination", () => {
    const rows = Array.from({ length: 27 }, (_, i) => ({
      id: `node-${i + 1}`,
    }));
    const html = renderToStaticMarkup(
      <PaginatedDataTable
        aria-label="Nodes"
        itemLabel="nodes"
        rows={rows}
        columns={[{ id: "name", header: "Node", cell: (r) => r.id }]}
      />,
    );
    expect(html).toContain('data-key="node-10"');
    expect(html).not.toContain('data-key="node-11"');
    expect(html).toContain('aria-label="Nodes pagination"');
    expect(html).toContain("27");
  });
  it("does not add pagination chrome to a short table", () => {
    const html = renderToStaticMarkup(
      <PaginatedDataTable
        aria-label="Nodes"
        itemLabel="nodes"
        rows={[{ id: "edge-a" }]}
        columns={[{ id: "name", header: "Node", cell: (r) => r.id }]}
      />,
    );
    expect(html).not.toContain('aria-label="Nodes pagination"');
  });
  it("delays first-load announcements without discarding their layout space", () => {
    const html = renderToStaticMarkup(
      <LoadingStatus delayMs={1000}>Loading observations</LoadingStatus>,
    );
    expect(html).toContain('data-waiting="true"');
    expect(html).toContain('aria-hidden="true"');
    expect(html).not.toContain('role="status"');
  });
  it("supports a local account without inventing an email identity", () => {
    const html = renderToStaticMarkup(
      <AccountMenu user={{ name: "demo-admin" }} placement="bottom end">
        <></>
      </AccountMenu>,
    );
    expect(html).toContain('aria-label="demo-admin, account menu"');
    expect(html).toContain("demo-admin");
  });
  it("spaces section feedback in its explicit content stack", () => {
    const html = renderToStaticMarkup(
      <Section title="Traffic" layout="stack">
        <InlineTip>Previous results remain visible.</InlineTip>
        <p>Current results</p>
      </Section>,
    );
    expect(html).toContain('class="cv-section__content"');
    expect(html).toContain("Current results");
  });
});

it("keeps cursor pagination truthful and uses standard table navigation", () => {
  const render = (page: number, count: number, hasNextPage: boolean) =>
    renderToStaticMarkup(
      <TablePagination
        aria-label="Request pagination"
        page={page}
        totalItems={null}
        currentPageItems={count}
        hasNextPage={hasNextPage}
        itemLabel="recorded requests"
        onPageChange={() => {}}
      />,
    );
  const first = render(1, 10, true);
  const text = first.replace(/<[^>]*>/g, "");
  expect(text).toContain("Page 1 – 10 recorded requests");
  expect(text).not.toContain(" of ");
  const buttons = first.match(/<button\b[^>]*>[\s\S]*?<\/button>/g)!;
  expect(buttons[0]).toContain("disabled");
  expect(buttons[0]).toContain("Previous");
  expect(buttons[1]).not.toContain("disabled");
  expect(buttons[1]).toContain("Next");
  const last = render(2, 3, false).match(/<button\b[^>]*>[\s\S]*?<\/button>/g)!;
  expect(last[0]).not.toContain("disabled");
  expect(last[1]).toContain("disabled");
});
