import type {
  ClaimStatus,
  NodeType,
  RelationStatus,
  RelationType,
  TraversalDirection,
} from "../../api/graph";

export const graphModes = ["global", "local", "path"] as const;
export type GraphMode = (typeof graphModes)[number];

export const graphNodeTypes = ["CLAIM", "TOPIC"] as const satisfies readonly NodeType[];
export const graphRelationTypes = [
  "BELONGS_TO",
  "CITES",
  "COMPLEMENTS",
  "CONFLICTS_WITH",
  "DERIVED_FROM",
  "DUPLICATES",
  "IMPACTS",
  "PREREQUISITE_OF",
  "SUPPORTS",
  "VERSION_OF",
] as const satisfies readonly RelationType[];
export const graphRelationStatuses = ["CONFIRMED", "STALE"] as const satisfies readonly RelationStatus[];
export const graphFilterClaimStatuses = ["CONFIRMED", "DISPUTED"] as const satisfies readonly ClaimStatus[];
export type GraphFilterClaimStatus = (typeof graphFilterClaimStatuses)[number];
export const graphTraversalDirections = ["BOTH", "OUTBOUND", "INBOUND"] as const satisfies readonly TraversalDirection[];
export const graphDepths = [1, 2, 3] as const;
export type GraphDepth = (typeof graphDepths)[number];

export const defaultGraphRelationStatuses = ["CONFIRMED"] as const satisfies readonly RelationStatus[];
export const defaultGraphClaimStatuses = ["CONFIRMED", "DISPUTED"] as const satisfies readonly GraphFilterClaimStatus[];
