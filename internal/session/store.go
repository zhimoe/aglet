package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/zhimoe/aglet/internal/config"
)

// Store owns the SQLite session service.
type Store struct {
	Service adksession.Service
	DB      *gorm.DB
	AppName string
	UserID  string
}

// Open creates the session DB and runs migrations.
func Open(dbPath, appName, userID string) (*Store, error) {
	if appName == "" {
		appName = config.AppName
	}
	if userID == "" {
		userID = "local"
	}
	path := config.ExpandPath(dbPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open session db: %w", err)
	}
	svc, err := database.NewSessionServiceFromDB(db)
	if err != nil {
		return nil, fmt.Errorf("create session service: %w", err)
	}
	if err := database.AutoMigrate(svc); err != nil {
		return nil, fmt.Errorf("migrate session db: %w", err)
	}
	return &Store{Service: svc, DB: db, AppName: appName, UserID: userID}, nil
}

// Ensure returns an existing session ID or creates a new session (with the
// provided ID if non-empty).
func (s *Store) Ensure(ctx context.Context, id string) (string, error) {
	if id != "" {
		_, err := s.Service.Get(ctx, &adksession.GetRequest{
			AppName: s.AppName, UserID: s.UserID, SessionID: id,
		})
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, adksession.ErrNotFound) {
			return "", err
		}
	}
	created, err := s.Service.Create(ctx, &adksession.CreateRequest{
		AppName: s.AppName, UserID: s.UserID, SessionID: id,
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return created.Session.ID(), nil
}

func (s *Store) Get(ctx context.Context, id string) (adksession.Session, error) {
	res, err := s.Service.Get(ctx, &adksession.GetRequest{
		AppName: s.AppName, UserID: s.UserID, SessionID: id,
	})
	if err != nil {
		return nil, err
	}
	return res.Session, nil
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	sqlDB, err := s.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
