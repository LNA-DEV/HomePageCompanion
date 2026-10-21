package basemap

import (
	"path"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/gorm"
)

// importLegacy moves the version rows and the parts of an interrupted copy
// over. The objects stay where they are: the module's blob area is the old
// maps/basemap/ folder, so only the keys lose their storage prefix. An
// interrupted copy resumes when the blob module uses the old bucket, prefix
// and credentials.
func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	if !l.HasTable("basemap_versions") {
		return nil
	}
	var versions []Version
	var parts []Part
	l.DB.Find(&versions)
	if l.HasTable("basemap_parts") {
		l.DB.Find(&parts)
	}
	active := ""
	for i := range versions {
		versions[i].Key = path.Base(versions[i].Key)
		if versions[i].Status == StatusActive {
			active = versions[i].Version
		}
	}
	if !l.Check {
		err := mc.DB().Transaction(func(tx *gorm.DB) error {
			if err := tx.CreateInBatches(&versions, 100).Error; err != nil {
				return err
			}
			return tx.CreateInBatches(&parts, 500).Error
		})
		if err != nil {
			return err
		}
	}
	l.Reportf("basemap: %d version(s) (active: %q), %d part(s) of an interrupted copy; the objects stay in %s",
		len(versions), active, len(parts), mc.Blobs().Prefix())
	return nil
}
