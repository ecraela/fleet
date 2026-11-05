/** Wait for submitted validation to render, then focus the first affected control. */
export function focusFormError(form: HTMLFormElement | null) {
  requestAnimationFrame(() => {
    const control = form?.querySelector<HTMLElement>(
      'input[aria-invalid="true"], textarea[aria-invalid="true"], button[aria-invalid="true"]',
    );
    control?.focus({ preventScroll: true });
    (control?.closest(".cv-field, .cv-select") ?? control)?.scrollIntoView({
      block: "center",
      inline: "nearest",
      behavior: "instant",
    });
  });
}
