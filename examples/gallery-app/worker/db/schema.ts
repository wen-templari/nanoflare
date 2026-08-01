import { index, integer, sqliteTable, text } from "drizzle-orm/sqlite-core";

export const galleryItems = sqliteTable(
  "gallery_items",
  {
    id: text("id").primaryKey(),
    objectKey: text("object_key").notNull(),
    filename: text("filename").notNull(),
    contentType: text("content_type").notNull(),
    uploadedAt: text("uploaded_at").notNull(),
    size: integer("size").notNull(),
    previewCount: integer("preview_count").notNull().default(0),
  },
  (table) => [index("gallery_items_uploaded_at_idx").on(table.uploadedAt)],
);
