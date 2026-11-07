import { CountryFlag, TagPicker } from "@ecraela/ui";
import { countryCodes, countryName } from "./countries";

export function HAProxyTrafficCountryPicker({
  label,
  value,
  onChange,
  isDisabled,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  isDisabled: boolean;
}) {
  const selected = value.trim()
    ? value
        .trim()
        .split(/[\s,]+/)
        .map((code) => code.toUpperCase())
    : [];
  const codes = [...new Set([...countryCodes, ...selected])];
  return (
    <TagPicker
      label={label}
      emptyLabel="Select countries"
      triggerLabel={
        selected.length === 1
          ? `${countryName(selected[0]!)} (${selected[0]})`
          : selected.length > 1
            ? `${selected.length} countries selected`
            : "Select countries"
      }
      description="Search by country name or code. Requires Country data on every node."
      searchLabel="Find rule countries"
      placeholder="Search country name or code"
      noResultsMessage="No matching countries"
      maxSelected={16}
      maxSelectedMessage="A rule can match up to 16 countries."
      isDisabled={isDisabled}
      items={codes.map((code) => ({
        id: code,
        label: `${countryName(code)} (${code})`,
        icon: (
          <CountryFlag
            code={code}
            basePath={`${import.meta.env.BASE_URL}flags/4x3`}
          />
        ),
      }))}
      selectedKeys={new Set(selected)}
      onSelectionChange={(keys) => {
        if (isDisabled || keys === "all" || keys.size > 16) return;
        onChange([...keys].map(String).sort().join(", "));
      }}
    />
  );
}
