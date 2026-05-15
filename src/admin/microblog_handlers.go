package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/interactions"
	mblog "github.com/LNA-DEV/HomePageCompanion/microblog"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

const microblogMediaDir = "data/microblog"
const microblogMaxImageBytes = 8 << 20 // 8 MiB

// RegisterMicroblogRoutes wires both the admin (auth-protected) and public
// (open) microblog endpoints. Call from main once routes are about to be
// registered.
func RegisterMicroblogRoutes(api *gin.RouterGroup, authMiddleware gin.HandlerFunc) {
	adm := api.Group("/admin/microblog")
	adm.Use(authMiddleware)
	{
		adm.POST("/posts", CreateMicroblogPost)
		adm.GET("/posts", ListMicroblogPostsAdmin)
		adm.GET("/posts/:id", GetMicroblogPostAdmin)
		adm.DELETE("/posts/:id", DeleteMicroblogPost)
		adm.POST("/posts/:id/retry/:publicationId", RetryMicroblogPublication)
		adm.POST("/posts/:id/refresh", RefreshMicroblogPost)
		adm.POST("/upload", UploadMicroblogImage)
	}

	pub := api.Group("/microblog")
	{
		pub.GET("/posts", ListMicroblogPostsPublic)
		pub.GET("/posts/:slug", GetMicroblogPostPublic)
		pub.GET("/posts/:slug/comments", ListMicroblogCommentsPublic)
		pub.GET("/posts/:slug/likes", ListMicroblogLikesPublic)
		pub.GET("/media/:sha", ServeMicroblogMedia)
	}
}

// ---------------------------------------------------------------------------
// Admin: create / list / get / delete / retry / refresh / upload

type createMicroblogRequest struct {
	Body           string `json:"body"`
	ContentWarning string `json:"contentWarning"`
	ImageURL       string `json:"imageUrl"`
	ImageAltText   string `json:"imageAltText"`
}

type publicationView struct {
	models.MicroblogPublication
	Error string `json:"errorOnAttempt,omitempty"`
}

type adminPostView struct {
	models.MicroblogPost
	Publications []publicationView `json:"publications"`
}

func CreateMicroblogPost(c *gin.Context) {
	var req createMicroblogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}
	post, results, err := mblog.Create(req.Body, req.ContentWarning, req.ImageURL, req.ImageAltText)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pubs := make([]publicationView, 0, len(results))
	for _, r := range results {
		pv := publicationView{MicroblogPublication: r.Publication}
		if r.Err != nil {
			pv.Error = r.Err.Error()
		}
		pubs = append(pubs, pv)
	}
	c.JSON(http.StatusCreated, adminPostView{MicroblogPost: *post, Publications: pubs})
}

func ListMicroblogPostsAdmin(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := (page - 1) * limit

	var posts []models.MicroblogPost
	var total int64
	database.Db.Model(&models.MicroblogPost{}).Count(&total)
	database.Db.Order("created_at DESC").Offset(offset).Limit(limit).Find(&posts)

	views := make([]adminPostView, 0, len(posts))
	for _, p := range posts {
		views = append(views, hydrateAdminPost(p))
	}
	c.JSON(http.StatusOK, gin.H{"items": views, "total": total, "page": page, "limit": limit})
}

func GetMicroblogPostAdmin(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var post models.MicroblogPost
	if err := database.Db.First(&post, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}
	c.JSON(http.StatusOK, hydrateAdminPost(post))
}

func DeleteMicroblogPost(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := mblog.Delete(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func RetryMicroblogPublication(c *gin.Context) {
	pubIDStr := c.Param("publicationId")
	pubID, err := strconv.ParseUint(pubIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid publication id"})
		return
	}
	result, err := mblog.Retry(uint(pubID))
	if err != nil {
		if strings.Contains(err.Error(), "already succeeded") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	pv := publicationView{MicroblogPublication: result.Publication}
	if result.Err != nil {
		pv.Error = result.Err.Error()
	}
	c.JSON(http.StatusOK, pv)
}

// RefreshMicroblogPost triggers an on-demand refresh of likes + comments for
// every successful publication of this post. Shares the scheduler's runMu so
// it cannot overlap with a cron tick or a manual full sweep — returns 409 in
// that case so the user can retry. On success returns the rehydrated
// publicPostView so the page can replace its state without a second GET.
func RefreshMicroblogPost(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := interactions.RefreshSinglePost(uint(id)); err != nil {
		if errors.Is(err, interactions.ErrRefreshBusy) {
			c.JSON(http.StatusConflict, gin.H{"error": "another refresh is running, try again in a moment"})
			return
		}
		log.Printf("microblog: refresh post id=%d failed: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var post models.MicroblogPost
	if err := database.Db.First(&post, id).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "refreshed"})
		return
	}
	c.JSON(http.StatusOK, hydratePublicPost(post))
}

// UploadMicroblogImage stores a multipart-uploaded image under
// data/microblog/<sha256>.bin and returns a URL the admin can paste into
// the post create form.
func UploadMicroblogImage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, microblogMaxImageBytes)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected multipart field 'file'"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read failed"})
		return
	}
	if len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty file"})
		return
	}

	if err := os.MkdirAll(microblogMediaDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create media dir"})
		return
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = ".bin"
	}
	path := filepath.Join(microblogMediaDir, sha+ext)
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed"})
			return
		}
	}

	// Public URL for the image (served by the public route below).
	publicURL := "/api/microblog/media/" + sha + ext
	c.JSON(http.StatusOK, gin.H{"url": publicURL, "size": len(data)})
}

// ---------------------------------------------------------------------------
// Public: list / get / comments / likes / media

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

func ListMicroblogPostsPublic(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	before, _ := strconv.ParseUint(c.Query("before"), 10, 32)

	query := database.Db.Model(&models.MicroblogPost{}).Order("id DESC").Limit(limit)
	if before > 0 {
		query = query.Where("id < ?", before)
	}

	var posts []models.MicroblogPost
	query.Find(&posts)

	out := make([]publicPostView, 0, len(posts))
	for _, p := range posts {
		out = append(out, hydratePublicPost(p))
	}
	c.JSON(http.StatusOK, out)
}

func GetMicroblogPostPublic(c *gin.Context) {
	slug := c.Param("slug")
	var post models.MicroblogPost
	if err := database.Db.Where("slug = ?", slug).First(&post).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}
	c.JSON(http.StatusOK, hydratePublicPost(post))
}

func ListMicroblogCommentsPublic(c *gin.Context) {
	slug := c.Param("slug")
	var post models.MicroblogPost
	if err := database.Db.Where("slug = ?", slug).First(&post).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	before, _ := strconv.ParseUint(c.Query("before"), 10, 32)

	query := database.Db.Model(&models.MicroblogComment{}).
		Where("post_id = ?", post.ID).
		Order("id DESC").
		Limit(limit)
	if before > 0 {
		query = query.Where("id < ?", before)
	}
	var comments []models.MicroblogComment
	query.Find(&comments)
	c.JSON(http.StatusOK, comments)
}

type publicLikesView struct {
	Platform string `json:"platform"`
	Count    int    `json:"count"`
}

func ListMicroblogLikesPublic(c *gin.Context) {
	slug := c.Param("slug")
	var post models.MicroblogPost
	if err := database.Db.Where("slug = ?", slug).First(&post).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}

	out := []publicLikesView{}
	var rows []models.Interaction
	database.Db.Where("source = ? AND item_id = ?", "microblog", slug).Find(&rows)
	for _, r := range rows {
		out = append(out, publicLikesView{Platform: r.Platform, Count: r.LikeCount})
	}
	var nativeCount int64
	database.Db.Model(&models.NativeLike{}).Where("item_id = ?", slug).Count(&nativeCount)
	out = append(out, publicLikesView{Platform: "native", Count: int(nativeCount)})

	c.JSON(http.StatusOK, out)
}

// ServeMicroblogMedia streams a previously-uploaded media file. Path
// traversal is blocked by validating the sha portion against the file ext.
func ServeMicroblogMedia(c *gin.Context) {
	name := c.Param("sha")
	if strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	path := filepath.Join(microblogMediaDir, name)
	abs, err := filepath.Abs(path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}
	dirAbs, _ := filepath.Abs(microblogMediaDir)
	if !strings.HasPrefix(abs, dirAbs+string(filepath.Separator)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	c.File(abs)
}

// ---------------------------------------------------------------------------
// Helpers

func hydrateAdminPost(p models.MicroblogPost) adminPostView {
	var pubs []models.MicroblogPublication
	database.Db.Where("post_id = ?", p.ID).Find(&pubs)
	views := make([]publicationView, 0, len(pubs))
	for _, pub := range pubs {
		views = append(views, publicationView{MicroblogPublication: pub})
	}
	return adminPostView{MicroblogPost: p, Publications: views}
}

func hydratePublicPost(p models.MicroblogPost) publicPostView {
	v := publicPostView{
		ID:             p.ID,
		Slug:           p.Slug,
		Body:           p.Body,
		ContentWarning: p.ContentWarning,
		ImageURL:       p.ImageURL,
		ImageAltText:   p.ImageAltText,
		CreatedAt:      p.CreatedAt,
	}
	var pubs []models.MicroblogPublication
	database.Db.Where("post_id = ? AND success = ?", p.ID, true).Find(&pubs)
	for _, pub := range pubs {
		pv := publicPublicationView{Platform: pub.Platform, TargetName: pub.TargetName}
		if pub.PostUrl != nil {
			pv.PostUrl = *pub.PostUrl
		}
		v.Publications = append(v.Publications, pv)
	}

	// Aggregate likes from Interaction rows (sum across platforms) plus
	// native likes.
	var sumPlatform struct {
		Total int
	}
	database.Db.Model(&models.Interaction{}).
		Select("COALESCE(SUM(like_count),0) as total").
		Where("source = ? AND item_id = ?", "microblog", p.Slug).
		Scan(&sumPlatform)
	var nativeCount int64
	database.Db.Model(&models.NativeLike{}).Where("item_id = ?", p.Slug).Count(&nativeCount)
	v.LikeCount = sumPlatform.Total + int(nativeCount)

	var commentCount int64
	database.Db.Model(&models.MicroblogComment{}).Where("post_id = ?", p.ID).Count(&commentCount)
	v.CommentCount = commentCount

	return v
}

// Unused for now but reserved so handlers compile if we need to ferry
// metadata about a created post downstream.
var _ = fmt.Sprintf
