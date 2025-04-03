package daemon

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rzalawad/tdm/server/pkg/core"
	"gorm.io/gorm"
)

// Aria2DownloadDaemon handles aria2c downloads
type Aria2DownloadDaemon struct {
	config        *core.DaemonConfig
	aria2Client   *Aria2JsonRPC
	aria2Cmd      *exec.Cmd
	running       bool
	wg            sync.WaitGroup
	db            *gorm.DB
	workProcessor *WorkProcessor
	daemonChan    chan DaemonMessage
	serverChan    chan ServerMessage
}

type ServerMessage struct {
	DownloadID int
	Status     int
}

type DaemonMessage struct {
	DownloadID int
	Action     string
}

type HandlerAction struct {
	DownloadID int
	Action     string
}

type CompletionMessage struct {
	DownloadID int
	Status     int
}

const DAEMON_SLEEP = 100 * time.Millisecond
const DOWNLOAD_HANDLER_SLEEP = 1 * time.Second

// NewAria2DownloadDaemon creates a new download daemon
func NewAria2DownloadDaemon(config *core.DaemonConfig, daemonChan chan DaemonMessage, serverChan chan ServerMessage) *Aria2DownloadDaemon {
	daemon := &Aria2DownloadDaemon{
		config:      config,
		aria2Client: NewAria2JsonRPC(fmt.Sprintf("http://localhost:%d/jsonrpc", config.Aria2.Port)),
		running:     false,
		daemonChan:  daemonChan,
		db:          core.GetDB(),
		serverChan:  serverChan,
	}

	// Create temporary download directory if specified
	if config.TemporaryDownloadDirectory != "" {
		if err := os.MkdirAll(config.TemporaryDownloadDirectory, 0755); err != nil {
			log.Printf("Error creating temporary download directory: %v", err)
		}
	}

	// Create work processor
	daemon.workProcessor = NewWorkProcessor(config)

	return daemon
}

// startAria2c starts the aria2c process if it's not already running
func (d *Aria2DownloadDaemon) startAria2c() error {
	// Check if aria2c is already running
	if IsAria2Running() {
		log.Println("aria2c is already running")
		return nil
	}

	// Build command from config
	cmd := d.config.Aria2.BuildCommand()

	// Start the aria2c process
	d.aria2Cmd = exec.Command(cmd[0], cmd[1:]...)

	// Capture stdout and stderr
	d.aria2Cmd.Stdout = os.Stdout
	d.aria2Cmd.Stderr = os.Stderr

	if err := d.aria2Cmd.Start(); err != nil {
		return fmt.Errorf("failed to start aria2c: %w", err)
	}

	log.Println("Started aria2c process")

	// Wait a moment for aria2c to start up
	time.Sleep(1 * time.Second)

	// Check if it's running
	if !IsAria2Running() {
		return fmt.Errorf("aria2c process failed to start properly")
	}

	return nil
}

// cleanupAria2c terminates the aria2c process if it was started by us
func (d *Aria2DownloadDaemon) cleanupAria2c() {
	if d.aria2Cmd != nil && d.aria2Cmd.Process != nil {
		log.Println("Terminating aria2c process...")

		// Try to terminate gracefully first
		if err := d.aria2Cmd.Process.Signal(os.Interrupt); err != nil {
			log.Printf("Error terminating aria2c process: %v", err)

			// If that fails, force kill it
			if err := d.aria2Cmd.Process.Kill(); err != nil {
				log.Printf("Error killing aria2c process: %v", err)
			} else {
				log.Println("Forcefully killed aria2c process")
			}
		}

		// Wait for the process to exit
		_, err := d.aria2Cmd.Process.Wait()
		if err != nil {
			log.Printf("Error waiting for aria2c process to exit: %v", err)
		}

		d.aria2Cmd = nil
		log.Println("Aria2c process terminated")
	}
}

// cleanupOldDownloads removes downloads that are older than the expiration time
func (d *Aria2DownloadDaemon) cleanupOldDownloads() {
	expireDuration, err := ParseDuration(d.config.ExpireDownloads)
	if err != nil {
		log.Printf("Error parsing expire_downloads duration: %v", err)
		return
	}

	cutoffTime := time.Now().Add(-expireDuration)

	var downloads []core.Download

	result := d.db.Where("date_added < ?", cutoffTime).Find(&downloads)
	if result.Error != nil {
		log.Printf("Error querying old downloads: %v", result.Error)
		return
	}

	log.Printf("Found %d downloads older than %s", len(downloads), d.config.ExpireDownloads)

	for _, download := range downloads {
		log.Printf("Deleting old download: ID=%d, URL=%s, Added=%s",
			download.ID, download.URL, download.DateAdded.Format(time.RFC3339))

		// If the download is active and has a GID, try to remove it from aria2
		if (download.Status == core.StatusDownloading ||
			download.Status == core.StatusPending ||
			download.Status == core.StatusSubmitted) &&
			download.Gid != nil {

			err := d.aria2Client.Remove(*download.Gid)
			if err != nil {
				log.Printf("Error removing download from aria2: %v", err)
			} else {
				log.Printf("Removed active download from aria2: GID=%s", *download.Gid)
			}
		}

		// Delete the download record
		if err := d.db.Delete(&download).Error; err != nil {
			log.Printf("Error deleting download: %v", err)
		}
	}

	log.Printf("Cleanup completed, deleted %d old downloads", len(downloads))
}

func (d *Aria2DownloadDaemon) handleDaemonMessage(msg DaemonMessage, downloadRoutineChan chan HandlerAction) {
	log.Printf("Handling daemon message: %v", msg)
	switch msg.Action {
	case "delete":
		log.Printf("Forwarding delete message to download handler for ID: %d", msg.DownloadID)
		downloadRoutineChan <- HandlerAction{
			DownloadID: msg.DownloadID,
			Action:     "delete",
		}
	}
}

// Start launches the daemon
func (d *Aria2DownloadDaemon) Start() error {
	if d.running {
		return fmt.Errorf("daemon is already running")
	}

	// Start aria2c
	if err := d.startAria2c(); err != nil {
		return fmt.Errorf("failed to start aria2c: %w", err)
	}

	// Start work processor
	d.workProcessor.Start()

	d.running = true

	// Start main daemon loop in a goroutine
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		lastCleanupTime := time.Now()

		// download routine channel map - stores a separate channel for each download
		downloadRoutineChanMap := make(map[int]chan HandlerAction)

		completionChan := make(chan CompletionMessage)

		for d.running {
			// Get database connection
			db := d.db

			// Use a non-blocking select with timeout to process one message at a time
			// This prevents the loop from getting stuck on channel operations
			select {
			case msg := <-completionChan:
				log.Printf("Received completion message: %v", msg)
				delete(downloadRoutineChanMap, msg.DownloadID)
			case msg := <-d.daemonChan:
				log.Printf("Received daemon message: %v", msg)
				downloadHandlerChan, ok := downloadRoutineChanMap[msg.DownloadID]
				log.Printf("Download handler channel: %v, ok: %v", downloadHandlerChan, ok)
				if !ok {
					log.Printf("Download handler channel not found for download ID: %d. Assuming that download is already finished.", msg.DownloadID)
					d.serverChan <- ServerMessage{
						DownloadID: msg.DownloadID,
						Status:     0,
					}
					log.Printf("Sent server message for download ID: %d", msg.DownloadID)
					continue
				}
				log.Printf("Forwarding delete message to download handler for ID: %d", msg.DownloadID)
				d.handleDaemonMessage(msg, downloadHandlerChan)
				log.Printf("Message sent to download handler for ID: %d", msg.DownloadID)
			default:
				// Non-blocking, continue with the loop
			}

			var pendingDownloads []core.Download
			if err := db.Where("status = ?", core.StatusPending).Find(&pendingDownloads).Error; err != nil {
				log.Printf("Error querying pending downloads: %v", err)
				time.Sleep(DAEMON_SLEEP)
				continue
			}

			// Get daemon settings for concurrency
			var settings core.DaemonSettings
			if err := db.First(&settings, 1).Error; err != nil {
				log.Printf("Error querying daemon settings: %v", err)
				settings.Concurrency = d.config.Concurrency
			}

			// Get number of active downloads from aria2
			activeCount := 0
			stats, err := d.aria2Client.GetGlobalStat()
			if err != nil {
				log.Printf("Error getting aria2 stats: %v", err)
			} else {
				if numActive, ok := stats["numActive"].(string); ok {
					activeCount, _ = strconv.Atoi(numActive)
				}
			}

			// Calculate available slots
			availableSlots := settings.Concurrency - activeCount
			if availableSlots < 0 {
				availableSlots = 0
			}

			// Start new downloads
			for i, download := range pendingDownloads {
				if i >= availableSlots {
					break
				}

				// Update download status to submitted
				download.Status = core.StatusSubmitted
				if err := db.Save(&download).Error; err != nil {
					log.Printf("Error updating download status: %v", err)
					continue
				}

				// Create a dedicated channel for this download
				downloadHandlerChan := make(chan HandlerAction)
				downloadRoutineChanMap[int(download.ID)] = downloadHandlerChan

				// Start download in a separate goroutine with its own dedicated channel
				go d.handleDownloadWithAria2(download.ID, download.URL, download.Directory,
					d.config.TemporaryDownloadDirectory, d.config.Mapper, d.config.Aria2.DownloadOptions, downloadHandlerChan, completionChan)

				var group core.Group
				db.First(&group, download.GroupID)
				if group.Task != nil && *group.Task == core.TaskTypeUnpack {
					createTaskIfNotExists(db, group.ID, core.TaskTypeUnpack)
				}

				if d.config.Organize != "" {
					createTaskIfNotExists(db, group.ID, core.TaskTypeOrganize)
				}

				if d.config.TemporaryDownloadDirectory != "" {
					createTaskIfNotExists(db, group.ID, core.TaskTypeMove)
				}
			}

			// Periodically cleanup old downloads (once per hour)
			if time.Since(lastCleanupTime).Hours() >= 1 {
				log.Println("Running cleanup of old downloads...")
				d.cleanupOldDownloads()
				lastCleanupTime = time.Now()
			}

			// Add a small delay to prevent tight loop CPU usage
			time.Sleep(DAEMON_SLEEP)
		}
	}()

	return nil
}

// Stop shuts down the daemon
func (d *Aria2DownloadDaemon) Stop() error {
	if !d.running {
		return nil
	}

	d.running = false

	// Stop work processor
	d.workProcessor.Stop()
	d.workProcessor.Wait()

	// Cleanup aria2c
	d.cleanupAria2c()

	// Wait for main loop to exit
	d.wg.Wait()

	return nil
}

// createTaskIfNotExists creates a task if it doesn't exist already
// Returns the task (either existing or new) and whether it was created
func createTaskIfNotExists(db *gorm.DB, groupID uint, taskType core.TaskType) (*core.Task, bool, error) {
	// First try to find an existing task
	var task core.Task
	err := db.Where("group_id = ? AND task_type = ?", groupID, taskType).First(&task).Error

	// If found, return it
	if err == nil {
		return &task, false, nil
	}

	// If not found, create a new one
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Try direct creation first - this may fail due to unique constraint
		newTask := core.Task{
			TaskType: taskType,
			GroupID:  groupID,
			Status:   core.StatusPending,
		}

		err := db.Create(&newTask).Error
		if err == nil {
			return &newTask, true, nil
		}

		// If creation failed, check if it's due to a unique constraint
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			// Task was created by another process in between our check and creation
			// Try to get it again
			if err := db.Where("group_id = ? AND task_type = ?", groupID, taskType).First(&task).Error; err != nil {
				return nil, false, fmt.Errorf("error retrieving task after unique constraint: %w", err)
			}
			return &task, false, nil
		}

		// If it's another kind of error
		return nil, false, fmt.Errorf("error creating task: %w", err)
	}

	// Some other error occurred during initial lookup
	return nil, false, fmt.Errorf("error checking for existing task: %w", err)
}

// handleDownloadWithAria2 manages a single download with aria2
func (d *Aria2DownloadDaemon) handleDownloadWithAria2(downloadID uint, url, directory, tempDir string,
	mapper map[string]string, aria2Options map[string]string, commChan chan HandlerAction, completionChan chan CompletionMessage) {

	db := core.GetDB()
	aria2Client := NewAria2JsonRPC("")

	log.Printf("Starting aria2 download: %s to %s", url, directory)

	// Get the download record
	var download core.Download
	if err := db.First(&download, downloadID).Error; err != nil {
		log.Printf("Error getting download %d: %v", downloadID, err)
		return
	}

	var gid string
	downloadPath := directory
	var filename string

	// Try to start the download
	err := func() error {
		// Check if URL needs mapping
		for pattern, mapProgram := range mapper {
			if strings.Contains(url, pattern) {
				log.Printf("Mapping URL %s with %s", url, mapProgram)

				cmd := exec.Command("sh", "-c", fmt.Sprintf("%s %s", mapProgram, url))
				output, err := cmd.CombinedOutput()
				if err != nil {
					errorStr := fmt.Sprintf("URL mapping failed: %v - %s", err, string(output))
					return fmt.Errorf(errorStr)
				}

				url = strings.TrimSpace(string(output))
				log.Printf("URL mapped to %s", url)
				break
			}
		}

		// Use temporary directory if specified
		if tempDir != "" {
			downloadPath = tempDir
		}

		// Create group subdirectory
		downloadPath = filepath.Join(downloadPath, fmt.Sprintf("%d", download.GroupID))
		os.MkdirAll(downloadPath, 0755)

		// Prepare aria2 options
		options := map[string]string{
			"dir":      downloadPath,
			"continue": "true",
		}

		// Add additional options
		for k, v := range aria2Options {
			options[k] = v
		}

		// Add URL to aria2
		var err error
		gid, err = aria2Client.AddURI(url, options)
		if err != nil {
			return fmt.Errorf("failed to add download to aria2: %w", err)
		}

		return nil
	}()

	// Handle initial setup error
	if err != nil {
		errorStr := err.Error()
		download.Status = core.StatusFailed
		download.Error = &errorStr
		db.Save(&download)
		log.Printf("Download setup failed: %v", err)
		return
	}

	// Update download with GID and status
	download.Status = core.StatusDownloading
	download.Error = nil
	download.Gid = &gid
	db.Save(&download)

	// Monitor the download progress
	completed := false
	startTime := time.Now()

	for !completed {
		time.Sleep(DOWNLOAD_HANDLER_SLEEP)

		select {
		case msg := <-commChan:
			switch msg.Action {
			case "delete":
				log.Printf("Received delete message for download ID: %d", msg.DownloadID)
				completed = true
				log.Printf("Removing download from aria2: %s", gid)
				aria2Client.Remove(gid)
				log.Printf("Download removed from aria2: %s", gid)
				download.Status = core.StatusFailed // Doesn't matter since we're deleting
				log.Printf("Setting download status to failed: %d", download.ID)
				log.Printf("Sending completion message for download ID: %d", download.ID)
				completionChan <- CompletionMessage{
					DownloadID: int(download.ID),
					Status:     0,
				}
				log.Printf("Sent completion message for download ID: %d", download.ID)
				d.serverChan <- ServerMessage{
					DownloadID: int(download.ID),
					Status:     0,
				}
				log.Printf("Sent server message for download ID: %d", download.ID)
				return
			}
		default:
		}

		// Get download status
		status, err := aria2Client.GetStatus(gid)
		if err != nil {
			log.Printf("Failed to get status for GID %s: %v", gid, err)

			errorStr := fmt.Sprintf("Failed to get download status: %v", err)
			download.Status = core.StatusFailed
			download.Error = &errorStr
			db.Save(&download)
			return
		}

		// Check download status
		currentStatus, _ := status["status"].(string)
		currentStatus = strings.ToLower(currentStatus)

		// Extract filename if available
		if filename == "" {
			if files, ok := status["files"].([]interface{}); ok && len(files) > 0 {
				if file, ok := files[0].(map[string]interface{}); ok {
					if path, ok := file["path"].(string); ok && path != "" {
						filename = filepath.Base(path)
					}
				}
			}
		}

		// Update progress information
		totalLength, _ := strconv.ParseInt(status["totalLength"].(string), 10, 64)
		completedLength, _ := strconv.ParseInt(status["completedLength"].(string), 10, 64)
		downloadSpeed, _ := strconv.ParseInt(status["downloadSpeed"].(string), 10, 64)

		if totalLength > 0 {
			progress := float64(completedLength) / float64(totalLength) * 100
			progressStr := fmt.Sprintf("%.1f%%", progress)
			download.Progress = &progressStr
		}

		downloaded := int(completedLength)
		totalSize := int(totalLength)
		download.Downloaded = &downloaded
		download.TotalSize = &totalSize

		if downloadSpeed > 0 {
			// Store raw speed in bytes/sec
			speedStr := fmt.Sprintf("%d", downloadSpeed)
			download.Speed = &speedStr
		}

		// Save progress update to database
		db.Save(&download)

		// Handle completion
		if currentStatus == "complete" {
			download.Status = core.StatusDownloaded

			// Update average speed for completed download
			totalTime := time.Since(startTime).Seconds()
			if totalTime > 0 && totalLength > 0 {
				// Store raw average speed in bytes/sec
				avgSpeed := int64(float64(totalLength) / totalTime)
				speedStr := fmt.Sprintf("%d", avgSpeed)
				download.Speed = &speedStr
			}

			db.Save(&download)
			completed = true

		} else if currentStatus == "error" {
			errorMsg := "Unknown error"
			if msg, ok := status["errorMessage"].(string); ok {
				errorMsg = msg
			}

			download.Error = &errorMsg
			download.Status = core.StatusFailed
			db.Save(&download)
			completed = true

		} else if time.Since(startTime) > 24*time.Hour {
			// Download timeout after 24 hours
			errorMsg := "Download timed out"
			download.Error = &errorMsg
			download.Status = core.StatusFailed
			db.Save(&download)
			completed = true

			// Try to remove from aria2
			if err := aria2Client.Remove(gid); err != nil {
				log.Printf("Error removing timed out download: %v", err)
			}
		}
	}

	// Determine the status code based on download.Status
	var statusCode int
	switch download.Status {
	case core.StatusDownloaded:
		statusCode = 0
	case core.StatusFailed:
		statusCode = 1
	default:
		statusCode = 2
	}

	completionChan <- CompletionMessage{
		DownloadID: int(download.ID),
		Status:     statusCode,
	}

	log.Printf("Download %s: %s", download.Status, url)
}
