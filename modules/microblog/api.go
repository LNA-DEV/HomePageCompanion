package microblog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
)

const maxImageBytes = 8 << 20 // 8 MiB

func blog(mc companion.ModuleContext) *Blog {
	for _, in := range mc.Instances() {
		return in.Instance.(*Blog)
	}
	return nil
}

type publicationView struct {
	Publication
	Error string `json:"errorOnAttempt,omitempty"`
}

type adminPostView struct {
	Post
	Publications []publicationView `json:"publications"`
}

func adminView(mc companion.ModuleContext, p Post) adminPostView {
	var pubs []Publication
	mc.DB().Where("post_id = ?", p.ID).Find(&pubs)
	views := make([]publicationView, 0, len(pubs))
	for _, pub := range pubs {
		views = append(views, publicationView{Publication: pub})
	}
	return adminPostView{Post: p, Publications: views}
}

// The website's view of a post.
type publicPublicationView struct {
	Platform   string `json:"platform"`
	TargetName string `json:"targetName"`
	PostUrl    string `json:"postUrl,omitempty"`
}

type publicPostView struct {
	ID             uint                    `json:"id"`
	Slug           string                  `json:"slug"`
	Body           string                  `json:"body"`
	ContentWarning string                  `json:"contentWarning,omitempty"`
	ImageURL       string                  `json:"imageUrl,omitempty"`
	ImageAltText   string                  `json:"imageAltText,omitempty"`
	CreatedAt      time.Time               `json:"createdAt"`
	LikeCount      int                     `json:"likeCount"`
	CommentCount   int64                   `json:"commentCount"`
	Publications   []publicPublicationView `json:"publications"`
}

func publicView(mc companion.ModuleContext, p Post) publicPostView {
	v := publicPostView{ID: p.ID, Slug: p.Slug, Body: p.Body, ContentWarning: p.ContentWarning, ImageURL: p.ImageURL,
		ImageAltText: p.ImageAltText, CreatedAt: p.CreatedAt}
	var pubs []Publication
	mc.DB().Where("post_id = ? AND success = ?", p.ID, true).Order("id").Find(&pubs)
	likes := 0
	for _, pub := range pubs {
		v.Publications = append(v.Publications, publicPublicationView{Platform: pub.Platform, TargetName: pub.TargetName, PostUrl: deref(pub.PostUrl)})
		likes += pub.Likes
	}
	v.LikeCount = likes + p.NativeLikes
	mc.DB().Model(&Comment{}).Where("post_id = ?", p.ID).Count(&v.CommentCount)
	return v
}

func routes(mc companion.ModuleContext, r *companion.Router) {
	a := r.Admin
	a.POST("/posts", func(c *gin.Context) {
		var req struct {
			Body           string `json:"body"`
			ContentWarning string `json:"contentWarning"`
			ImageURL       string `json:"imageUrl"`
			ImageAltText   string `json:"imageAltText"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
			return
		}
		b := blog(mc)
		if b == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "microblog not configured"})
			return
		}
		post, pubs, err := b.Create(c.Request.Context(), req.Body, req.ContentWarning, req.ImageURL, req.ImageAltText)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		views := make([]publicationView, 0, len(pubs))
		for _, p := range pubs {
			pv := publicationView{Publication: p}
			if !p.Success {
				pv.Error = p.ErrorMessage
			}
			views = append(views, pv)
		}
		c.JSON(http.StatusCreated, adminPostView{Post: *post, Publications: views})
	})
	a.GET("/posts", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if page < 1 {
			page = 1
		}
		if limit < 1 || limit > 200 {
			limit = 50
		}
		q := mc.DB().Model(&Post{})
		if term := strings.TrimSpace(c.Query("q")); term != "" {
			q = q.Where("LOWER(body) LIKE ?", "%"+strings.ToLower(term)+"%")
		}
		var total int64
		q.Count(&total)
		var posts []Post
		q.Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&posts)
		views := make([]adminPostView, 0, len(posts))
		for _, p := range posts {
			views = append(views, adminView(mc, p))
		}
		c.JSON(http.StatusOK, gin.H{"items": views, "total": total, "page": page, "limit": limit, "targets": targets(mc)})
	})
	a.GET("/posts/:id", func(c *gin.Context) {
		var p Post
		if mc.DB().Where("id = ?", c.Param("id")).Limit(1).Find(&p).RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
			return
		}
		var comments []Comment
		mc.DB().Where("post_id = ?", p.ID).Order("posted_at desc").Find(&comments)
		c.JSON(http.StatusOK, gin.H{"post": adminView(mc, p), "comments": comments, "public": publicView(mc, p)})
	})
	a.DELETE("/posts/:id", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
		b := blog(mc)
		if b == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "microblog not configured"})
			return
		}
		if err := b.Delete(c.Request.Context(), uint(id)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "deleted"})
	})
	a.POST("/posts/:id/retry/:publicationId", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("publicationId"), 10, 32)
		b := blog(mc)
		if b == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "microblog not configured"})
			return
		}
		pub, err := b.Retry(c.Request.Context(), uint(id))
		switch {
		case err != nil && strings.Contains(err.Error(), "already succeeded"):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case err != nil:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		default:
			pv := publicationView{Publication: pub}
			if !pub.Success {
				pv.Error = pub.ErrorMessage
			}
			c.JSON(http.StatusOK, pv)
		}
	})
	a.POST("/posts/:id/refresh", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
		b := blog(mc)
		if b == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "microblog not configured"})
			return
		}
		if err := b.RefreshPost(c.Request.Context(), uint(id)); err != nil {
			if errors.Is(err, errBusy) {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		var p Post
		mc.DB().First(&p, id)
		c.JSON(http.StatusOK, publicView(mc, p))
	})
	// upload stores an image in the blob store and returns the URL to put
	// into the post.
	a.POST("/upload", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImageBytes)
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expected multipart field 'file'"})
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil || len(data) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "empty or unreadable file"})
			return
		}
		sum := sha256.Sum256(data)
		name := mediaName(strings.ToLower(filepath.Ext(header.Filename)), hex.EncodeToString(sum[:]))
		ct := mime.TypeByExtension(filepath.Ext(name))
		if _, err := mc.Blobs().Stat(c.Request.Context(), name); err != nil {
			if err := mc.Blobs().Put(c.Request.Context(), name, bytes.NewReader(data), int64(len(data)), ct); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed: " + err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"url": mediaPath + name, "size": len(data)})
	})

	pub := r.Alias("/api/microblog")
	pub.GET("/posts", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if limit < 1 || limit > 100 {
			limit = 20
		}
		q := mc.DB().Model(&Post{}).Order("id DESC").Limit(limit)
		if before, _ := strconv.ParseUint(c.Query("before"), 10, 32); before > 0 {
			q = q.Where("id < ?", before)
		}
		var posts []Post
		q.Find(&posts)
		out := make([]publicPostView, 0, len(posts))
		for _, p := range posts {
			out = append(out, publicView(mc, p))
		}
		c.JSON(http.StatusOK, out)
	})
	bySlug := func(c *gin.Context) (Post, bool) {
		var p Post
		if mc.DB().Where("slug = ?", c.Param("slug")).Limit(1).Find(&p).RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
			return p, false
		}
		return p, true
	}
	pub.GET("/posts/:slug", func(c *gin.Context) {
		if p, ok := bySlug(c); ok {
			c.JSON(http.StatusOK, publicView(mc, p))
		}
	})
	pub.GET("/posts/:slug/comments", func(c *gin.Context) {
		p, ok := bySlug(c)
		if !ok {
			return
		}
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if limit < 1 || limit > 200 {
			limit = 50
		}
		q := mc.DB().Model(&Comment{}).Where("post_id = ?", p.ID).Order("id DESC").Limit(limit)
		if before, _ := strconv.ParseUint(c.Query("before"), 10, 32); before > 0 {
			q = q.Where("id < ?", before)
		}
		comments := []Comment{}
		q.Find(&comments)
		c.JSON(http.StatusOK, comments)
	})
	pub.GET("/posts/:slug/likes", func(c *gin.Context) {
		p, ok := bySlug(c)
		if !ok {
			return
		}
		type likes struct {
			Platform string `json:"platform"`
			Count    int    `json:"count"`
		}
		out := []likes{}
		var pubs []Publication
		mc.DB().Where("post_id = ? AND success = ? AND likes_refreshed_at IS NOT NULL", p.ID, true).Order("id").Find(&pubs)
		for _, pub := range pubs {
			out = append(out, likes{Platform: pub.Platform, Count: pub.Likes})
		}
		out = append(out, likes{Platform: "native", Count: p.NativeLikes})
		c.JSON(http.StatusOK, out)
	})
	pub.GET("/media/:sha", func(c *gin.Context) {
		name := c.Param("sha")
		if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
			return
		}
		rc, info, err := mc.Blobs().Get(c.Request.Context(), name)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer rc.Close()
		ct := info.ContentType
		if ct == "" {
			ct = mime.TypeByExtension(filepath.Ext(name))
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.DataFromReader(http.StatusOK, info.Size, ct, rc, nil)
	})
}

// targets are the publishTo targets, for the compose form.
func targets(mc companion.ModuleContext) []string {
	if b := blog(mc); b != nil {
		return b.PublishToIDs
	}
	return nil
}
