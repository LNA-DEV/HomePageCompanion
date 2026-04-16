package database

import "gorm.io/gorm"

func init() {
	RegisterMigration("001_itemname_to_guid", migrateItemNameToGUID)
}

func migrateItemNameToGUID(tx *gorm.DB) error {
	tables := []string{"auto_upload_items", "interactions", "native_likes"}

	// First update values from filename to UUID
	for _, table := range tables {
		err := tx.Exec(`
			UPDATE `+table+` SET item_name = (
				SELECT fi.guid FROM feed_items fi
				WHERE fi.title = `+table+`.item_name
				LIMIT 1
			) WHERE EXISTS (
				SELECT 1 FROM feed_items fi
				WHERE fi.title = `+table+`.item_name
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
