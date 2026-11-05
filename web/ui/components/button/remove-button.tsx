import { Trash } from "../../icons";
import { Tooltip } from "../tooltip/tooltip";
import { IconButton } from "./button";

export interface RemoveButtonProps {
  /** Names what is removed, e.g. "Remove pool eu-primary". Used as the accessible name and tooltip. */
  label: string;
  onPress: () => void;
  /** Why removal is unavailable; keeps the reason reachable by keyboard and tooltip. */
  disabledReason?: string | undefined;
}

/**
 * The one way to remove an item from an editable list or group: a ghost
 * 16px trash icon button whose label says what is removed.
 */
export function RemoveButton({
  label,
  onPress,
  disabledReason,
}: RemoveButtonProps) {
  const button = (
    <IconButton
      aria-label={label}
      variant="ghost"
      disabledReason={disabledReason}
      onPress={onPress}
    >
      <Trash />
    </IconButton>
  );
  return disabledReason ? button : <Tooltip content={label}>{button}</Tooltip>;
}
