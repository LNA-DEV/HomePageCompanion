package interactions

import (
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// importLegacy moves the old like counting over: every publication with an
// id becomes a tracked post (autouploader items on their platform's target,
// microblog publications on theirs), with its last count; native likes and
// their counts stay as they were.
func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	db := mc.DB()
	targetOf := func(platform, name string) (string, bool) {
		if name != "" {
			if t, ok := l.Target(name); ok {
				return t.InstanceID(), true
			}
		}
		if t, ok := l.TargetOfPlatform(platform); ok {
			return t.InstanceID(), true
		}
		return "", false
	}
	likes := map[string]int{} // source|item|target → count
	if l.HasTable("interactions") {
		var rows []struct {
			Source, ItemID, Platform, TargetName string
			LikeCount                            int
		}
		l.DB.Raw("SELECT COALESCE(source, 'rss') AS source, item_id, platform, target_name, like_count FROM interactions").Scan(&rows)
		for _, r := range rows {
			if r.Platform == "native" {
				if !l.Check {
					db.Save(&NativeCount{ItemID: r.ItemID, Likes: r.LikeCount})
				}
				continue
			}
			if t, ok := targetOf(r.Platform, r.TargetName); ok {
				likes[r.Source+"|"+r.ItemID+"|"+t] = r.LikeCount
			}
		}
	}
	n := 0
	add := func(t Tracked, likeKey string) {
		t.Likes = likes[likeKey]
		now := time.Now()
		if t.Likes > 0 {
			t.LikesAt = &now
		}
		n++
		if !l.Check {
			db.Where("item_id = ? AND target = ?", t.ItemID, t.Target).FirstOrCreate(&t)
		}
	}
	if l.HasTable("auto_upload_items") {
		var rows []struct{ Platform, ItemID, PostURL, VersionID, PostID string }
		l.DB.Raw("SELECT platform, item_id, COALESCE(post_url, '') AS post_url, COALESCE(version_id, '') AS version_id, COALESCE(post_id, '') AS post_id FROM auto_upload_items").Scan(&rows)
		for _, r := range rows {
			target, ok := targetOf(r.Platform, "")
			if !ok {
				continue
			}
			t := Tracked{ItemID: r.ItemID, Target: target, Source: "autoupload", Platform: r.Platform, PostID: r.PostID, PostURL: r.PostURL, Version: r.VersionID}
			if r.Platform == "bluesky" { // the at:// URI was kept in post_url
				t.PostID, t.PostURL = r.PostURL, ""
			}
			if t.PostID == "" {
				continue
			}
			add(t, "rss|"+r.ItemID+"|"+target)
		}
	}
	if l.HasTable("microblog_publications") && l.HasTable("microblog_posts") {
		var rows []struct{ Slug, Platform, TargetName, PostURL, ExternalID, VersionID string }
		l.DB.Raw(`SELECT p.slug, u.platform, u.target_name, COALESCE(u.post_url, '') AS post_url, COALESCE(u.external_id, '') AS external_id,
			COALESCE(u.version_id, '') AS version_id FROM microblog_publications u JOIN microblog_posts p ON p.id = u.post_id WHERE u.success = 1`).Scan(&rows)
		for _, r := range rows {
			target, ok := targetOf(r.Platform, r.TargetName)
			if !ok || r.ExternalID == "" {
				continue
			}
			add(Tracked{ItemID: r.Slug, Target: target, Source: "microblog", Platform: r.Platform, PostID: r.ExternalID, PostURL: r.PostURL, Version: r.VersionID},
				"microblog|"+r.Slug+"|"+target)
		}
	}
	natives := 0
	if l.HasTable("native_likes") {
		var rows []NativeLike
		l.DB.Raw("SELECT item_id, ip_hash, token, created_at FROM native_likes").Scan(&rows)
		natives = len(rows)
		if !l.Check {
			for _, r := range rows {
				db.Create(&NativeLike{ItemID: r.ItemID, IPHash: r.IPHash, Token: r.Token, CreatedAt: r.CreatedAt})
			}
		}
	}
	l.Reportf("interactions: %d tracked post(s), %d website like(s)", n, natives)
	return nil
}
