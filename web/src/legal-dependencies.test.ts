import { createRequire } from "node:module";
import { expect, it } from "vitest";
import { legalDependencies } from "../vite.config";

const resolve = createRequire(import.meta.url).resolve;

it("accepts a reviewed runtime package in rendered output", () => {
  const plugin = legalDependencies();
  expect(() =>
    plugin.generateBundle(
      {},
      {
        main: { type: "chunk", modules: { [resolve("react")]: {} } },
      },
    ),
  ).not.toThrow();
});

it("rejects accidentally shipping a build-only dependency without its notice", () => {
  const plugin = legalDependencies();
  expect(() =>
    plugin.generateBundle(
      {},
      {
        main: { type: "chunk", modules: { [resolve("vite")]: {} } },
      },
    ),
  ).toThrow("Missing reviewed distribution notice for vite@");
});
