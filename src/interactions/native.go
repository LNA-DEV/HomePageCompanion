package interactions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

const nativePlatform = "native"
const nativeTargetName = "native"

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

// HandleNativeLike handles POST requests to like an item natively
func HandleNativeLike(c *gin.Context) {
	itemID := c.Param("item_id")
	ipHash := hashIP(c.ClientIP())

	var req NativeLikeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// No token provided, will generate one
		req.Token = ""
	}

	// Generate token if not provided
	token := req.Token
	if token == "" {
		token = generateToken()
	}

	// Check if IP hash has already liked this item
	var ipLike models.NativeLike
	ipExists := database.Db.Where("item_id = ? AND ip_hash = ?", itemID, ipHash).First(&ipLike).Error == nil

	// Check if token has already liked this item
	var tokenLike models.NativeLike
	tokenExists := database.Db.Where("item_id = ? AND token = ?", itemID, token).First(&tokenLike).Error == nil

	// Block if IP OR token has already liked (AND logic for allowing)
	if ipExists || tokenExists {
		likeCount := getNativeLikeCount(itemID)
		c.JSON(http.StatusConflict, NativeLikeResponse{
			Success:   false,
			Token:     token,
			LikeCount: likeCount,
			HasLiked:  true,
			Message:   "Already liked",
		})
		return
	}

	// Create new like with hashed IP
	nativeLike := models.NativeLike{
		ItemID: itemID,
		IPHash:   ipHash,
		Token:    token,
	}

	if err := database.Db.Create(&nativeLike).Error; err != nil {
		c.JSON(http.StatusInternalServerError, NativeLikeResponse{
			Success: false,
			Message: "Failed to save like",
		})
		return
	}

	// Update the interaction count
	updateNativeInteractionCount(itemID)

	likeCount := getNativeLikeCount(itemID)
	c.JSON(http.StatusOK, NativeLikeResponse{
		Success:   true,
		Token:     token,
		LikeCount: likeCount,
		HasLiked:  true,
	})
}

// HandleNativeUnlike handles DELETE requests to unlike an item
func HandleNativeUnlike(c *gin.Context) {
	itemID := c.Param("item_id")
	ipHash := hashIP(c.ClientIP())

	var req NativeLikeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Token == "" {
		c.JSON(http.StatusBadRequest, NativeLikeResponse{
			Success: false,
			Message: "Token required for unlike",
		})
		return
	}

	// Find and delete the like that matches BOTH IP hash and token
	result := database.Db.Where("item_id = ? AND ip_hash = ? AND token = ?", itemID, ipHash, req.Token).Delete(&models.NativeLike{})

	if result.RowsAffected == 0 {
		likeCount := getNativeLikeCount(itemID)
		c.JSON(http.StatusNotFound, NativeLikeResponse{
			Success:   false,
			Token:     req.Token,
			LikeCount: likeCount,
			HasLiked:  false,
			Message:   "Like not found",
		})
		return
	}

	// Update the interaction count
	updateNativeInteractionCount(itemID)

	likeCount := getNativeLikeCount(itemID)
	c.JSON(http.StatusOK, NativeLikeResponse{
		Success:   true,
		Token:     req.Token,
		LikeCount: likeCount,
		HasLiked:  false,
	})
}

// HandleNativeLikeStatus handles GET requests to check like status
func HandleNativeLikeStatus(c *gin.Context) {
	itemID := c.Param("item_id")
	ipHash := hashIP(c.ClientIP())
	token := c.Query("token")

	hasLiked := false

	if token != "" {
		// Check if this IP hash + token combo has liked
		var like models.NativeLike
		if database.Db.Where("item_id = ? AND ip_hash = ? AND token = ?", itemID, ipHash, token).First(&like).Error == nil {
			hasLiked = true
		}
	}

	likeCount := getNativeLikeCount(itemID)
	c.JSON(http.StatusOK, NativeLikeResponse{
		Success:   true,
		LikeCount: likeCount,
		HasLiked:  hasLiked,
	})
}

func getNativeLikeCount(itemID string) int {
	var count int64
	database.Db.Model(&models.NativeLike{}).Where("item_id = ?", itemID).Count(&count)
	return int(count)
}

func updateNativeInteractionCount(itemID string) {
	likeCount := getNativeLikeCount(itemID)

	var interaction models.Interaction
	result := database.Db.Where("item_id = ? AND platform = ? AND target_name = ?", itemID, nativePlatform, nativeTargetName).First(&interaction)

	if result.Error != nil {
		// Create new
		interaction = models.Interaction{
			ItemID:   itemID,
			Platform:   nativePlatform,
			TargetName: nativeTargetName,
			LikeCount:  likeCount,
		}
		database.Db.Create(&interaction)
	} else {
		// Update existing
		interaction.LikeCount = likeCount
		database.Db.Save(&interaction)
	}
}

func generateToken() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func hashIP(ip string) string {
	hash := sha256.Sum256([]byte(ip + config.Data.Security.IPHashSalt))
	return hex.EncodeToString(hash[:])
}
