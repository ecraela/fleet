import { useHref } from "react-router-dom";

export function useManagerHref(href: string) {
  const internal = useHref(href);
  return /^(?:[a-z][a-z0-9+.-]*:|\/\/)/i.test(href) ? href : internal;
}
