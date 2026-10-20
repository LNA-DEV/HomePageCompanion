package interactions

import (
	"context"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
)

// LikesResponse is one platform's likes, as the website reads them.
type LikesResponse struct {
	Platform string `json:"platform"`
	Likes    int    `json:"likes"`
}

// NativeLikeRequest and NativeLikeResponse are the website's like button.
type NativeLikeRequest struct {
	Token string `json:"token"`
}

type NativeLikeResponse struct {
	Success   bool   `json:"success"`
	Token     string `json:"token,omitempty"`
	LikeCount int    `json:"like_count"`
	HasLiked  bool   `json:"has_liked"`
	Message   string `json:"message,omitempty"`
}

func counter(mc companion.ModuleContext) *Counter {
	for _, in := range mc.Instances() {
		return in.Instance.(*Counter)
	}
	return nil
}

func routes(mc companion.ModuleContext, r *companion.Router) {
	pub := r.Alias("/api/interactions")

	// The likes of one item per platform, website likes included as
	// "native"; target_name is "all" or one target.
	pub.GET("/post/:target_name/:item_id", func(c *gin.Context) {
		itemID, target := c.Param("item_id"), c.Param("target_name")
		var rows []Tracked
		q := mc.DB().Where("item_id = ?", itemID).Order("id")
		if target != "all" && target != "native" {
			q = q.Where("target = ? OR target LIKE ?", target, "%:"+target)
		}
		out := []LikesResponse{}
		if target != "native" {
			if err := q.Find(&rows).Error; err != nil {
				c.Data(http.StatusInternalServerError, "application/text", []byte(err.Error()))
				return
			}
			for _, t := range rows {
				out = append(out, LikesResponse{Platform: t.Platform, Likes: t.Likes})
			}
		}
		if target == "all" || target == "native" {
			var nc NativeCount
			if mc.DB().Where("item_id = ?", itemID).Limit(1).Find(&nc).RowsAffected > 0 {
				out = append(out, LikesResponse{Platform: "native", Likes: nc.Likes})
			}
		}
		c.JSON(http.StatusOK, out)
	})

	pub.POST("/native/:item_id/like", func(c *gin.Context) {
		n := counter(mc)
		if n == nil {
			c.JSON(http.StatusServiceUnavailable, NativeLikeResponse{Message: "likes are off"})
			return
		}
		itemID := c.Param("item_id")
		ipHash := n.hashIP(c.ClientIP())
		var req NativeLikeRequest
		_ = c.ShouldBindJSON(&req)
		token := req.Token
		if token == "" {
			token = newToken()
		}
		var exists int64
		mc.DB().Model(&NativeLike{}).Where("item_id = ? AND (ip_hash = ? OR token = ?)", itemID, ipHash, token).Count(&exists)
		if exists > 0 {
			c.JSON(http.StatusConflict, NativeLikeResponse{Token: token, LikeCount: n.nativeCount(itemID), HasLiked: true, Message: "Already liked"})
			return
		}
		if err := mc.DB().Create(&NativeLike{ItemID: itemID, IPHash: ipHash, Token: token}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, NativeLikeResponse{Message: "Failed to save like"})
			return
		}
		n.nativeChanged(c.Request.Context(), itemID)
		c.JSON(http.StatusOK, NativeLikeResponse{Success: true, Token: token, LikeCount: n.nativeCount(itemID), HasLiked: true})
	})

	pub.DELETE("/native/:item_id/like", func(c *gin.Context) {
		n := counter(mc)
		if n == nil {
			c.JSON(http.StatusServiceUnavailable, NativeLikeResponse{Message: "likes are off"})
			return
		}
		itemID := c.Param("item_id")
		var req NativeLikeRequest
		if err := c.ShouldBindJSON(&req); err != nil || req.Token == "" {
			c.JSON(http.StatusBadRequest, NativeLikeResponse{Message: "Token required for unlike"})
			return
		}
		res := mc.DB().Where("item_id = ? AND ip_hash = ? AND token = ?", itemID, n.hashIP(c.ClientIP()), req.Token).Delete(&NativeLike{})
		if res.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, NativeLikeResponse{Token: req.Token, LikeCount: n.nativeCount(itemID), Message: "Like not found"})
			return
		}
		n.nativeChanged(c.Request.Context(), itemID)
		c.JSON(http.StatusOK, NativeLikeResponse{Success: true, Token: req.Token, LikeCount: n.nativeCount(itemID)})
	})

	pub.GET("/native/:item_id/status", func(c *gin.Context) {
		n := counter(mc)
		if n == nil {
			c.JSON(http.StatusServiceUnavailable, NativeLikeResponse{Message: "likes are off"})
			return
		}
		itemID := c.Param("item_id")
		liked := false
		if token := c.Query("token"); token != "" {
			var cnt int64
			mc.DB().Model(&NativeLike{}).Where("item_id = ? AND ip_hash = ? AND token = ?", itemID, n.hashIP(c.ClientIP()), token).Count(&cnt)
			liked = cnt > 0
		}
		c.JSON(http.StatusOK, NativeLikeResponse{Success: true, LikeCount: n.nativeCount(itemID), HasLiked: liked})
	})

	// The old manual trigger.
	pub.POST("/fetch", r.RequireAdmin, func(c *gin.Context) {
		if n := counter(mc); n != nil {
			go func() {
				defer recoverLog(mc)
				_ = n.Sweep(context.Background())
			}()
		}
		c.JSON(http.StatusAccepted, gin.H{"status": "Interactions fetch scheduled"})
	})

	r.Admin.GET("/tracked", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		if page < 1 {
			page = 1
		}
		q := mc.DB().Model(&Tracked{})
		if term := strings.TrimSpace(c.Query("q")); term != "" {
			q = q.Where("item_id LIKE ? OR target LIKE ?", "%"+term+"%", "%"+term+"%")
		}
		if c.Query("errors") == "true" {
			q = q.Where("last_error <> ''")
		}
		var total int64
		q.Count(&total)
		var rows []Tracked
		q.Order("likes_at desc").Offset((page - 1) * 50).Limit(50).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total})
	})
	r.Admin.POST("/refresh", func(c *gin.Context) {
		var body struct {
			ItemID string `json:"itemId"`
		}
		_ = c.ShouldBindJSON(&body)
		n := counter(mc)
		if n == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not configured"})
			return
		}
		var err error
		if body.ItemID != "" {
			err = n.RefreshItem(c.Request.Context(), body.ItemID)
		} else {
			go func() {
				defer recoverLog(mc)
				_ = n.Sweep(context.Background())
			}()
		}
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"status": "refreshing"})
	})
	r.Admin.GET("/summary", func(c *gin.Context) {
		var tracked, native int64
		var likes int64
		mc.DB().Model(&Tracked{}).Count(&tracked)
		mc.DB().Model(&Tracked{}).Select("COALESCE(SUM(likes), 0)").Scan(&likes)
		mc.DB().Model(&NativeLike{}).Count(&native)
		c.JSON(http.StatusOK, gin.H{"tracked": tracked, "likes": likes, "websiteLikes": native})
	})
}

func recoverLog(mc companion.ModuleContext) {
	if r := recover(); r != nil {
		mc.Log().Printf("panic: %v\n%s", r, debug.Stack())
	}
}
