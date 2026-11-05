import { describe, expect, it } from "vitest";
import { passwordChangeErrors } from "./auth-forms";
describe("password-change form feedback", () => {
  it("attaches missing inputs and mismatch errors to the affected fields", () => {
    expect(Object.values(passwordChangeErrors("", "", "")).every(Boolean)).toBe(
      true,
    );
    expect(
      passwordChangeErrors(
        "current example",
        "long example password",
        "different",
      ).confirm,
    ).toContain("match");
  });
  it("counts Unicode characters consistently with the local password policy", () => {
    expect(
      passwordChangeErrors("current example", "🧩".repeat(8), "🧩".repeat(8))
        .next,
    ).toBeTruthy();
    expect(
      Object.values(
        passwordChangeErrors(
          "current example",
          "long example password",
          "long example password",
        ),
      ).some(Boolean),
    ).toBe(false);
  });
});
