package api

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rzalawad/tdm/server/pkg/core"
	"github.com/rzalawad/tdm/server/pkg/daemon"
	"gorm.io/gorm"
)

// DownloadRequest represents a request to download files
type DownloadRequest struct {
	URLs      []string `json:"urls" binding:"required"`
	Directory string   `json:"directory"`
	Task      string   `json:"task"`
}

// ConcurrencyRequest represents a request to update concurrency settings
type ConcurrencyRequest struct {
	Concurrency int `json:"concurrency" binding:"required,min=1"`
}

// DownloadResponse represents a serialized Download object
type DownloadResponse struct {
	ID         uint   `json:"id"`
	URL        string `json:"url"`
	Directory  string `json:"directory"`
	Status     string `json:"status"`
	Speed      string `json:"speed"`
	Downloaded int    `json:"downloaded"`
	TotalSize  int    `json:"total_size"`
	DateAdded  string `json:"date_added"`
	Progress   string `json:"progress"`
	Error      string `json:"error"`
	Gid        string `json:"gid"`
	GroupID    uint   `json:"group_id"`
	GroupTask  string `json:"group_task"`
}

// SetupRoutes configures the API routes
func SetupRoutes(router *gin.Engine, daemonChan chan daemon.DaemonMessage, serverChan chan daemon.ServerMessage) {

	router.Use(func(c *gin.Context) {
		c.Set("daemonChannel", daemonChan)
		c.Set("serverChannel", serverChan)
		c.Next()
	})

	router.POST("/download", handleDownload)
	router.DELETE("/delete/:id", handleDeleteDownload)
	router.PUT("/settings/concurrency", handleUpdateConcurrency)
	router.GET("/download/:id", handleGetDownload)
	router.GET("/downloads", handleGetDownloads)
	router.GET("/settings/concurrency", handleGetConcurrency)
}

// handleDownload handles requests to add new downloads
func handleDownload(c *gin.Context) {
	var req DownloadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	if len(req.URLs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No URLs provided"})
		return
	}

	directory := req.Directory
	if directory == "" {
		directory = "."
	}

	// Convert task string to TaskType if provided
	var taskEnum *core.TaskType
	if req.Task != "" {
		task := core.TaskType(req.Task)

		// Validate task type
		validTask := false
		for _, validType := range []core.TaskType{core.TaskTypeUnpack, core.TaskTypeMove} {
			if task == validType {
				validTask = true
				break
			}
		}

		if !validTask {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid task type. Must be one of: unpack, move",
			})
			return
		}

		taskEnum = &task
	}

	// Create group and downloads in a transaction
	err := core.Transaction(func(tx *gorm.DB) error {
		// Create new group
		group := core.Group{
			Task:   taskEnum,
			Status: core.GroupStatusPending,
		}

		if err := tx.Create(&group).Error; err != nil {
			return err
		}

		// Add downloads to group
		for _, url := range req.URLs {
			download := core.Download{
				URL:       url,
				Directory: directory,
				Status:    core.StatusPending,
				GroupID:   group.ID,
			}

			if err := tx.Create(&download).Error; err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to insert download request: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Download request with " + strconv.Itoa(len(req.URLs)) + " URL(s) received"})
}

// handleDeleteDownload handles requests to delete downloads
func handleDeleteDownload(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid download ID"})
		return
	}

	db := core.GetDB()
	var download core.Download
	if err := db.Preload("Group").First(&download, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Download " + idStr + " not found"})
		return
	}

	log.Printf("Deleting download: %d", id)

	daemonChan := c.MustGet("daemonChannel").(chan daemon.DaemonMessage)
	serverChan := c.MustGet("serverChannel").(chan daemon.ServerMessage)
	deleteMsg := daemon.DaemonMessage{
		DownloadID: id,
		Action:     "delete",
	}

	// Send the message using a goroutine with timeout to prevent blocking indefinitely
	msgSent := make(chan bool, 1)
	go func() {
		daemonChan <- deleteMsg
		log.Printf("Sent message to daemon channel")
		msgSent <- true
		log.Printf("Message sent to daemon channel")
	}()

	// Wait for message to be sent or timeout
	select {
	case <-msgSent:
		log.Printf("Daemon message sent successfully")
	case <-time.After(5 * time.Second):
		log.Printf("WARNING: Timeout sending message to daemon channel")
		// Continue anyway to delete from database
	}

	// Wait for the message to be processed
	log.Printf("Waiting for message from server channel")
	msg := <-serverChan
	log.Printf("Received message from server channel: %v", msg)

	// After receiving server channel message, handle database cleanup in a transaction
	err = db.Transaction(func(tx *gorm.DB) error {
		// First delete the download
		if err := tx.Delete(&download).Error; err != nil {
			return fmt.Errorf("failed to delete download: %w", err)
		}
		log.Printf("Download %d deleted from database", id)

		// Check if this was the last download in the group
		var remainingDownloads int64
		if err := tx.Model(&core.Download{}).Where("group_id = ?", download.GroupID).Count(&remainingDownloads).Error; err != nil {
			return fmt.Errorf("failed to check remaining downloads: %w", err)
		}

		// If this was the last download, clean up the group and its tasks
		if remainingDownloads == 0 {
			// Delete all tasks for this group
			if err := tx.Where("group_id = ?", download.GroupID).Delete(&core.Task{}).Error; err != nil {
				return fmt.Errorf("failed to delete tasks: %w", err)
			}
			log.Printf("Tasks for group %d deleted from database", download.GroupID)

			// Delete the group
			if err := tx.Delete(&download.Group).Error; err != nil {
				return fmt.Errorf("failed to delete group: %w", err)
			}
			log.Printf("Group %d deleted from database", download.GroupID)
		}

		return nil
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Delete request processed successfully"})
}

// handleUpdateConcurrency handles requests to update the concurrency setting
func handleUpdateConcurrency(c *gin.Context) {
	var req ConcurrencyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid concurrency value"})
		return
	}

	db := core.GetDB()
	var settings core.DaemonSettings
	result := db.First(&settings, 1)

	// Create settings if not found
	if result.Error != nil {
		settings = core.DaemonSettings{
			ID:          1,
			Concurrency: req.Concurrency,
		}
		if err := db.Create(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create daemon settings: " + err.Error()})
			return
		}
	} else {
		// Update existing settings
		settings.Concurrency = req.Concurrency
		if err := db.Save(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update concurrency: " + err.Error()})
			return
		}
	}
	config, err := core.GetConfig()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"concurrency": 1})
		return
	}

	aria2Client := daemon.NewAria2JsonRPC(fmt.Sprintf("http://localhost:%d/jsonrpc", config.Daemon.Aria2.Port))
	if err := aria2Client.SetConcurrency(req.Concurrency); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update concurrency: " + err.Error()})
	}

	c.JSON(http.StatusOK, gin.H{"message": "Concurrency updated successfully"})
}

// handleGetDownload handles requests to get a specific download
func handleGetDownload(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid download ID"})
		return
	}

	db := core.GetDB()
	var download core.Download
	if err := db.Preload("Group").First(&download, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Download id " + idStr + " not found"})
		return
	}

	// Format the response
	response := formatDownloadResponse(&download)
	c.JSON(http.StatusOK, response)
}

// handleGetDownloads handles requests to get all downloads
func handleGetDownloads(c *gin.Context) {
	db := core.GetDB()
	var downloads []core.Download
	if err := db.Preload("Group").Order("id desc").Find(&downloads).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch downloads: " + err.Error()})
		return
	}

	// Format the response
	response := make([]DownloadResponse, len(downloads))
	for i, download := range downloads {
		response[i] = formatDownloadResponse(&download)
	}

	c.JSON(http.StatusOK, response)
}

// handleGetConcurrency handles requests to get the current concurrency setting
func handleGetConcurrency(c *gin.Context) {
	db := core.GetDB()
	var settings core.DaemonSettings
	if err := db.First(&settings, 1).Error; err != nil {
		// Return default concurrency if settings not found
		c.JSON(http.StatusOK, gin.H{"concurrency": 1})
		return
	}
	c.JSON(http.StatusOK, gin.H{"concurrency": settings.Concurrency})
}

// formatDownloadResponse formats a Download entity for the API response
func formatDownloadResponse(download *core.Download) DownloadResponse {
	response := DownloadResponse{
		ID:        download.ID,
		URL:       download.URL,
		Directory: download.Directory,
		Status:    string(download.Status),
		GroupID:   download.GroupID,
		DateAdded: download.DateAdded.Format(time.RFC3339),
	}

	// Add speed in KB/s
	if download.Speed != nil {
		response.Speed = *download.Speed
	} else {
		response.Speed = "N/A"
	}

	if download.Downloaded != nil {
		response.Downloaded = *download.Downloaded
	}

	if download.TotalSize != nil {
		response.TotalSize = *download.TotalSize
	}

	if download.Progress != nil {
		response.Progress = *download.Progress
	} else {
		response.Progress = "0%"
	}

	if download.Error != nil {
		response.Error = *download.Error
	}

	if download.Gid != nil {
		response.Gid = *download.Gid
	}

	// Add group task if available
	if download.Group.Task != nil {
		response.GroupTask = string(*download.Group.Task)
	}

	return response
}
