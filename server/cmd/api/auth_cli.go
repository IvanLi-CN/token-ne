package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/authsettings"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

func authLocalLogin(args []string) error {
	if len(args) != 2 || args[0] != "local-login" || args[1] != "enable" {
		return errors.New("usage: api auth local-login enable")
	}
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, e := store.Open(ctx, cfg.DatabaseURL)
	if e != nil {
		return e
	}
	defer repo.Close()
	if e = repo.Bootstrap(ctx, cfg.AdminUsername, cfg.AdminPassword); e != nil {
		return e
	}
	admin, e := repo.AdminByUsername(ctx, cfg.AdminUsername)
	if e != nil {
		return fmt.Errorf("administrator %q: %w", cfg.AdminUsername, e)
	}
	payload, _ := json.Marshal(map[string]string{"source": "cli"})
	audit := store.Audit{
		ID:         uuid.NewString(),
		Actor:      "cli",
		Action:     "auth.local_login.enable",
		TargetType: "admin",
		TargetID:   &admin.ID,
		Payload:    payload,
	}
	if e = authsettings.EnableLocalLogin(ctx, repo, cfg, admin.ID, audit); e != nil {
		return e
	}
	fmt.Println("local login enabled")
	return nil
}
