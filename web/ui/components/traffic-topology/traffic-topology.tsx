import { useState } from "react";
import { SearchField } from "../text-field/text-field";
import { Select } from "../select/select";
import { PaginatedDataTable } from "../table/paginated-table";
import { EmptyState } from "../feedback/feedback";
import { Badge, type BadgeTone } from "../badge/badge";
import { Button } from "../button/button";

export interface TrafficTopologyNode {
  id: string;
  label: string;
  address: string;
  status: string;
  tone: BadgeTone;
  description: string;
}

export interface TrafficTopologyProps {
  entrypoint: string;
  strategy: string;
  backend: string;
  nodes: readonly TrafficTopologyNode[];
  selectedId?: string | null;
  onSelect: (id: string) => void;
}

/** Declared routing paired with observed node state; edges do not imply health. */
export function TrafficTopology({
  entrypoint,
  strategy,
  backend,
  nodes,
  selectedId,
  onSelect,
}: TrafficTopologyProps) {
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("all");
  const useTable = nodes.length > 4;
  const statuses = [
    ...new Set([
      ...nodes.map((node) => node.status),
      ...(status === "all" ? [] : [status.slice(7)]),
    ]),
  ].sort();
  const filtered = nodes.filter(
    (node) =>
      (status === "all" || `status:${node.status}` === status) &&
      `${node.label} ${node.address}`
        .toLocaleLowerCase()
        .includes(query.trim().toLocaleLowerCase()),
  );
  return (
    <figure
      className="cv-traffic-topology"
      aria-label="Cluster traffic topology"
    >
      <div className="cv-traffic-topology__flow">
        <div className="cv-traffic-topology__endpoint">
          <span className="cv-traffic-topology__label">Entrypoint</span>
          <code>{entrypoint}</code>
          <span className="cv-traffic-topology__detail">{strategy}</span>
        </div>
        <div className="cv-traffic-topology__node-group">
          <div className="cv-traffic-topology__group-heading">
            <h3>HAProxy nodes</h3>
            <span>
              {nodes.length} {nodes.length === 1 ? "node" : "nodes"} in this
              route
            </span>
          </div>
          {useTable ? (
            <>
              <div className="cv-traffic-topology__filters">
                <SearchField
                  label="Find topology nodes"
                  value={query}
                  onChange={setQuery}
                />
                <Select
                  label="Node status"
                  selectedKey={status}
                  onSelectionChange={(value) => setStatus(String(value))}
                  options={[
                    { id: "all", label: "All statuses" },
                    ...statuses.map((label) => ({
                      id: `status:${label}`,
                      label,
                    })),
                  ]}
                />
              </div>
              <PaginatedDataTable
                aria-label="Traffic-serving nodes"
                itemLabel="nodes"
                paginationKey={`${query}/${status}`}
                rows={filtered}
                rowLabel={(node) => node.label}
                emptyState={
                  <EmptyState
                    size="sm"
                    title="No matching nodes"
                    description="Try a different name or address, or choose All statuses."
                  />
                }
                columns={[
                  {
                    id: "node",
                    header: "Node",
                    isRowHeader: true,
                    cell: (node) => (
                      <Button
                        variant="ghost"
                        aria-pressed={node.id === selectedId}
                        onPress={() => onSelect(node.id)}
                      >
                        {node.label}
                      </Button>
                    ),
                  },
                  {
                    id: "address",
                    header: "Address",
                    cell: (node) => <code>{node.address}</code>,
                  },
                  {
                    id: "status",
                    header: "Status",
                    cell: (node) => (
                      <Badge tone={node.tone}>{node.status}</Badge>
                    ),
                  },
                  {
                    id: "observation",
                    header: "Observation",
                    cell: (node) => node.description,
                  },
                ]}
              />
            </>
          ) : (
            <ul
              className="cv-traffic-topology__nodes"
              aria-label="Traffic-serving nodes"
            >
              {nodes.map((node) => (
                <li key={node.id}>
                  <Button
                    className="cv-traffic-topology__node"
                    variant="ghost"
                    aria-pressed={node.id === selectedId}
                    onPress={() => onSelect(node.id)}
                  >
                    <span className="cv-traffic-topology__node-body">
                      <span className="cv-traffic-topology__node-heading">
                        <strong>{node.label}</strong>
                        <Badge tone={node.tone}>{node.status}</Badge>
                      </span>
                      <code>{node.address}</code>
                      <span className="cv-traffic-topology__detail">
                        {node.description}
                      </span>
                    </span>
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="cv-traffic-topology__endpoint">
          <span className="cv-traffic-topology__label">Application</span>
          <code>{backend}</code>
          <span className="cv-traffic-topology__detail">Backend pool</span>
        </div>
      </div>
      <figcaption>
        Declared routing from the entrypoint through these HAProxy nodes to the
        backend pool. Node status comes from agent observations; entrypoint
        reachability is not monitored.
      </figcaption>
    </figure>
  );
}
