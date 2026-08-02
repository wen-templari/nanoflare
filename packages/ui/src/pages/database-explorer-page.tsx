import type { components } from "@nanoflare/schema";

import { Banner, Button, Dialog, Table, Text } from "@cloudflare/kumo";
import {
  ChevronLeft,
  ChevronRight,
  Database,
  Plus,
  Play,
  RefreshCw,
  Table2,
  X,
} from "lucide-react";
import { useEffect, useMemo, useState, type PointerEvent } from "react";
import { DataGrid, renderTextEditor, type Column as GridColumn } from "react-data-grid";
import { Navigate, useParams } from "react-router-dom";
import "react-data-grid/lib/styles.css";

import { apiClient, errorMessage } from "../app/api";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { useWorkspace } from "../app/workspace-context";
import { Input } from "../components/ui/input";

type DBQueryResponse = components["schemas"]["DBQueryResponse"];
type Row = Record<string, unknown>;
type GridRow = Row & { __gridIndex: number; __rowNumber: number };
type StagedChange = { rowKey: string; original: Row; values: Record<string, unknown> };

type Column = { name: string; type: string; primaryKey: boolean; notNull: boolean };
type TableTab = {
  id: string;
  kind: "table";
  table: string;
  columns: Column[];
  rows: Row[];
  changes: StagedChange[];
  page: number;
  loading: boolean;
  error?: string;
};
type QueryTab = {
  id: string;
  kind: "query";
  label: string;
  sql: string;
  rows: Row[];
  error?: string;
  running: boolean;
  summary?: string;
};
type ExplorerTab = TableTab | QueryTab;

const pageSize = 50;

export function DatabaseExplorerPage() {
  const { databaseId } = useParams();
  const { activeOrgID, apiConnected, databases, notify } = useWorkspace();
  const resourcesReady = useWorkspaceResources(["databases"], "details");
  const database = databases.find((item) => item.id === databaseId);
  const [tables, setTables] = useState<string[]>([]);
  const [tablesError, setTablesError] = useState("");
  const [tableFilter, setTableFilter] = useState("");
  const [tabs, setTabs] = useState<ExplorerTab[]>([]);
  const [activeTabID, setActiveTabID] = useState("");
  const [queryNumber, setQueryNumber] = useState(1);
  const [addTabID, setAddTabID] = useState<string>();
  const [addValues, setAddValues] = useState<Record<string, string>>({});
  const [mutationError, setMutationError] = useState("");
  const [mutating, setMutating] = useState(false);
  const [sidebarWidth, setSidebarWidth] = useState(256);

  async function execute(sql: string, params: unknown[] = []) {
    if (!apiConnected) throw new Error("API connection is required to explore data");
    const { data, error } = await apiClient.POST(
      "/v1/organizations/{orgID}/databases/{databaseID}/queries",
      {
        params: { path: { orgID: activeOrgID, databaseID: database!.id } },
        body: { sql, statements: [{ sql, params }] },
        parseAs: "json",
      },
    );
    if (error || !data) throw new Error(errorMessage(error, "Query failed"));
    return data;
  }

  useEffect(() => {
    if (!database || !apiConnected) return;
    let cancelled = false;
    void execute("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name;")
      .then((response) => {
        if (!cancelled) setTables(rowsFor(response).map((row) => String(row.name)));
      })
      .catch((error) => !cancelled && setTablesError(messageFor(error)))
      .finally(() => !cancelled && undefined);
    return () => {
      cancelled = true;
    };
  }, [activeOrgID, apiConnected, database?.id]);

  if (!resourcesReady) return null;
  if (!database) return <Navigate replace to="/databases" />;

  const visibleTables = tables.filter((table) =>
    table.toLowerCase().includes(tableFilter.toLowerCase()),
  );
  const activeTab = tabs.find((tab) => tab.id === activeTabID);
  const addTab = tabs.find((tab): tab is TableTab => tab.id === addTabID && tab.kind === "table");

  async function loadTable(tabID: string, table: string, page: number, existingColumns?: Column[]) {
    setTabs((current) =>
      current.map((tab) =>
        tab.id === tabID && tab.kind === "table"
          ? { ...tab, loading: true, error: undefined }
          : tab,
      ),
    );
    try {
      const columns = existingColumns?.length
        ? existingColumns
        : columnsFor(await execute(`PRAGMA table_info(${quoteIdentifier(table)});`));
      const response = await execute(`SELECT * FROM ${quoteIdentifier(table)} LIMIT ? OFFSET ?;`, [
        pageSize,
        page * pageSize,
      ]);
      const rows = rowsFor(response);
      setTabs((current) =>
        current.map((tab) =>
          tab.id === tabID && tab.kind === "table"
            ? {
                ...tab,
                columns,
                rows: applyStagedChanges(rows, columns, tab.changes),
                page,
                loading: false,
              }
            : tab,
        ),
      );
    } catch (error) {
      setTabs((current) =>
        current.map((tab) =>
          tab.id === tabID && tab.kind === "table"
            ? { ...tab, loading: false, error: messageFor(error) }
            : tab,
        ),
      );
    }
  }

  function openTable(table: string) {
    const existing = tabs.find((tab) => tab.kind === "table" && tab.table === table);
    if (existing) {
      setActiveTabID(existing.id);
      return;
    }
    const id = `table-${crypto.randomUUID()}`;
    setTabs((current) => [
      ...current,
      { id, kind: "table", table, columns: [], rows: [], changes: [], page: 0, loading: true },
    ]);
    setActiveTabID(id);
    void loadTable(id, table, 0);
  }

  function createQueryTab() {
    const id = `query-${crypto.randomUUID()}`;
    setTabs((current) => [
      ...current,
      { id, kind: "query", label: `Query ${queryNumber}`, sql: "", rows: [], running: false },
    ]);
    setQueryNumber((current) => current + 1);
    setActiveTabID(id);
  }

  function closeTab(id: string) {
    const index = tabs.findIndex((tab) => tab.id === id);
    const next = tabs.filter((tab) => tab.id !== id);
    setTabs(next);
    if (activeTabID === id) setActiveTabID(next[Math.max(0, index - 1)]?.id ?? "");
  }

  async function runQuery(tab: QueryTab) {
    if (!tab.sql.trim()) return notify("SQL is required");
    setTabs((current) =>
      current.map((item) =>
        item.id === tab.id && item.kind === "query"
          ? { ...item, running: true, error: undefined }
          : item,
      ),
    );
    try {
      const response = await execute(tab.sql);
      const result = response.results?.[0];
      const meta = result?.meta;
      setTabs((current) =>
        current.map((item) =>
          item.id === tab.id && item.kind === "query"
            ? {
                ...item,
                rows: rowsFor(response),
                running: false,
                summary: `${result?.results?.length ?? 0} rows · ${meta?.changes ?? 0} changes · ${formatDuration(meta?.duration ?? 0)}`,
              }
            : item,
        ),
      );
    } catch (error) {
      setTabs((current) =>
        current.map((item) =>
          item.id === tab.id && item.kind === "query"
            ? { ...item, running: false, error: messageFor(error) }
            : item,
        ),
      );
    }
  }

  function stageGridCell(tabID: string, row: Row, column: Column, value: unknown) {
    const tab = tabs.find((item): item is TableTab => item.id === tabID && item.kind === "table");
    if (!tab) return;
    const primaryKeys = tab.columns.filter((column) => column.primaryKey);
    const nextValue = typeof value === "string" ? parseValue(value) : value;
    setTabs((current) =>
      current.map((item) =>
        item.id === tab.id && item.kind === "table"
          ? stageCellChange(item, row, column, nextValue, primaryKeys)
          : item,
      ),
    );
  }

  async function commitTableChanges(tabID: string) {
    const tab = tabs.find((item): item is TableTab => item.id === tabID && item.kind === "table");
    if (!tab?.changes.length) return true;
    const primaryKeys = tab.columns.filter((column) => column.primaryKey);
    setMutating(true);
    setMutationError("");
    try {
      for (const change of tab.changes) {
        const columns = Object.keys(change.values);
        await execute(
          `UPDATE ${quoteIdentifier(tab.table)} SET ${columns.map((name) => `${quoteIdentifier(name)} = ?`).join(", ")} WHERE ${primaryKeys.map((primaryKey) => `${quoteIdentifier(primaryKey.name)} IS ?`).join(" AND ")};`,
          [
            ...columns.map((name) => change.values[name]),
            ...primaryKeys.map((primaryKey) => change.original[primaryKey.name]),
          ],
        );
      }
      setTabs((current) =>
        current.map((item) =>
          item.id === tab.id && item.kind === "table" ? { ...item, changes: [] } : item,
        ),
      );
      notify(`Committed ${tab.changes.length} change${tab.changes.length === 1 ? "" : "s"}`);
      return true;
    } catch (error) {
      const message = messageFor(error);
      setMutationError(message);
      notify(`Could not commit changes: ${message}`);
      return false;
    } finally {
      setMutating(false);
    }
  }

  async function discardTableChanges(tabID: string) {
    const tab = tabs.find((item): item is TableTab => item.id === tabID && item.kind === "table");
    if (!tab) return;
    const count = tab.changes.length;
    setTabs((current) =>
      current.map((item) =>
        item.id === tab.id && item.kind === "table" ? { ...item, changes: [] } : item,
      ),
    );
    await loadTable(tab.id, tab.table, tab.page, tab.columns);
    notify(`Discarded ${count} change${count === 1 ? "" : "s"}`);
  }

  async function addRow() {
    if (!addTab) return;
    const entries = addTab.columns.filter(
      (column) => addValues[column.name] !== undefined && addValues[column.name] !== "",
    );
    setMutating(true);
    setMutationError("");
    try {
      const sql = entries.length
        ? `INSERT INTO ${quoteIdentifier(addTab.table)} (${entries.map((column) => quoteIdentifier(column.name)).join(", ")}) VALUES (${entries.map(() => "?").join(", ")});`
        : `INSERT INTO ${quoteIdentifier(addTab.table)} DEFAULT VALUES;`;
      await execute(
        sql,
        entries.map((column) => parseValue(addValues[column.name])),
      );
      setAddTabID(undefined);
      setAddValues({});
      await loadTable(addTab.id, addTab.table, addTab.page, addTab.columns);
      notify("Row added");
    } catch (error) {
      setMutationError(messageFor(error));
    } finally {
      setMutating(false);
    }
  }

  async function deleteRows(tabID: string, rows: Row[]) {
    if (!rows.length) return false;
    const tab = tabs.find((item): item is TableTab => item.id === tabID && item.kind === "table");
    if (!tab) return false;
    const primaryKeys = tab.columns.filter((column) => column.primaryKey);
    setMutating(true);
    setMutationError("");
    try {
      for (const row of rows) {
        await execute(
          `DELETE FROM ${quoteIdentifier(tab.table)} WHERE ${primaryKeys.map((column) => `${quoteIdentifier(column.name)} IS ?`).join(" AND ")};`,
          primaryKeys.map((column) => row[column.name]),
        );
      }
      await loadTable(tab.id, tab.table, tab.page, tab.columns);
      notify(`Deleted ${rows.length} row${rows.length === 1 ? "" : "s"}`);
      return true;
    } catch (error) {
      setMutationError(messageFor(error));
      notify(`Could not delete rows: ${messageFor(error)}`);
      return false;
    } finally {
      setMutating(false);
    }
  }

  return (
    <div className="flex h-full min-h-[540px] overflow-hidden bg-kumo-canvas md:min-h-0">
      <aside className="flex shrink-0 flex-col bg-kumo-base" style={{ width: sidebarWidth }}>
        <div className="p-3 pb-1">
          <Input
            aria-label="Search tables"
            onChange={(event) => setTableFilter(event.target.value)}
            placeholder="Search tables"
            value={tableFilter}
          />
        </div>
        {tablesError && (
          <div className="p-3">
            <Banner variant="error">{tablesError}</Banner>
          </div>
        )}
        <div className="min-h-0 flex-1 overflow-y-auto py-2">
          {visibleTables.map((table) => (
            <button
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-kumo-tint"
              key={table}
              onClick={() => openTable(table)}
              type="button"
            >
              <Table2 className="size-4 shrink-0 text-kumo-secondary" />
              <span className="truncate font-mono text-[0.9em]">{table}</span>
            </button>
          ))}
          {!tablesError && !visibleTables.length && (
            <Text DANGEROUS_className="block px-3 py-4" size="sm" variant="secondary">
              No tables found.
            </Text>
          )}
        </div>
      </aside>
      <ResizeHandle
        ariaLabel="Resize table navigation"
        direction="horizontal"
        onResize={(position, total) =>
          setSidebarWidth(clamp(position, 180, Math.min(480, total * 0.5)))
        }
      />
      <section className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <div className="flex shrink-0 items-center border-b border-kumo-line bg-kumo-base">
          <div className="flex min-w-0 flex-1 overflow-x-auto items-center">
            {tabs.map((tab) => (
              <WorkspaceTab
                active={tab.id === activeTabID}
                key={tab.id}
                label={tab.kind === "table" ? tab.table : tab.label}
                onClose={() => closeTab(tab.id)}
                onSelect={() => setActiveTabID(tab.id)}
              />
            ))}
            <Button
              aria-label="New query"
              onClick={createQueryTab}
              variant="ghost"
              className="mx-2"
            >
              <Plus className="size-4" />
              New
            </Button>
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-hidden">
          {activeTab?.kind === "table" ? (
            <TableWorkspace
              onAdd={() => {
                setAddValues({});
                setMutationError("");
                setAddTabID(activeTab.id);
              }}
              onDelete={(rows) => deleteRows(activeTab.id, rows)}
              onDiscard={() => discardTableChanges(activeTab.id)}
              onEdit={(row, column, value) => stageGridCell(activeTab.id, row, column, value)}
              onCommit={() => commitTableChanges(activeTab.id)}
              onLoad={(page) =>
                void loadTable(activeTab.id, activeTab.table, page, activeTab.columns)
              }
              tab={activeTab}
            />
          ) : activeTab?.kind === "query" ? (
            <QueryWorkspace
              onRun={() => void runQuery(activeTab)}
              onSQLChange={(sql) =>
                setTabs((current) =>
                  current.map((tab) =>
                    tab.id === activeTab.id && tab.kind === "query" ? { ...tab, sql } : tab,
                  ),
                )
              }
              tab={activeTab}
            />
          ) : (
            <EmptyWorkspace onNewQuery={createQueryTab} />
          )}
        </div>
      </section>
      <Dialog.Root open={Boolean(addTab)} onOpenChange={(open) => !open && setAddTabID(undefined)}>
        <Dialog className="max-h-[80dvh] overflow-auto p-0">
          <Dialog.Title className="border-b border-kumo-line px-6 py-4 text-lg font-semibold">
            Add row
          </Dialog.Title>
          <div className="grid gap-4 px-6 py-5">
            {mutationError && <Banner variant="error">{mutationError}</Banner>}
            {addTab?.columns.map((column) => (
              <label className="grid gap-1.5" key={column.name}>
                <Text size="sm">
                  {column.name}{" "}
                  <span className="font-mono text-[0.9em] text-kumo-secondary">
                    {column.type || "value"}
                  </span>
                </Text>
                <Input
                  onChange={(event) =>
                    setAddValues((current) => ({ ...current, [column.name]: event.target.value }))
                  }
                  value={addValues[column.name] ?? ""}
                />
              </label>
            ))}
          </div>
          <div className="flex justify-end gap-2 border-t border-kumo-line px-6 py-4">
            <Button onClick={() => setAddTabID(undefined)} variant="secondary">
              Cancel
            </Button>
            <Button loading={mutating} onClick={() => void addRow()}>
              Add row
            </Button>
          </div>
        </Dialog>
      </Dialog.Root>
    </div>
  );
}

function WorkspaceTab({
  active,
  label,
  onClose,
  onSelect,
}: {
  active: boolean;
  label: string;
  onClose: () => void;
  onSelect: () => void;
}) {
  return (
    <div
      className={`group flex shrink-0 items-center gap-1 border-r border-kumo-line ${active ? "bg-kumo-canvas" : ""}`}
    >
      <button className="max-w-48 truncate pl-4 pr-2 py-4 text-sm" onClick={onSelect} type="button">
        {label}
      </button>
      <div className="w-8">
        <Button
          aria-label={`Close ${label}`}
          onClick={onClose}
          shape="square"
          size="sm"
          variant="ghost"
          className="mr-2 hidden group-hover:flex"
        >
          <X className="size-3.5" />
        </Button>
      </div>
    </div>
  );
}

function EmptyWorkspace({ onNewQuery }: { onNewQuery: () => void }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3">
      <Database className="size-7 text-kumo-secondary" />
      <div className="text-center">
        <Text bold>Explore your database</Text>
        <Text size="sm" variant="secondary">
          Select a table or create a query to get started.
        </Text>
      </div>
      <Button onClick={onNewQuery}>
        <Plus className="size-4" />
        New query
      </Button>
    </div>
  );
}

function TableWorkspace({
  tab,
  onAdd,
  onCommit,
  onDiscard,
  onDelete,
  onEdit,
  onLoad,
}: {
  tab: TableTab;
  onAdd: () => void;
  onCommit: () => Promise<boolean>;
  onDiscard: () => void;
  onDelete: (rows: Row[]) => Promise<boolean>;
  onEdit: (row: Row, column: Column, value: unknown) => void;
  onLoad: (page: number) => void;
}) {
  const editable = tab.columns.some((column) => column.primaryKey);
  const [filter, setFilter] = useState("");
  const [selectedRowIDs, setSelectedRowIDs] = useState<Set<number>>(new Set());
  const [pendingDeleteIDs, setPendingDeleteIDs] = useState<Set<number>>(new Set());
  const [deleting, setDeleting] = useState(false);
  const [committing, setCommitting] = useState(false);
  const gridRows = useMemo<GridRow[]>(
    () =>
      tab.rows.map((row, index) => ({
        ...row,
        __gridIndex: index,
        __rowNumber: tab.page * pageSize + index + 1,
      })),
    [tab.page, tab.rows],
  );
  const filteredRows = useMemo(
    () =>
      filter.trim()
        ? gridRows.filter((row) =>
            tab.columns.some((column) =>
              displayValue(row[column.name]).toLowerCase().includes(filter.trim().toLowerCase()),
            ),
          )
        : gridRows,
    [filter, gridRows, tab.columns],
  );
  const selectedRows = gridRows.filter((row) => selectedRowIDs.has(row.__gridIndex));
  const pendingDeleteRows = gridRows.filter((row) => pendingDeleteIDs.has(row.__gridIndex));
  const gridColumns = useMemo<readonly GridColumn<GridRow>[]>(
    () => [
      {
        key: "__rowNumber",
        name: "#",
        width: 40,
        frozen: "start" as const,
        renderCell: ({ row }: { row: GridRow }) => (
          <span className="block text-right font-mono text-[12px] text-kumo-secondary">
            {row.__rowNumber}
          </span>
        ),
      },
      ...tab.columns.map((column) => ({
        key: column.name,
        name: <span className="font-mono text-[0.9em]">{column.name}</span>,
        editable: editable && !tab.loading,
        renderEditCell: renderTextEditor,
        cellClass: (row: GridRow) =>
          hasStagedChange(tab.changes, tab.columns, row, column.name)
            ? "database-data-grid__pending-change"
            : undefined,
        width: 180,
        resizable: true,
        renderCell: ({ row }: { row: GridRow }) => (
          <span
            className="block truncate font-mono text-[12px]"
            title={displayValue(row[column.name])}
          >
            {displayValue(row[column.name])}
          </span>
        ),
      })),
    ],
    [editable, tab.changes, tab.columns, tab.loading],
  );

  function handleRowsChange(rows: GridRow[], data: { indexes: number[]; column: { key: string } }) {
    const column = tab.columns.find((item) => item.name === data.column.key);
    if (!column) return;
    for (const index of data.indexes) {
      const previousRow = tab.rows[rows[index]?.__gridIndex ?? -1];
      const nextRow = rows[index];
      if (previousRow && nextRow && previousRow[column.name] !== nextRow[column.name]) {
        onEdit(previousRow, column, nextRow[column.name]);
        setSelectedRowIDs(new Set([nextRow.__gridIndex]));
      }
    }
  }

  function selectRow(row: GridRow, multiSelect: boolean) {
    setSelectedRowIDs((current) => {
      if (!multiSelect) return new Set([row.__gridIndex]);
      const next = new Set(current);
      if (next.has(row.__gridIndex)) next.delete(row.__gridIndex);
      else next.add(row.__gridIndex);
      return next;
    });
  }

  async function confirmDelete() {
    setDeleting(true);
    try {
      if (await onDelete(pendingDeleteRows)) {
        setPendingDeleteIDs(new Set());
        setSelectedRowIDs(new Set());
      }
    } finally {
      setDeleting(false);
    }
  }

  async function commitChanges() {
    setCommitting(true);
    try {
      await onCommit();
    } finally {
      setCommitting(false);
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-kumo-line p-1">
        <div className="flex items-center gap-2">
          <Button
            aria-label="Refresh table"
            disabled={tab.loading}
            onClick={() => onLoad(tab.page)}
            shape="square"
            size="sm"
            variant="secondary"
          >
            <RefreshCw className="size-4" />
          </Button>
          <Button disabled={!editable || tab.loading} onClick={onAdd} size="sm">
            Add row
          </Button>
          <Button
            disabled={!editable || !selectedRows.length || tab.loading}
            onClick={() =>
              setPendingDeleteIDs((current) => new Set([...current, ...selectedRowIDs]))
            }
            size="sm"
            variant="destructive"
          >
            Delete row
          </Button>
        </div>
        <div className="min-w-52 flex-1">
          <Input
            aria-label="Filter visible rows"
            className="h-8"
            onChange={(event) => setFilter(event.target.value)}
            placeholder="Filter visible rows"
            value={filter}
          />
        </div>
        {tab.changes.length > 0 && (
          <div className="flex items-center gap-2">
            <Text size="sm" variant="secondary">
              {tab.changes.length} row{tab.changes.length === 1 ? "" : "s"} changed
            </Text>
            <Button disabled={committing} onClick={onDiscard} size="sm" variant="secondary">
              Discard
            </Button>
            <Button loading={committing} onClick={() => void commitChanges()} size="sm">
              Commit {tab.changes.length} change{tab.changes.length === 1 ? "" : "s"}
            </Button>
          </div>
        )}
        {pendingDeleteRows.length > 0 && (
          <div className="flex items-center gap-2">
            <Button
              disabled={deleting}
              onClick={() => setPendingDeleteIDs(new Set())}
              size="sm"
              variant="secondary"
            >
              Discard
            </Button>
            <Button
              loading={deleting}
              onClick={() => void confirmDelete()}
              size="sm"
              variant="destructive"
            >
              Delete {pendingDeleteRows.length} row{pendingDeleteRows.length === 1 ? "" : "s"}
            </Button>
          </div>
        )}
        {!editable && tab.columns.length > 0 && (
          <Text size="sm" variant="secondary">
            This table has no primary key and is read-only.
          </Text>
        )}
      </div>
      {tab.error ? (
        <div className="p-4">
          <Banner variant="error">{tab.error}</Banner>
        </div>
      ) : (
        <div className="min-h-0 flex-1">
          <DataGrid
            aria-label={`${tab.table} data`}
            className="database-data-grid rdg-light h-full border-0 text-sm"
            columns={gridColumns}
            defaultColumnOptions={{ resizable: true }}
            headerRowHeight={36}
            onCellClick={(args, event) => selectRow(args.row, event.metaKey || event.ctrlKey)}
            onRowsChange={handleRowsChange}
            rowKeyGetter={(row) => row.__gridIndex}
            rows={filteredRows}
            rowClass={(row) =>
              pendingDeleteIDs.has(row.__gridIndex)
                ? "database-data-grid__pending-delete"
                : undefined
            }
            selectedRows={selectedRowIDs}
            rowHeight={36}
            style={{ height: "100%" }}
          />
        </div>
      )}
      <div className="flex shrink-0 items-center justify-between border-t border-kumo-line px-4 py-3">
        <Text size="sm" variant="secondary">
          {tab.loading ? "Loading…" : `${tab.rows.length} rows`}
        </Text>
        <div className="flex items-center gap-2">
          <Button
            aria-label="Previous page"
            disabled={tab.loading || tab.page === 0}
            onClick={() => onLoad(tab.page - 1)}
            shape="square"
            size="sm"
            variant="secondary"
          >
            <ChevronLeft className="size-4" />
          </Button>
          <Text size="sm">Page {tab.page + 1}</Text>
          <Button
            aria-label="Next page"
            disabled={tab.loading || tab.rows.length < pageSize}
            onClick={() => onLoad(tab.page + 1)}
            shape="square"
            size="sm"
            variant="secondary"
          >
            <ChevronRight className="size-4" />
          </Button>
        </div>
      </div>
    </div>
  );
}

function QueryWorkspace({
  tab,
  onRun,
  onSQLChange,
}: {
  tab: QueryTab;
  onRun: () => void;
  onSQLChange: (sql: string) => void;
}) {
  const columns = useMemo(() => rowColumns(tab.rows), [tab.rows]);
  const [editorHeight, setEditorHeight] = useState(40);
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex min-h-40 shrink-0 flex-col" style={{ height: `${editorHeight}%` }}>
        <textarea
          aria-label="SQL query"
          className="min-h-0 flex-1 resize-none bg-kumo-canvas p-4 font-mono text-sm outline-none"
          onChange={(event) => onSQLChange(event.target.value)}
          placeholder="SELECT * FROM your_table;"
          spellCheck={false}
          value={tab.sql}
        />
        <div className="flex justify-end border-t border-kumo-line px-3 py-2">
          <Button disabled={tab.running} loading={tab.running} onClick={onRun} size="sm">
            <Play className="size-4" />
            Run
          </Button>
        </div>
      </div>
      <ResizeHandle
        ariaLabel="Resize query editor"
        direction="vertical"
        onResize={(position, total) => setEditorHeight(clamp((position / total) * 100, 20, 75))}
      />
      {tab.error && (
        <div className="p-4">
          <Banner variant="error">{tab.error}</Banner>
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-auto">
        {columns.length ? (
          <Table className="min-w-max" layout="fixed">
            <Table.Header>
              <Table.Row>
                {columns.map((column) => (
                  <Table.Head key={column}>
                    <span className="font-mono text-[0.9em]">{column}</span>
                  </Table.Head>
                ))}
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {tab.rows.map((row, index) => (
                <Table.Row key={index}>
                  {columns.map((column) => (
                    <Table.Cell key={column}>
                      <span
                        className="block max-w-72 truncate font-mono text-[12px]"
                        title={displayValue(row[column])}
                      >
                        {displayValue(row[column])}
                      </span>
                    </Table.Cell>
                  ))}
                </Table.Row>
              ))}
            </Table.Body>
          </Table>
        ) : (
          !tab.error && (
            <div className="p-4">
              <Text size="sm" variant="secondary">
                Run SQL to see results.
              </Text>
            </div>
          )
        )}
      </div>
      {tab.summary && (
        <div className="border-t border-kumo-line px-4 py-2">
          <Text size="sm" variant="secondary">
            {tab.summary}
          </Text>
        </div>
      )}
    </div>
  );
}

function ResizeHandle({
  ariaLabel,
  direction,
  onResize,
}: {
  ariaLabel: string;
  direction: "horizontal" | "vertical";
  onResize: (position: number, total: number) => void;
}) {
  function resize(event: PointerEvent<HTMLDivElement>) {
    const bounds = event.currentTarget.parentElement?.getBoundingClientRect();
    if (!bounds) return;
    onResize(
      direction === "horizontal" ? event.clientX - bounds.left : event.clientY - bounds.top,
      direction === "horizontal" ? bounds.width : bounds.height,
    );
  }

  function beginResize(event: PointerEvent<HTMLDivElement>) {
    event.preventDefault();
    event.currentTarget.setPointerCapture(event.pointerId);
    resize(event);
  }

  return (
    <div
      aria-label={ariaLabel}
      aria-orientation={direction === "horizontal" ? "vertical" : "horizontal"}
      className={
        direction === "horizontal"
          ? "relative z-10 w-px shrink-0 cursor-col-resize bg-kumo-line hover:bg-kumo-brand before:absolute before:-inset-x-2 before:inset-y-0 before:content-['']"
          : "relative z-10 h-px shrink-0 cursor-row-resize bg-kumo-line hover:bg-kumo-brand before:absolute before:inset-x-0 before:-inset-y-2 before:content-['']"
      }
      onPointerDown={beginResize}
      onPointerMove={(event) => {
        if (event.currentTarget.hasPointerCapture(event.pointerId)) resize(event);
      }}
      role="separator"
    />
  );
}

function rowsFor(response: DBQueryResponse): Row[] {
  return (response.results?.[0]?.results ?? []) as Row[];
}
function columnsFor(response: DBQueryResponse): Column[] {
  return rowsFor(response).map((row) => ({
    name: String(row.name),
    type: primitiveString(row.type),
    primaryKey: Number(row.pk) > 0,
    notNull: Boolean(row.notnull),
  }));
}
function rowColumns(rows: Row[]) {
  return [...new Set(rows.flatMap((row) => Object.keys(row)))];
}
function stageCellChange(
  tab: TableTab,
  row: Row,
  column: Column,
  value: unknown,
  primaryKeys: Column[],
): TableTab {
  const existing = tab.changes.find((change) => changeMatchesRow(change, row, primaryKeys));
  const original = existing?.original ?? row;
  const values = { ...existing?.values, [column.name]: value };
  if (Object.is(value, original[column.name])) delete values[column.name];
  const changes = Object.keys(values).length
    ? [
        ...tab.changes.filter((change) => change !== existing),
        { rowKey: existing?.rowKey ?? primaryKeyFor(original, primaryKeys), original, values },
      ]
    : tab.changes.filter((change) => change !== existing);
  return {
    ...tab,
    changes,
    rows: tab.rows.map((currentRow) =>
      changeMatchesRow(existing ?? { original, values: {}, rowKey: "" }, currentRow, primaryKeys)
        ? { ...currentRow, [column.name]: value }
        : currentRow,
    ),
  };
}
function applyStagedChanges(rows: Row[], columns: Column[], changes: StagedChange[]) {
  const primaryKeys = columns.filter((column) => column.primaryKey);
  return rows.map((row) => {
    const change = changes.find(
      (item) => primaryKeyFor(item.original, primaryKeys) === primaryKeyFor(row, primaryKeys),
    );
    return change ? { ...row, ...change.values } : row;
  });
}
function hasStagedChange(changes: StagedChange[], columns: Column[], row: Row, columnName: string) {
  const primaryKeys = columns.filter((column) => column.primaryKey);
  return changes.some(
    (change) => columnName in change.values && changeMatchesRow(change, row, primaryKeys),
  );
}
function changeMatchesRow(change: StagedChange, row: Row, primaryKeys: Column[]) {
  return primaryKeys.every((column) =>
    Object.is(row[column.name], change.values[column.name] ?? change.original[column.name]),
  );
}
function primaryKeyFor(row: Row, primaryKeys: Column[]) {
  return JSON.stringify(primaryKeys.map((column) => row[column.name]));
}
function quoteIdentifier(value: string) {
  return `"${value.replace(/"/g, '""')}"`;
}
function displayValue(value: unknown) {
  if (value === null || value === undefined) return "NULL";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return "[Unserializable value]";
  }
}
function primitiveString(value: unknown) {
  return typeof value === "string" || typeof value === "number" || typeof value === "boolean"
    ? String(value)
    : "";
}
function parseValue(value: string) {
  if (value === "NULL") return null;
  if (value === "true") return true;
  if (value === "false") return false;
  if (value.trim() !== "" && Number.isFinite(Number(value))) return Number(value);
  return value;
}
function messageFor(error: unknown) {
  return error instanceof Error ? error.message : "Query failed";
}
function formatDuration(duration: number) {
  return duration < 1 ? `${duration.toFixed(2)}ms` : `${Math.round(duration)}ms`;
}

function clamp(value: number, minimum: number, maximum: number) {
  return Math.min(Math.max(value, minimum), Math.max(minimum, maximum));
}
