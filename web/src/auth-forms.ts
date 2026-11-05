export function passwordChangeErrors(
  current: string,
  next: string,
  confirm: string,
) {
  return {
    current: current ? undefined : "Enter your current password.",
    next:
      [...next].length >= 15
        ? undefined
        : "Use at least 15 characters for your new password.",
    confirm: !confirm
      ? "Confirm your new password."
      : next !== confirm
        ? "The new passwords do not match."
        : undefined,
  };
}
