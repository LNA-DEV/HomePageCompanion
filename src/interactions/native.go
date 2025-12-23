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
	itemName := c.Param("item_name")
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
	ipExists := database.Db.Where("item_name = ? AND ip_hash = ?", itemName, ipHash).First(&ipLike).Error == nil

	// Check if token has already liked this item
	var tokenLike models.NativeLike
	tokenExists := database.Db.Where("item_name = ? AND token = ?", itemName, token).First(&tokenLike).Error == nil

	// Block if IP OR token has already liked (AND logic for allowing)
	if ipExists || tokenExists {
		likeCount := getNativeLikeCount(itemName)
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
		ItemName: itemName,
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
	updateNativeInteractionCount(itemName)

	likeCount := getNativeLikeCount(itemName)
	c.JSON(http.StatusOK, NativeLikeResponse{
		Success:   true,
		Token:     token,
		LikeCount: likeCount,
		HasLiked:  true,
	})
}

// HandleNativeUnlike handles DELETE requests to unlike an item
func HandleNativeUnlike(c *gin.Context) {
	itemName := c.Param("item_name")
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
	result := database.Db.Where("item_name = ? AND ip_hash = ? AND token = ?", itemName, ipHash, req.Token).Delete(&models.NativeLike{})

	if result.RowsAffected == 0 {
		likeCount := getNativeLikeCount(itemName)
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
	updateNativeInteractionCount(itemName)

	likeCount := getNativeLikeCount(itemName)
	c.JSON(http.StatusOK, NativeLikeResponse{
		Success:   true,
		Token:     req.Token,
		LikeCount: likeCount,
		HasLiked:  false,
	})
}

// HandleNativeLikeStatus handles GET requests to check like status
func HandleNativeLikeStatus(c *gin.Context) {
	itemName := c.Param("item_name")
	ipHash := hashIP(c.ClientIP())
	token := c.Query("token")

	hasLiked := false

	if token != "" {
		// Check if this IP hash + token combo has liked
		var like models.NativeLike
		if database.Db.Where("item_name = ? AND ip_hash = ? AND token = ?", itemName, ipHash, token).First(&like).Error == nil {
			hasLiked = true
		}
	}

	// Also check if IP hash alone has liked (for cases where token was lost)
	if !hasLiked {
		var ipLike models.NativeLike
		if database.Db.Where("item_name = ? AND ip_hash = ?", itemName, ipHash).First(&ipLike).Error == nil {
			hasLiked = true
		}
	}

	likeCount := getNativeLikeCount(itemName)
	c.JSON(http.StatusOK, NativeLikeResponse{
		Success:   true,
		LikeCount: likeCount,
		HasLiked:  hasLiked,
	})
}

func getNativeLikeCount(itemName string) int {
	var count int64
	database.Db.Model(&models.NativeLike{}).Where("item_name = ?", itemName).Count(&count)
	return int(count)
}

func updateNativeInteractionCount(itemName string) {
	likeCount := getNativeLikeCount(itemName)

	var interaction models.Interaction
	result := database.Db.Where("item_name = ? AND platform = ? AND target_name = ?", itemName, nativePlatform, nativeTargetName).First(&interaction)

	if result.Error != nil {
		// Create new
		interaction = models.Interaction{
			ItemName:   itemName,
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
