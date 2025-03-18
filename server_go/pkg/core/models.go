package core

import (
	"log"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TaskType represents the type of task to perform
type TaskType string

// Status represents the status of a download
type Status string

// GroupStatus represents the status of a group
type GroupStatus string

const (
	// Task types
	TaskTypeUnpack TaskType = "unpack"
	TaskTypeMove   TaskType = "move"

	// Status values
	StatusPending     Status = "pending"
	StatusSubmitted   Status = "submitted"
	StatusDownloading Status = "downloading"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusMoving      Status = "moving"
	StatusUnpacking   Status = "unpacking"

	// GroupStatus values
	GroupStatusPending   GroupStatus = "pending"
	GroupStatusCompleted GroupStatus = "completed"
	GroupStatusFailed    GroupStatus = "failed"
	GroupStatusUnpacking GroupStatus = "unpacking"
)

// Map task types to their corresponding statuses
var TaskTypeToStatus = map[TaskType]Status{
	TaskTypeUnpack: StatusUnpacking,
	TaskTypeMove:   StatusMoving,
}

// Map statuses back to task types
var StatusToTaskType = map[Status]TaskType{
	StatusUnpacking: TaskTypeUnpack,
	StatusMoving:    TaskTypeMove,
}

// Group represents a group of downloads with a common task
type Group struct {
	ID        uint        `gorm:"primaryKey" json:"id"`
	Task      *TaskType   `json:"task"`
	Status    GroupStatus `gorm:"not null" json:"status"`
	Error     *string     `json:"error"`
	Downloads []Download  `gorm:"foreignKey:GroupID" json:"downloads,omitempty"`
	Tasks     []Task      `gorm:"foreignKey:GroupID" json:"tasks,omitempty"`
}

// Task represents a task to be performed on a group of downloads
type Task struct {
	ID       uint     `gorm:"primaryKey" json:"id"`
	TaskType TaskType `gorm:"not null" json:"task_type"`
	GroupID  uint     `gorm:"index:idx_group_task_type,unique" json:"group_id"`
	Status   Status   `gorm:"not null" json:"status"`
	Error    *string  `json:"error"`
	Group    Group    `gorm:"foreignKey:GroupID" json:"-"`
}

// Download represents a download record
type Download struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	URL        string    `gorm:"not null" json:"url"`
	Directory  string    `gorm:"not null" json:"directory"`
	Status     Status    `gorm:"not null" json:"status"`
	Speed      *string   `json:"speed"`
	Progress   *string   `json:"progress"`
	Downloaded *int      `json:"downloaded"`
	TotalSize  *int      `json:"total_size"`
	Error      *string   `json:"error"`
	DateAdded  time.Time `gorm:"autoCreateTime" json:"date_added"`
	Gid        *string   `json:"gid"`
	GroupID    uint      `gorm:"index" json:"group_id"`
	Group      Group     `gorm:"foreignKey:GroupID" json:"-"`
}

// DaemonSettings represents global daemon settings
type DaemonSettings struct {
	ID          uint `gorm:"primaryKey" json:"id"`
	Concurrency int  `gorm:"default:1" json:"concurrency"`
}

var db *gorm.DB

// InitDB initializes the database connection and creates tables
func InitDB(dbPath string) error {
	var err error

	// Configure GORM to log errors
	gormConfig := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Error),
	}

	// Connect to the SQLite database
	db, err = gorm.Open(sqlite.Open(dbPath), gormConfig)
	if err != nil {
		return err
	}

	// Create tables automatically
	err = db.AutoMigrate(&Group{}, &Task{}, &Download{}, &DaemonSettings{})
	if err != nil {
		return err
	}

	// Check for daemon settings record and create if it doesn't exist
	var count int64
	db.Model(&DaemonSettings{}).Count(&count)
	if count == 0 {
		db.Create(&DaemonSettings{ID: 1, Concurrency: 1})
	}

	log.Printf("Database initialized at %s", dbPath)
	return nil
}

// GetDB returns the database connection
func GetDB() *gorm.DB {
	return db
}

// Transaction wraps a database operation in a transaction
func Transaction(fn func(tx *gorm.DB) error) error {
	return db.Transaction(fn)
}
