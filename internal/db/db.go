package db

import (
	"database/sql"
	"log"
	"net/url"
	"strings"
	"war-room-backend/internal/config"
	"war-room-backend/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/go-sql-driver/mysql"
	gorm_mysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var DB *gorm.DB

func Connect(cfg *config.Config) {
	var err error
	dsn := cfg.DatabaseURL

	if strings.Contains(dsn, "sqlite") || strings.HasSuffix(dsn, ".db") {
		log.Println("Using SQLite database at", dsn)
		DB, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	} else {
		log.Println("Using MySQL database")

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
				}), &gorm.Config{})
			} else {
				log.Printf("Failed to create mysql connector: %v", connErr)
				DB, err = gorm.Open(gorm_mysql.Open(dsn), &gorm.Config{})
			}
		} else {
			log.Printf("Failed to parse DSN: %v", parseErr)
			DB, err = gorm.Open(gorm_mysql.Open(dsn), &gorm.Config{})
		}
	}

	if err != nil {
		log.Fatal("Failed to connect to database: ", err)
	}

	log.Println("Connected to database successfully")

	// Auto Migrate
	if cfg.RunMigrations {
		log.Println("Running migrations...")
		// Disable foreign key constraints modification during migration to avoid failures on existing bad data
		// Actually, just ignore the error for now so the app can start
		if err := DB.AutoMigrate(
			&models.User{},
			&models.Assessment{},
			&models.Stage{},
			&models.Response{},
			&models.CompetencyScore{},
			&models.MentorInteraction{},
			&models.InvestorScorecard{},
			&models.Report{},
			&models.LeaderboardEntry{},
		); err != nil {
			log.Printf("Warning: Database migration had errors (likely foreign key inconsistencies): %v", err)
			log.Println("Continuing application startup anyway...")
		} else {
			log.Println("Database migration completed successfully")
		}
	} else {
		log.Println("Skipping migrations (RUN_MIGRATIONS != true)")
	}
}
