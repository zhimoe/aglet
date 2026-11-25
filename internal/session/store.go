package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/zhimoe/aglet/internal/config"
)

// Info is a lightweight session listing entry.
type Info struct {
	ID        string
	UpdatedAt time.Time
	Events    int
	Preview   string
}

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

// List returns sessions ordered oldest to newest (ADK's ordering).
func (s *Store) List(ctx context.Context) ([]Info, error) {
	res, err := s.Service.List(ctx, &adksession.ListRequest{AppName: s.AppName, UserID: s.UserID})
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(res.Sessions))
	for _, sess := range res.Sessions {
		info := Info{
			ID:        sess.ID(),
			UpdatedAt: sess.LastUpdateTime(),
		}
		// ADK's database List does not load events, so fetch the full session
		// to compute the event count and preview.
		if full, err := s.Get(ctx, info.ID); err == nil {
			info.Events = full.Events().Len()
			var preview string
			for ev := range full.Events().All() {
				if ev.Author != "user" || ev.Content == nil {
					continue
				}
				for _, p := range ev.Content.Parts {
					if p != nil && p.Text != "" {
						preview = p.Text
						break
					}
				}
				if preview != "" {
					break
				}
			}
			preview = strings.Join(strings.Fields(preview), " ")
			info.Preview = truncateRunes(preview, 100)
		}
		out = append(out, info)
	}
	return out, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
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
