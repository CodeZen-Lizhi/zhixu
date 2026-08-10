import { expect, test, type Page, type Request } from "@playwright/test";
import { once } from "node:events";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";

const workspaceA = "7a000000-0000-4000-8000-000000000001";
const workspaceB = "7a000000-0000-4000-8000-000000000005";
const answerId = "7a000000-0000-4000-8000-000000000002";

interface BrowserServerEvent {
  id: string;
  workspaceId: string;
}

interface BrowserConnection {
  close: () => void;
  done: Promise<void>;
}

interface BrowserConnectOptions {
  workspaceId: string;
  lastEventId?: string;
  baseUrl?: string;
  onRecoveryRequired: () => void;
  onEvent?: (event: BrowserServerEvent) => void | Promise<void>;
  onError?: (error: { code: string; retryable: boolean }) => void;
  onStateChange?: (state: string) => void;
}

type BrowserConnect = (options: BrowserConnectOptions) => BrowserConnection;
type TransportWindow = Window & {
  connectServerEvents?: BrowserConnect;
  connectPersistedServerEvents?: BrowserConnect;
};

interface RequestFact {
  workspaceId: string | null;
  queryCursor: string | null;
  eventFormat: string | null;
  headerCursor: string | undefined;
  cookie: string | undefined;
}

const eventEnvelope = (id: string, workspaceId: string): Record<string, unknown> => ({
  schema_version: 1,
  id,
  type: "answer.completed",
  occurred_at: "2026-08-10T12:00:00Z",
  workspace_id: workspaceId,
  resource_ref: `answer:${answerId}`,
  resource_version: 1,
  payload_summary: { answer_id: answerId, status: "completed" },
});

const sseMessage = (id: string, workspaceId: string): string =>
  `id: ${id}\ndata: ${JSON.stringify(eventEnvelope(id, workspaceId))}\n\n`;

const multilineSSEMessage = (id: string, workspaceId: string): string => {
  const json = JSON.stringify(eventEnvelope(id, workspaceId));
  const splitAt = json.indexOf('"id"');
  if (splitAt < 1) throw new Error("EventSource smoke fixture cannot split its envelope");
  return `: heartbeat\r\nid: ${id}\r\ndata: ${json.slice(0, splitAt)}\r\ndata: ${json.slice(splitAt)}\r\n\r\n`;
};

const requestFact = async (request: Request): Promise<RequestFact> => {
  const url = new URL(request.url());
  const headers = await request.allHeaders();
  return {
    workspaceId: url.searchParams.get("workspace_id"),
    queryCursor: url.searchParams.get("last_event_id"),
    eventFormat: url.searchParams.get("event_format"),
    headerCursor: headers["last-event-id"],
    cookie: headers.cookie,
  };
};

const loadTransport = async (page: Page): Promise<void> => {
  await page.route("**/__eventsource_smoke__", (route) => route.fulfill({
    status: 200,
    contentType: "text/html",
    body: `<!doctype html><html><body><script type="module">
      import { connectServerEvents } from "/src/events/server-events.ts";
      window.connectServerEvents = connectServerEvents;
      window.connectPersistedServerEvents = (options) => {
        const storageKey = "zhixu.event-cursor." + options.workspaceId;
        const onEvent = options.onEvent;
        return connectServerEvents({
          ...options,
          lastEventId: window.sessionStorage.getItem(storageKey) ?? undefined,
          onEvent: async (event) => {
            await onEvent?.(event);
            window.sessionStorage.setItem(storageKey, event.id);
          },
        });
      };
    </script></body></html>`,
  }));
  await page.goto("/__eventsource_smoke__");
  await page.waitForFunction(() =>
    typeof (window as TransportWindow).connectServerEvents === "function" &&
    typeof (window as TransportWindow).connectPersistedServerEvents === "function");
};

const captureRuntimeIssues = (page: Page, issues: string[]): void => {
  page.on("console", (message) => {
    if (message.type() === "warning" || message.type() === "error") {
      issues.push(`console.${message.type()}: ${message.text()}`);
    }
  });
  page.on("pageerror", (error) => issues.push(`pageerror: ${error.message}`));
};

const incomingHeader = (request: IncomingMessage, name: string): string | undefined => {
  const value = request.headers[name];
  return Array.isArray(value) ? value.join(", ") : value;
};

const closeSmokeServer = async (
  server: ReturnType<typeof createServer>,
  responses: Set<ServerResponse>,
): Promise<void> => {
  for (const response of responses) response.end();
  server.closeAllConnections();
  await new Promise<void>((resolve, reject) => {
    server.close((error) => {
      if (error === undefined) resolve();
      else reject(error);
    });
  });
};

test("Chromium 原生 EventSource 解析 heartbeat/多行 data，并在 EOF 重连携带 Last-Event-ID", async ({ page }) => {
  const runtimeIssues: string[] = [];
  const requests: RequestFact[] = [];
  const openResponses = new Set<ServerResponse>();
  let allowedOrigin = "";
  const server = createServer((request, response) => {
    const url = new URL(request.url ?? "/", "http://eventsource-smoke.invalid");
    if (url.pathname !== "/api/v1/events") {
      response.writeHead(404).end();
      return;
    }
    requests.push({
      workspaceId: url.searchParams.get("workspace_id"),
      queryCursor: url.searchParams.get("last_event_id"),
      eventFormat: url.searchParams.get("event_format"),
      headerCursor: incomingHeader(request, "last-event-id"),
      cookie: incomingHeader(request, "cookie"),
    });
    openResponses.add(response);
    request.once("close", () => openResponses.delete(response));
    response.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-store",
      "X-Accel-Buffering": "no",
      "Access-Control-Allow-Origin": allowedOrigin,
      "Access-Control-Allow-Credentials": "true",
    });
    if (requests.length === 1) {
      const frame = multilineSSEMessage("42", workspaceA);
      const splitAt = frame.indexOf("\r\n") + 1;
      response.write(frame.slice(0, splitAt));
      setTimeout(() => response.end(frame.slice(splitAt)), 10);
      return;
    }
    response.write(requests.length === 2
      ? sseMessage("43", workspaceA)
      : sseMessage("44", workspaceA));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("EventSource smoke server has no TCP address");
  const eventSourceBaseUrl = `http://127.0.0.1:${String(address.port)}`;

  captureRuntimeIssues(page, runtimeIssues);
  try {
    await loadTransport(page);
    allowedOrigin = new URL(page.url()).origin;
    await page.context().grantPermissions(["local-network-access"], { origin: allowedOrigin });
    await page.evaluate(() => {
      document.cookie = "eventsource_smoke=present; Path=/; SameSite=Strict";
    });

    const firstRun = await page.evaluate(async ({ workspaceId, baseUrl }) => {
      const connect = (window as TransportWindow).connectPersistedServerEvents;
      if (connect === undefined) throw new Error("persisted EventSource owner is not loaded");
      window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceId}`, "41");
      const events: string[] = [];
      const states: string[] = [];
      const connection = connect({
        workspaceId,
        baseUrl,
        onRecoveryRequired: () => undefined,
        onStateChange: (state) => states.push(state),
        onEvent: (event) => {
          events.push(event.id);
          if (event.id === "43") window.setTimeout(() => connection.close(), 0);
        },
      });
      window.setTimeout(() => connection.close(), 15_000);
      await connection.done;
      return { events, states };
    }, { workspaceId: workspaceA, baseUrl: eventSourceBaseUrl });

    expect(firstRun.events, JSON.stringify({ firstRun, requests, runtimeIssues })).toEqual(["42", "43"]);
    expect(firstRun.states).toContain("open");
    expect(firstRun.states).toContain("reconnecting");
    expect(requests).toHaveLength(2);
    expect(requests[0]).toMatchObject({
      workspaceId: workspaceA,
      queryCursor: "41",
      eventFormat: "message",
      headerCursor: undefined,
    });
    expect(requests[1]).toMatchObject({
      workspaceId: workspaceA,
      queryCursor: "41",
      eventFormat: "message",
      headerCursor: "42",
    });
    expect(requests[0]?.cookie).toContain("eventsource_smoke=present");
    expect(requests[1]?.cookie).toContain("eventsource_smoke=present");

    await page.reload();
    await page.waitForFunction(() =>
      typeof (window as TransportWindow).connectPersistedServerEvents === "function");
    const refreshRun = await page.evaluate(async ({ workspaceId, baseUrl }) => {
      const connect = (window as TransportWindow).connectPersistedServerEvents;
      if (connect === undefined) throw new Error("persisted EventSource owner is not loaded after reload");
      const events: string[] = [];
      const connection = connect({
        workspaceId,
        baseUrl,
        onRecoveryRequired: () => undefined,
        onEvent: (event) => {
          events.push(event.id);
          window.setTimeout(() => connection.close(), 0);
        },
      });
      await connection.done;
      return events;
    }, { workspaceId: workspaceA, baseUrl: eventSourceBaseUrl });

    expect(refreshRun).toEqual(["44"]);
    expect(requests).toHaveLength(3);
    expect(requests[2]).toMatchObject({
      workspaceId: workspaceA,
      queryCursor: "43",
      eventFormat: "message",
      headerCursor: undefined,
    });
    expect(runtimeIssues, runtimeIssues.join("\n")).toEqual([]);
  } finally {
    await closeSmokeServer(server, openResponses);
  }
});

test("Chromium 路径对非法 MessageEvent fail closed，fatal HTTP 只用短 Fetch probe 分类", async ({ page }) => {
  const runtimeIssues: string[] = [];
  const invalidRequests: RequestFact[] = [];
  captureRuntimeIssues(page, runtimeIssues);
  await page.route("**/api/v1/events?workspace_id=7a000000-0000-4000-8000-000000000001**", async (route) => {
    invalidRequests.push(await requestFact(route.request()));
    await route.fulfill({
      status: 200,
      headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-store" },
      body: `id: 42\ndata: ${JSON.stringify(eventEnvelope("43", workspaceA))}\n\n`,
    });
  });
  await loadTransport(page);

  const invalidResult = await page.evaluate(async (workspaceId) => {
    const connect = (window as TransportWindow).connectServerEvents;
    if (connect === undefined) throw new Error("native EventSource transport is not loaded");
    const errors: { code: string; retryable: boolean }[] = [];
    const connection = connect({
      workspaceId,
      onRecoveryRequired: () => undefined,
      onError: (error) => errors.push({ code: error.code, retryable: error.retryable }),
    });
    await connection.done;
    return errors;
  }, workspaceA);
  expect(invalidResult).toEqual([{ code: "INVALID_EVENT", retryable: false }]);
  expect(invalidRequests).toHaveLength(1);

  await page.unroute("**/api/v1/events?workspace_id=7a000000-0000-4000-8000-000000000001**");
  const fatalRequests: RequestFact[] = [];
  await page.route("**/api/v1/events?**", async (route) => {
    fatalRequests.push(await requestFact(route.request()));
    await route.fulfill({
      status: 401,
      headers: { "Content-Type": "application/json", "Cache-Control": "no-store" },
      body: JSON.stringify({
        error_code: "AUTH_UNAUTHORIZED",
        message: "认证已失效",
        retryable: true,
      }),
    });
  });
  const fatalResult = await page.evaluate(async (workspaceId) => {
    const connect = (window as TransportWindow).connectServerEvents;
    if (connect === undefined) throw new Error("native EventSource transport is not loaded");
    const errors: { code: string; retryable: boolean }[] = [];
    const connection = connect({
      workspaceId,
      lastEventId: "41",
      onRecoveryRequired: () => undefined,
      onError: (error) => errors.push({ code: error.code, retryable: error.retryable }),
    });
    await connection.done;
    return errors;
  }, workspaceA);

  expect(fatalRequests).toHaveLength(2);
  expect(fatalRequests[0]).toMatchObject({ queryCursor: "41", headerCursor: undefined });
  expect(fatalRequests[1]).toMatchObject({ queryCursor: "41", headerCursor: "41" });
  expect(fatalResult).toEqual([{ code: "HTTP_ERROR", retryable: false }]);
  expect(runtimeIssues).toHaveLength(2);
  expect(runtimeIssues.every((issue) => issue.includes("401 (Unauthorized)"))).toBe(true);
});

test("Workspace A 关闭后只建立 Workspace B 连接", async ({ page }) => {
  const runtimeIssues: string[] = [];
  const requests: RequestFact[] = [];
  captureRuntimeIssues(page, runtimeIssues);
  await page.route("**/api/v1/events?**", async (route) => {
    const fact = await requestFact(route.request());
    requests.push(fact);
    const workspaceId = fact.workspaceId;
    if (workspaceId !== workspaceA && workspaceId !== workspaceB) {
      await route.abort();
      return;
    }
    await route.fulfill({
      status: 200,
      headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-store" },
      body: sseMessage("42", workspaceId),
    });
  });
  await loadTransport(page);

  const result = await page.evaluate(async (workspaces) => {
    const connect = (window as TransportWindow).connectServerEvents;
    if (connect === undefined) throw new Error("native EventSource transport is not loaded");
    const delivered: string[] = [];
    const connectionA = connect({
      workspaceId: workspaces.a,
      onRecoveryRequired: () => undefined,
      onEvent: (event) => {
        delivered.push(`A:${event.workspaceId}`);
        window.setTimeout(() => connectionA.close(), 0);
      },
    });
    await connectionA.done;

    const connectionB = connect({
      workspaceId: workspaces.b,
      onRecoveryRequired: () => undefined,
      onEvent: (event) => {
        delivered.push(`B:${event.workspaceId}`);
        window.setTimeout(() => connectionB.close(), 0);
      },
    });
    await connectionB.done;
    return delivered;
  }, { a: workspaceA, b: workspaceB });

  expect(result).toEqual([`A:${workspaceA}`, `B:${workspaceB}`]);
  expect(requests.map((request) => request.workspaceId)).toEqual([workspaceA, workspaceB]);
  expect(runtimeIssues, runtimeIssues.join("\n")).toEqual([]);
});
