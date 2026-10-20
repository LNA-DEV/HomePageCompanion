package database

import "gorm.io/gorm"

func init() {
	RegisterMigration("001_itemname_to_guid", migrateItemNameToGUID)
}

// migrateItemNameToGUID rewrites item names (file names) to feed GUIDs and
// renames item_name to item_id. A table that does not exist yet (a fresh
// installation) or no longer has item_name is left alone: AutoMigrate creates
// it with item_id afterwards.
func migrateItemNameToGUID(tx *gorm.DB) error {
	var tables []string
	for _, table := range []string{"auto_upload_items", "interactions", "native_likes"} {
		if tx.Migrator().HasTable(table) && tx.Migrator().HasColumn(table, "item_name") {
			tables = append(tables, table)
		}
	}
	hasFeedItems := tx.Migrator().HasTable("feed_items")

	// First update values from filename to UUID
	for _, table := range tables {
		if !hasFeedItems {
			break
		}
		err := tx.Exec(`
			UPDATE ` + table + ` SET item_name = (
				SELECT fi.guid FROM feed_items fi
				WHERE fi.title = ` + table + `.item_name
				LIMIT 1
			) WHERE EXISTS (
				SELECT 1 FROM feed_items fi
				WHERE fi.title = ` + table + `.item_name
			)
		`).Error
		if err != nil {
			return err
		}
	}

	// Then rename column from item_name to item_id
	for _, table := range tables {
		err := tx.Exec(`ALTER TABLE ` + table + ` RENAME COLUMN item_name TO item_id`).Error
		if err != nil {
			return err
		}
	}

	return nil
}
