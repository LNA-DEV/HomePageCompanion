package microblog

import (
	"bytes"
	"context"
	"mime"
	"os"
	"path/filepath"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// importLegacy moves posts, publications, comments and the images in
// data/microblog/ over. The images go to the blob store and keep their
// public URLs.
func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	db := mc.DB()
	if !l.HasTable("microblog_posts") {
		return nil
	}
	var posts []struct {
		ID                                                 uint
		Slug, Body, ContentWarning, ImageURL, ImageAltText string
		CreatedAt, UpdatedAt                               time.Time
	}
	l.DB.Raw("SELECT id, slug, body, content_warning, image_url, image_alt_text, created_at, updated_at FROM microblog_posts").Scan(&posts)
	natives := map[string]int{}
	if l.HasTable("native_likes") {
		var rows []struct {
			ItemID string
			N      int
		}
		l.DB.Raw("SELECT item_id, COUNT(*) AS n FROM native_likes GROUP BY item_id").Scan(&rows)
		for _, r := range rows {
			natives[r.ItemID] = r.N
		}
	}
	for _, p := range posts {
		if l.Check {
			continue
		}
		db.Create(&Post{ID: p.ID, Slug: p.Slug, Body: p.Body, ContentWarning: p.ContentWarning, ImageURL: p.ImageURL,
			ImageAltText: p.ImageAltText, NativeLikes: natives[p.Slug], CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt})
	}
	likes := map[string]int{}
	if l.HasTable("interactions") {
		var rows []struct {
			ItemID, Platform, TargetName string
			LikeCount                    int
		}
		l.DB.Raw("SELECT item_id, platform, target_name, like_count FROM interactions WHERE source = 'microblog'").Scan(&rows)
		for _, r := range rows {
			likes[r.ItemID+"|"+r.TargetName] = r.LikeCount
		}
	}
	slugOf := map[uint]string{}
	for _, p := range posts {
		slugOf[p.ID] = p.Slug
	}
	var pubs []struct {
		ID, PostID                            uint
		TargetName, Platform                  string
		PostURL, ExternalID, VersionID        *string
		Success                               bool
		ErrorMessage                          string
		LikesRefreshedAt, CommentsRefreshedAt *time.Time
		CreatedAt, UpdatedAt                  time.Time
	}
	if l.HasTable("microblog_publications") {
		l.DB.Raw(`SELECT id, post_id, target_name, platform, post_url, external_id, version_id, success, error_message,
			likes_refreshed_at, comments_refreshed_at, created_at, updated_at FROM microblog_publications`).Scan(&pubs)
	}
	for _, p := range pubs {
		target := companion.InstanceID(p.Platform, p.TargetName)
		if t, ok := l.Target(p.TargetName); ok {
			target = t.InstanceID()
		}
		if l.Check {
			continue
		}
		db.Create(&Publication{ID: p.ID, PostID: p.PostID, Target: target, TargetName: p.TargetName, Platform: p.Platform,
			PostUrl: p.PostURL, ExternalID: p.ExternalID, VersionId: p.VersionID, Success: p.Success, ErrorMessage: p.ErrorMessage,
			Likes: likes[slugOf[p.PostID]+"|"+p.TargetName], LikesRefreshedAt: p.LikesRefreshedAt, CommentsRefreshedAt: p.CommentsRefreshedAt,
			CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt})
	}
	var comments []Comment
	if l.HasTable("microblog_comments") {
		l.DB.Raw("SELECT id, post_id, platform, external_id, author, author_url, avatar_url, body, posted_at, imported_at FROM microblog_comments").Scan(&comments)
		if !l.Check {
			for _, c := range comments {
				db.Create(&c)
			}
		}
	}
	files := 0
	dir := filepath.Join(l.DataDir, "microblog")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files++
		if l.Check {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if err := mc.Blobs().Put(bgCtx(), e.Name(), bytes.NewReader(data), int64(len(data)), mime.TypeByExtension(filepath.Ext(e.Name()))); err != nil {
			return err
		}
	}
	l.Reportf("microblog: %d post(s), %d publication(s), %d comment(s), %d image(s) into the blob store", len(posts), len(pubs), len(comments), files)
	return nil
}

func bgCtx() context.Context { return context.Background() }
