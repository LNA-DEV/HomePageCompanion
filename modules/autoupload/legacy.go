package autoupload

import (
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// importLegacy moves the old autouploader tables over:
//
//   - auto_upload_items (one row per platform) become publications on the
//     target instance of that platform;
//   - upload_attempts keep their history, with a cause, and a failure counts
//     as resolved when a later success or a publication exists;
//   - interactions become the likes of the publications, native likes the
//     website likes;
//   - microblog publications appear as notes, and photos no longer in the
//     feed keep a row of their own.
func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	db := mc.DB()
	connOf := map[string]string{} // old target name → connection name
	for _, c := range l.Connections {
		if _, ok := connOf[c.TargetName]; !ok {
			connOf[c.TargetName] = companion.LegacyName(c.Name)
		}
	}
	targetOf := func(platform, targetName string) (string, bool) {
		if targetName != "" {
			if t, ok := l.Target(targetName); ok {
				return t.InstanceID(), true
			}
		}
		if t, ok := l.TargetOfPlatform(platform); ok {
			return t.InstanceID(), true
		}
		return "", false
	}

	// Posts index: every feed item, with its source and whether it is still
	// in the feed.
	if l.HasTable("feed_items") {
		var rows []struct {
			GUID, Title, Description, Link, ImageURL, ItemType, FeedName string
			Published                                                    time.Time
			Deleted                                                      bool
		}
		l.DB.Raw(`SELECT fi.guid, fi.title, fi.description, fi.link, fi.image_url, fi.item_type, f.feed_name, fi.published,
			fi.deleted_at IS NOT NULL AS deleted FROM feed_items fi JOIN feeds f ON f.id = fi.feed_id WHERE fi.guid <> ''`).Scan(&rows)
		for _, r := range rows {
			kind := feed.KindPhoto
			if r.ImageURL == "" {
				kind = feed.KindArticle
			}
			if !l.Check {
				upsertPost(mc, Post{ItemID: r.GUID, Source: "rss:" + companion.LegacyName(r.FeedName), Kind: kind, Title: r.Title, Link: r.Link,
					ImageURL: r.ImageURL, Published: r.Published, InFeed: !r.Deleted})
			}
		}
		l.Reportf("autoupload: %d post(s) indexed", len(rows))
	}

	// Publications.
	if l.HasTable("auto_upload_items") {
		var rows []struct {
			Platform, ItemID           string
			PostURL, VersionID, PostID *string
			CreatedAt                  time.Time
		}
		l.DB.Raw("SELECT platform, item_id, post_url AS post_url, version_id, post_id, created_at FROM auto_upload_items").Scan(&rows)
		moved, unknown := 0, 0
		for _, r := range rows {
			target, ok := targetOf(r.Platform, "")
			if !ok {
				unknown++
				continue
			}
			t, _ := l.TargetOfPlatform(r.Platform)
			pub := Publication{ItemID: r.ItemID, Target: target, Platform: r.Platform, Source: "autoupload", Connection: connOf[t.Name],
				Status: StatusPublished, PublishedAt: r.CreatedAt}
			pub.PostID, pub.PostURL, pub.Version = legacyRef(r.Platform, deref(r.PostURL), deref(r.VersionID), deref(r.PostID))
			moved++
			if l.Check {
				continue
			}
			db.Where("item_id = ? AND target = ?", pub.ItemID, pub.Target).Assign(pub).FirstOrCreate(&pub)
			ensurePost(mc, r.ItemID, r.CreatedAt)
		}
		l.Reportf("autoupload: %d publication(s), %d of a platform without target", moved, unknown)
	}

	// Attempts.
	if l.HasTable("upload_attempts") {
		var rows []struct {
			Source, ConnectionName, ItemID, Platform, TargetName string
			Success                                              bool
			ErrorCode, ErrorMessage                              string
			HTTPStatus                                           int
			CreatedAt                                            time.Time
		}
		l.DB.Raw(`SELECT COALESCE(source, 'autouploader') AS source, connection_name, item_id, platform, target_name, success,
			error_code, error_message, http_status, created_at FROM upload_attempts ORDER BY created_at`).Scan(&rows)
		lastSuccess := map[string]time.Time{}
		for _, r := range rows {
			if r.Success {
				if target, ok := targetOf(r.Platform, r.TargetName); ok {
					lastSuccess[r.ItemID+"|"+target] = r.CreatedAt
				}
			}
		}
		var pubs []Publication
		db.Find(&pubs)
		published := map[string]time.Time{}
		for _, p := range pubs {
			published[p.ItemID+"|"+p.Target] = p.PublishedAt
		}
		n := 0
		for _, r := range rows {
			target, ok := targetOf(r.Platform, r.TargetName)
			if !ok {
				continue
			}
			src := "autoupload"
			conn := companion.LegacyName(r.ConnectionName)
			if r.Source == "microblog" {
				src, conn = "microblog", ""
			}
			a := Attempt{ItemID: r.ItemID, Target: target, Platform: r.Platform, Source: src, Connection: conn, Success: r.Success,
				Code: r.ErrorCode, HTTPStatus: r.HTTPStatus, Error: r.ErrorMessage, CreatedAt: r.CreatedAt}
			if !r.Success {
				a.Cause = causeOfCode(r.ErrorCode)
				key := r.ItemID + "|" + target
				if t, ok := lastSuccess[key]; ok && t.After(r.CreatedAt) {
					a.ResolvedAt = &t
				} else if t, ok := published[key]; ok {
					a.ResolvedAt = &t
				}
			}
			n++
			if !l.Check {
				db.Create(&a)
			}
		}
		l.Reportf("autoupload: %d attempt(s)", n)
	}

	// Microblog notes, so they get their cells.
	if l.HasTable("microblog_publications") && l.HasTable("microblog_posts") {
		var rows []struct {
			Slug, Body, TargetName, Platform string
			PostURL, ExternalID, VersionID   *string
			CreatedAt                        time.Time
		}
		l.DB.Raw(`SELECT p.slug, p.body, u.target_name, u.platform, u.post_url, u.external_id, u.version_id, u.created_at
			FROM microblog_publications u JOIN microblog_posts p ON p.id = u.post_id WHERE u.success = 1`).Scan(&rows)
		for _, r := range rows {
			target, ok := targetOf(r.Platform, r.TargetName)
			if !ok || l.Check {
				continue
			}
			pub := Publication{ItemID: r.Slug, Target: target, Platform: r.Platform, Source: "microblog", Status: StatusPublished,
				PostID: deref(r.ExternalID), PostURL: deref(r.PostURL), Version: deref(r.VersionID), PublishedAt: r.CreatedAt}
			db.Where("item_id = ? AND target = ?", pub.ItemID, pub.Target).Assign(pub).FirstOrCreate(&pub)
			ensureNote(mc, r.Slug, "note", firstLine(r.Body), "", "microblog", r.CreatedAt)
		}
		l.Reportf("autoupload: %d microblog publication(s)", len(rows))
	}

	// Likes.
	if l.HasTable("interactions") {
		var rows []struct {
			Source, ItemID, Platform, TargetName string
			LikeCount                            int
			UpdatedAt                            time.Time
		}
		l.DB.Raw("SELECT COALESCE(source, 'rss') AS source, item_id, platform, target_name, like_count, updated_at FROM interactions").Scan(&rows)
		for _, r := range rows {
			if l.Check {
				continue
			}
			if r.Platform == "native" {
				db.Model(&Post{}).Where("item_id = ?", r.ItemID).Update("website_likes", r.LikeCount)
				continue
			}
			target, ok := targetOf(r.Platform, r.TargetName)
			if !ok {
				continue
			}
			at := r.UpdatedAt
			db.Model(&Publication{}).Where("item_id = ? AND target = ?", r.ItemID, target).Updates(map[string]any{"likes": r.LikeCount, "likes_at": at})
			db.Create(&LikeDay{ItemID: r.ItemID, Target: target, Day: at.Format(time.DateOnly), Likes: r.LikeCount})
		}
		l.Reportf("autoupload: %d like count(s)", len(rows))
	}
	return nil
}

// legacyRef maps the old columns to a Result. The old Bluesky rows kept the
// at:// URI in post_url and the CID in version_id.
func legacyRef(platform, postURL, versionID, postID string) (id, url, version string) {
	if platform == "bluesky" {
		return postURL, blueskyWebURL(postURL), versionID
	}
	return postID, postURL, versionID
}

// blueskyWebURL turns at://<did>/app.bsky.feed.post/<rkey> into the
// bsky.app page, which accepts a DID in place of the handle.
func blueskyWebURL(uri string) string {
	rest, ok := strings.CutPrefix(uri, "at://")
	if !ok {
		return ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return ""
	}
	return "https://bsky.app/profile/" + parts[0] + "/post/" + parts[2]
}

func causeOfCode(code string) string {
	switch code {
	case "auth":
		return social.CauseAccount
	case "client", "content":
		return social.CauseContent
	}
	return social.CauseTransient
}

// ensurePost keeps a row for a published item the feed no longer has.
func ensurePost(mc companion.ModuleContext, id string, at time.Time) {
	var p Post
	if mc.DB().Where("item_id = ?", id).Limit(1).Find(&p).RowsAffected > 0 {
		return
	}
	upsertPost(mc, Post{ItemID: id, Kind: feed.KindPhoto, Title: id, Published: at, InFeed: false})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 80 {
		s = string(r[:79]) + "…"
	}
	return s
}
