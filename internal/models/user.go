package models

import (
	"time"

	"gorm.io/gorm"
)

type User struct {
	ID        string    `gorm:"primaryKey;type:varchar(191)" json:"id"` // varchar(191) for UUID/CUID compatibility
	Email     string    `gorm:"column:email;uniqueIndex;not null" json:"email"`
	Password  string    `gorm:"column:password;not null" json:"-"` // Don't return password in JSON
	Name      string    `gorm:"column:name" json:"name"`
	CreatedAt time.Time `gorm:"column:createdAt" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updatedAt" json:"updatedAt"`
	// We can keep DeletedAt mapped to deleted_at if we want, or map it to nothing if checking against schema.
	// The schema has 'deleted_at' (created by GORM earlier) but original schema didn't have soft delete?
	// Let's use 'deleted_at' since it was added by GORM and seems to be used.
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}
