import { integer, sqliteTable, text } from "drizzle-orm/sqlite-core";
export const notes = sqliteTable("notes", {
  id: integer("id").primaryKey(),
  body: text("body").notNull(),
});
