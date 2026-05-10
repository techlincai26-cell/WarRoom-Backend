package db

import (
	"database/sql"
	"log"
	"net/url"
	"os"
	"strings"
	"time"
	"war-room-backend/internal/config"
	"war-room-backend/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	gorm_mysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// EnsureAssessmentCompatibilitySchema repairs legacy assessment-flow tables so
// older databases can still accept new assessment creates.
func EnsureAssessmentCompatibilitySchema() error {
	return DB.AutoMigrate(
		&models.Assessment{},
		&models.Stage{},
		&models.CompetencyScore{},
	)
}

func Connect(cfg *config.Config) {
	var err error
	dsn := cfg.DatabaseURL

	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
		logger.Config{
			SlowThreshold:             200 * time.Millisecond, // Lower threshold to catch more slow queries
			LogLevel:                  logger.Info,            // Set to Info to see SQL queries (or use DB.Debug())
			IgnoreRecordNotFoundError: true,                   // Ignore ErrRecordNotFound error for logger
			Colorful:                  true,
		},
	)

	if strings.Contains(dsn, "sqlite") || strings.HasSuffix(dsn, ".db") {
		log.Println("Using SQLite database at", dsn)
		DB, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: newLogger})
	} else {
		log.Println("Using MySQL database")

		// Append critical timeouts if not present
		if !strings.Contains(dsn, "timeout=") {
			if strings.Contains(dsn, "?") {
				dsn += "&timeout=10s&readTimeout=30s&writeTimeout=30s"
			} else {
				dsn += "?parseTime=true&timeout=10s&readTimeout=30s&writeTimeout=30s"
			}
		}

		// Parse DSN to handle encoded passwords
		vals, parseErr := mysql.ParseDSN(dsn)
		if parseErr == nil {
			if strings.Contains(vals.Passwd, "%") {
				decoded, err := url.QueryUnescape(vals.Passwd)
				if err == nil {
					vals.Passwd = decoded
				}
			}
			// Use connector to avoid reprocessing DSN string with special chars
			connector, connErr := mysql.NewConnector(vals)
			if connErr == nil {
				sqlDB := sql.OpenDB(connector)
				DB, err = gorm.Open(gorm_mysql.New(gorm_mysql.Config{
					Conn: sqlDB,
				}), &gorm.Config{Logger: newLogger})
			} else {
				log.Printf("Failed to create mysql connector: %v", connErr)
				DB, err = gorm.Open(gorm_mysql.Open(dsn), &gorm.Config{Logger: newLogger})
			}
		} else {
			log.Printf("Failed to parse DSN: %v", parseErr)
			DB, err = gorm.Open(gorm_mysql.Open(dsn), &gorm.Config{Logger: newLogger})
		}
	}

	if err != nil {
		log.Fatalf("Failed to connect to primary database: %v", err)
	} else {
		log.Println("Connected to database successfully")
		// Enable global debug mode
		DB = DB.Debug()
	}

	if err := EnsureAssessmentCompatibilitySchema(); err != nil {
		log.Fatalf("Critical: assessment compatibility migration failed: %v", err)
	}

	// Configure connection pool
	if sqlDB, dbErr := DB.DB(); dbErr == nil {
		sqlDB.SetMaxIdleConns(5)  // Reduced as per user instruction
		sqlDB.SetMaxOpenConns(10) // Reduced as per user instruction
		sqlDB.SetConnMaxLifetime(3 * time.Minute)
		sqlDB.SetConnMaxIdleTime(2 * time.Minute) // Added as per user instruction
	} else {
		log.Printf("Warning: Failed to get underlying sqlDB to set pool limits: %v", dbErr)
	}

	// Auto Migrate
	if cfg.RunMigrations {
		log.Println("Running auto-migrations...")
		if err := DB.AutoMigrate(
			&models.User{},
			&models.Batch{},
			&models.Assessment{},
			&models.Stage{},
			&models.Response{},
			&models.PhaseScenario{},
			&models.DynamicScenario{},
			&models.CompetencyScore{},
			&models.MentorInteraction{},
			&models.InvestorScorecard{},
			&models.Report{},
			&models.LeaderboardEntry{},
		); err != nil {
			log.Fatalf("Critical: Database migration failed: %v", err)
		} else {
			log.Println("Database auto-migration completed successfully")
		}

		// Seed admin user if env vars are set
		seedAdmin()
	} else {
		log.Println("Skipping migrations (RUN_MIGRATIONS != true)")
	}
}

func seedAdmin() {
	email := os.Getenv("ADMIN_EMAIL")
	password := os.Getenv("ADMIN_PASSWORD")
	name := os.Getenv("ADMIN_NAME")
	if email == "" || password == "" {
		return
	}
	if name == "" {
		name = "Administrator"
	}

	var existing models.User
	err := DB.Where("email = ?", email).First(&existing).Error
	if err == nil {
		updates := make(map[string]interface{})
		if existing.Role != "admin" {
			updates["role"] = "admin"
		}

		// Always sync password if we're seeding/migrating
		hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err == nil {
			updates["password"] = string(hashed)
		}

		if len(updates) > 0 {
			if err := DB.Model(&existing).Updates(updates).Error; err != nil {
				log.Printf("Warning: failed to update admin user: %v", err)
			} else {
				log.Printf("Synced admin user %s credentials", email)
			}
		}
		return
	}
	if err != gorm.ErrRecordNotFound {
		log.Printf("Warning: could not check for admin user: %v", err)
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("Warning: could not hash admin password: %v", err)
		return
	}

	admin := models.User{
		ID:       uuid.New().String(),
		Email:    email,
		Password: string(hashed),
		Name:     name,
		Role:     "admin",
	}
	if err := DB.Create(&admin).Error; err != nil {
		log.Printf("Warning: could not create admin user: %v", err)
		return
	}
	log.Printf("Admin user created: %s", email)
}
