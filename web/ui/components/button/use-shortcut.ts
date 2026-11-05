import { useEffect, type RefObject } from "react";

/**
 * Turns the keyboard hint chips on a button (e.g. `["⌘", "↩︎"]`) into a live
 * binding: while the button is mounted, enabled, and not hidden behind an
 * overlay, the matching keystroke clicks it. A synthetic click drives both
 * `onPress` and native form submission for `type="submit"` buttons.
 *
 * Bindings are document-wide. Buttons under an `aria-hidden` or `inert`
 * ancestor (everything outside an open modal) are skipped, so a dialog's
 * actions take precedence over the page behind it.
 *
 * `Esc` is deliberately not bound: dialogs already close on Escape, and
 * double-firing a cancel handler would be surprising.
 */
export function useShortcut(
  ref: RefObject<HTMLButtonElement | null>,
  shortcut: readonly string[] | undefined,
  isDisabled: boolean,
) {
  const matcher = toMatcher(shortcut);

  useEffect(() => {
    if (matcher === null || isDisabled) return;

    const onKeyDown = (event: KeyboardEvent) => {
      const button = ref.current;
      if (
        event.defaultPrevented ||
        button === null ||
        !button.isConnected ||
        button.disabled ||
        button.getAttribute("aria-disabled") === "true" ||
        button.closest('[aria-hidden="true"], [inert]') !== null ||
        !matcher(event)
      )
        return;
      event.preventDefault();
      button.click();
    };

    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [ref, matcher, isDisabled]);
}

type Matcher = (event: KeyboardEvent) => boolean;

const ENTER_KEYS = new Set(["↩︎", "↩", "↵", "⏎", "Enter"]);

function toMatcher(shortcut: readonly string[] | undefined): Matcher | null {
  if (shortcut === undefined || shortcut.length === 0) return null;
  const key = shortcut.at(-1) ?? "";
  const modifiers = new Set(shortcut.slice(0, -1));
  const wantsCommand = modifiers.has("⌘") || modifiers.has("Ctrl");

  if (ENTER_KEYS.has(key) && wantsCommand) {
    return commandEnter;
  }
  if (key === "⌫" && modifiers.size === 0) {
    return plainBackspace;
  }
  return null;
}

/** ⌘ on Apple platforms, Ctrl elsewhere. Either is accepted. */
function commandEnter(event: KeyboardEvent) {
  return (
    event.key === "Enter" &&
    (event.metaKey || event.ctrlKey) &&
    !event.altKey &&
    !event.shiftKey &&
    !event.repeat
  );
}

/** Backspace or Delete with no modifiers, unless the user is editing text. */
function plainBackspace(event: KeyboardEvent) {
  return (
    (event.key === "Backspace" || event.key === "Delete") &&
    !event.metaKey &&
    !event.ctrlKey &&
    !event.altKey &&
    !event.shiftKey &&
    !isEditing(event.target)
  );
}

function isEditing(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    target.isContentEditable
  );
}

/** Whether the current platform uses ⌘ as its primary modifier. */
export const isApplePlatform =
  typeof navigator !== "undefined" &&
  /mac|iphone|ipad|ipod/i.test(
    (navigator as { userAgentData?: { platform?: string } }).userAgentData
      ?.platform ?? navigator.platform,
  );
