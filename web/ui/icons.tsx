import type { ComponentProps, ComponentType } from "react";
import {
  Adjustments as MedusaAdjustments,
  ArrowDownMini as MedusaArrowDownMini,
  ArrowDownTray as MedusaArrowDownTray,
  ArrowLeft as MedusaArrowLeft,
  ArrowPath as MedusaArrowPath,
  ArrowRight as MedusaArrowRight,
  ArrowUpDown as MedusaArrowUpDown,
  ArrowUpMini as MedusaArrowUpMini,
  ArrowUpRightMini as MedusaArrowUpRightMini,
  ArrowUpRightOnBox as MedusaArrowUpRightOnBox,
  ArrowUpTray as MedusaArrowUpTray,
  BarsThree as MedusaBarsThree,
  BellAlert as MedusaBellAlert,
  BookOpen as MedusaBookOpen,
  Brackets as MedusaBrackets,
  Calendar as MedusaCalendar,
  ChartActivity as MedusaChartActivity,
  ChatBubble as MedusaChatBubble,
  Check as MedusaCheck,
  CheckCircle as MedusaCheckCircle,
  ChevronDown as MedusaChevronDown,
  ChevronLeft as MedusaChevronLeft,
  ChevronRight as MedusaChevronRight,
  ChevronUpDown as MedusaChevronUpDown,
  CircleDottedLine as MedusaCircleDottedLine,
  CloudArrowDown as MedusaCloudArrowDown,
  Code as MedusaCode,
  CodeCommit as MedusaCodeCommit,
  CodeCompare as MedusaCodeCompare,
  CogSixTooth as MedusaCogSixTooth,
  Collapse as MedusaCollapse,
  ComputerDesktop as MedusaComputerDesktop,
  CreditCard as MedusaCreditCard,
  Directions as MedusaDirections,
  DotsSix as MedusaDotsSix,
  EllipsisHorizontal as MedusaEllipsisHorizontal,
  Envelope as MedusaEnvelope,
  ExclamationCircle as MedusaExclamationCircle,
  Funnel as MedusaFunnel,
  Globe as MedusaGlobe,
  GridLayout as MedusaGridLayout,
  History as MedusaHistory,
  InformationCircle as MedusaInformationCircle,
  Key as MedusaKey,
  Lifebuoy as MedusaLifebuoy,
  LightBulb as MedusaLightBulb,
  Link as MedusaLink,
  ListCheckbox as MedusaListCheckbox,
  MagnifyingGlass as MedusaMagnifyingGlass,
  Minus as MedusaMinus,
  Moon as MedusaMoon,
  OpenRectArrowOut as MedusaOpenRectArrowOut,
  PaperPlane as MedusaPaperPlane,
  PencilSquare as MedusaPencilSquare,
  Phone as MedusaPhone,
  Plug as MedusaPlug,
  Plus as MedusaPlus,
  QuestionMarkCircle as MedusaQuestionMarkCircle,
  ScrollText as MedusaScrollText,
  Server as MedusaServer,
  ShieldCheck as MedusaShieldCheck,
  Spinner as MedusaSpinner,
  SquareTwoStack as MedusaSquareTwoStack,
  Sun as MedusaSun,
  Tag as MedusaTag,
  Trash as MedusaTrash,
  Unfold as MedusaUnfold,
  Users as MedusaUsers,
  XCircle as MedusaXCircle,
  XMark as MedusaXMark,
} from "@medusajs/icons";
import { cx } from "./utils/cx";

/**
 * The console's icon set: Medusa UI icons (https://docs.medusajs.com/ui/icons/overview),
 * curated to the glyphs this console uses. Import icons from here, never from
 * `@medusajs/icons` directly, so every icon is decorative by default and the
 * set stays reviewable. Add a glyph here before using it.
 */

type Glyph = typeof MedusaPlus;

export interface IconProps extends Omit<
  ComponentProps<Glyph>,
  "width" | "height" | "ref"
> {
  /** Rendered width and height in px. Glyphs are drawn on a 15px grid. */
  size?: number;
}

export type IconComponent = ComponentType<IconProps>;

/** Medusa glyphs are drawn on a 15px grid; render them there unless the context calls for another size. */
export const ICON_SIZE = 15;

function icon(Glyph: Glyph, name: string): IconComponent {
  function Icon({ size = ICON_SIZE, className, ...props }: IconProps) {
    const labelled =
      props["aria-label"] !== undefined ||
      props["aria-labelledby"] !== undefined;
    return (
      <Glyph
        aria-hidden={labelled ? undefined : true}
        focusable="false"
        {...props}
        className={cx("cv-icon", className)}
        width={size}
        height={size}
      />
    );
  }
  Icon.displayName = name;
  return Icon;
}

export const Adjustments = icon(MedusaAdjustments, "Adjustments");
export const ArrowDownMini = icon(MedusaArrowDownMini, "ArrowDownMini");
export const ArrowDownTray = icon(MedusaArrowDownTray, "ArrowDownTray");
export const ArrowLeft = icon(MedusaArrowLeft, "ArrowLeft");
export const ArrowPath = icon(MedusaArrowPath, "ArrowPath");
export const ArrowRight = icon(MedusaArrowRight, "ArrowRight");
export const ArrowUpDown = icon(MedusaArrowUpDown, "ArrowUpDown");
export const ArrowUpMini = icon(MedusaArrowUpMini, "ArrowUpMini");
export const ArrowUpRightMini = icon(
  MedusaArrowUpRightMini,
  "ArrowUpRightMini",
);
export const ArrowUpRightOnBox = icon(
  MedusaArrowUpRightOnBox,
  "ArrowUpRightOnBox",
);
export const ArrowUpTray = icon(MedusaArrowUpTray, "ArrowUpTray");
export const BarsThree = icon(MedusaBarsThree, "BarsThree");
export const BellAlert = icon(MedusaBellAlert, "BellAlert");
export const BookOpen = icon(MedusaBookOpen, "BookOpen");
export const Brackets = icon(MedusaBrackets, "Brackets");
export const Calendar = icon(MedusaCalendar, "Calendar");
export const ChartActivity = icon(MedusaChartActivity, "ChartActivity");
export const ChatBubble = icon(MedusaChatBubble, "ChatBubble");
export const Check = icon(MedusaCheck, "Check");
export const CheckCircle = icon(MedusaCheckCircle, "CheckCircle");
export const ChevronDown = icon(MedusaChevronDown, "ChevronDown");
export const ChevronLeft = icon(MedusaChevronLeft, "ChevronLeft");
export const ChevronRight = icon(MedusaChevronRight, "ChevronRight");
export const ChevronUpDown = icon(MedusaChevronUpDown, "ChevronUpDown");
export const CircleDottedLine = icon(
  MedusaCircleDottedLine,
  "CircleDottedLine",
);
export const CloudArrowDown = icon(MedusaCloudArrowDown, "CloudArrowDown");
export const Code = icon(MedusaCode, "Code");
export const CodeCommit = icon(MedusaCodeCommit, "CodeCommit");
export const CodeCompare = icon(MedusaCodeCompare, "CodeCompare");
export const CogSixTooth = icon(MedusaCogSixTooth, "CogSixTooth");
export const Collapse = icon(MedusaCollapse, "Collapse");
export const ComputerDesktop = icon(MedusaComputerDesktop, "ComputerDesktop");
export const CreditCard = icon(MedusaCreditCard, "CreditCard");
export const Directions = icon(MedusaDirections, "Directions");
export const DotsSix = icon(MedusaDotsSix, "DotsSix");
export const EllipsisHorizontal = icon(
  MedusaEllipsisHorizontal,
  "EllipsisHorizontal",
);
export const Envelope = icon(MedusaEnvelope, "Envelope");
export const ExclamationCircle = icon(
  MedusaExclamationCircle,
  "ExclamationCircle",
);
export const Funnel = icon(MedusaFunnel, "Funnel");
export const Globe = icon(MedusaGlobe, "Globe");
export const GridLayout = icon(MedusaGridLayout, "GridLayout");
export const History = icon(MedusaHistory, "History");
export const InformationCircle = icon(
  MedusaInformationCircle,
  "InformationCircle",
);
export const Key = icon(MedusaKey, "Key");
export const Lifebuoy = icon(MedusaLifebuoy, "Lifebuoy");
export const LightBulb = icon(MedusaLightBulb, "LightBulb");
export const Link = icon(MedusaLink, "Link");
export const ListCheckbox = icon(MedusaListCheckbox, "ListCheckbox");
export const MagnifyingGlass = icon(MedusaMagnifyingGlass, "MagnifyingGlass");
export const Minus = icon(MedusaMinus, "Minus");
export const Moon = icon(MedusaMoon, "Moon");
export const OpenRectArrowOut = icon(
  MedusaOpenRectArrowOut,
  "OpenRectArrowOut",
);
export const PaperPlane = icon(MedusaPaperPlane, "PaperPlane");
export const PencilSquare = icon(MedusaPencilSquare, "PencilSquare");
export const Phone = icon(MedusaPhone, "Phone");
export const Plug = icon(MedusaPlug, "Plug");
export const Plus = icon(MedusaPlus, "Plus");
export const QuestionMarkCircle = icon(
  MedusaQuestionMarkCircle,
  "QuestionMarkCircle",
);
export const ScrollText = icon(MedusaScrollText, "ScrollText");
export const Server = icon(MedusaServer, "Server");
export const ShieldCheck = icon(MedusaShieldCheck, "ShieldCheck");
export const Spinner = icon(MedusaSpinner, "Spinner");
export const SquareTwoStack = icon(MedusaSquareTwoStack, "SquareTwoStack");
export const Sun = icon(MedusaSun, "Sun");
export const Tag = icon(MedusaTag, "Tag");
export const Trash = icon(MedusaTrash, "Trash");
export const Unfold = icon(MedusaUnfold, "Unfold");
export const Users = icon(MedusaUsers, "Users");
export const XCircle = icon(MedusaXCircle, "XCircle");
export const XMark = icon(MedusaXMark, "XMark");
