import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import {
  ConnectionIssue,
  connectionHealth,
  updateConnectionIssues,
  CONNECTION_FAILURE_DELAY,
  type ConnectionIssues,
} from "./connection-health";

describe("connection health", () => {
  it("gives transient manager failures 30 seconds before showing disconnected", () => {
    const issues = updateConnectionIssues(
      {},
      {
        id: "fleet",
        message: "Inventory unavailable",
        connection: true,
        now: 1000,
      },
    );
    expect(
      connectionHealth(issues, 1000 + CONNECTION_FAILURE_DELAY - 1)
        .disconnected,
    ).toBe(false);
    expect(
      connectionHealth(issues, 1000 + CONNECTION_FAILURE_DELAY).disconnected,
    ).toBe(true);
    const recovered = updateConnectionIssues(issues, {
      id: "fleet",
      message: null,
      connection: true,
      now: 32000,
    });
    expect(connectionHealth(recovered, 32000)).toEqual({
      messages: [],
      disconnected: false,
    });
  });
  it("does not reset the outage timer on repeated or changing failures", () => {
    let issues: ConnectionIssues = {};
    issues = updateConnectionIssues(issues, {
      id: "fleet",
      message: "Timeout",
      connection: true,
      now: 1000,
    });
    issues = updateConnectionIssues(issues, {
      id: "fleet",
      message: "Unavailable",
      connection: true,
      now: 20000,
    });
    expect(issues.fleet?.since).toBe(1000);
    expect(connectionHealth(issues, 31000).disconnected).toBe(true);
  });
  it("keeps resource failures separate and never calls them a disconnected manager", () => {
    let issues: ConnectionIssues = {};
    issues = updateConnectionIssues(issues, {
      id: "history",
      message: "History unavailable",
      connection: false,
      now: 0,
    });
    issues = updateConnectionIssues(issues, {
      id: "requests",
      message: "Requests unavailable",
      connection: false,
      now: 0,
    });
    issues = updateConnectionIssues(issues, {
      id: "history",
      message: null,
      connection: false,
      now: 90000,
    });
    expect(connectionHealth(issues, 90000)).toEqual({
      messages: ["Requests unavailable"],
      disconnected: false,
    });
    expect(
      updateConnectionIssues(issues, {
        id: "unknown",
        message: null,
        connection: false,
        now: 90000,
      }),
    ).toBe(issues);
  });
  it("reports background failures without adding layout content", () => {
    expect(
      renderToStaticMarkup(
        createElement(ConnectionIssue, { message: "Refresh failed" }),
      ),
    ).toBe("");
  });
});
