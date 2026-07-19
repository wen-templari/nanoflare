import { type Dispatch, type FormEvent, type SetStateAction, useState } from "react";
import { apiFetch, errorText } from "../../app/api";
import type { Database } from "../../app/types";
import { sortDatabases } from "../../app/utils";
import { Field } from "../shared/primitives";
import { Button } from "../ui/button";
import { Dialog } from "../ui/dialog";
import { Input } from "../ui/input";

export function CreateDatabaseDialog({
  open,
  onClose,
  databases,
  setDatabases,
  notify,
  apiConnected,
}: {
  open: boolean;
  onClose: () => void;
  databases: Database[];
  setDatabases: Dispatch<SetStateAction<Database[]>>;
  notify: (text: string) => void;
  apiConnected: boolean;
}) {
  const [name, setName] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return notify("Database name is required");
    let database: Database = { id: crypto.randomUUID().replace(/-/g, ""), name: trimmed, created_at: new Date().toISOString() };
    if (apiConnected) {
      const response = await apiFetch("/v1/db", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ name: trimmed }),
      });
      if (!response.ok) return notify(await errorText(response, "Database creation failed"));
      database = await response.json() as Database;
    }
    setDatabases(sortDatabases([...databases, database]));
    setName("");
    onClose();
    notify(`${database.name} created`);
  }

  return <Dialog open={open} onClose={onClose} title="Create database" description="Provision a SQLite database you can bind into one or more workers."><form className="space-y-4" onSubmit={submit}><Field label="Name"><Input required placeholder="app-data" value={name} onChange={(event) => setName(event.target.value)} /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Create database</Button></div></form></Dialog>;
}
