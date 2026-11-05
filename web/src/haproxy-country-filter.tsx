import { CountryFlag, TagPicker } from "@ecraela/ui";
import { Globe } from "@ecraela/ui/icons";
import { countryCodes, countryName } from "./countries";

export function HAProxyCountryFilter({
  value,
  onChange,
}: {
  value: string | undefined;
  onChange: (value: string) => void;
}) {
  const selected = value ? value.split(",") : [];
  const codes = [
    ...new Set([...countryCodes, ...selected.filter((c) => c !== "unknown")]),
  ];
  return (
    <TagPicker
      label="Countries"
      aria-label="Countries"
      emptyLabel="All countries"
      triggerLabel={
        selected.length
          ? `${selected.length} ${selected.length === 1 ? "country" : "countries"} selected`
          : "All countries"
      }
      searchLabel="Find countries"
      placeholder="Search country name or code"
      noResultsMessage="No matching countries"
      maxSelected={32}
      maxSelectedMessage="Select up to 32 countries."
      items={[
        { id: "unknown", label: "Unknown country", icon: <Globe /> },
        ...codes.map((code) => ({
          id: code,
          label: `${countryName(code)} (${code})`,
          icon: (
            <CountryFlag
              code={code}
              basePath={`${import.meta.env.BASE_URL}flags/4x3`}
            />
          ),
        })),
      ]}
      selectedKeys={new Set(selected)}
      onSelectionChange={(keys) => {
        if (keys === "all" || keys.size > 32) return;
        onChange([...keys].map(String).sort().join(","));
      }}
    />
  );
}
