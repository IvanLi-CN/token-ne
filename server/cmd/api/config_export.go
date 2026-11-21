package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/cores"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

func exportConfig(args []string) error {
	if len(args) == 0 || args[0] != "export" {
		return errors.New("expected config export --instance <id> [--out <path>]")
	}
	args = args[1:]
	var instance, output string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--instance":
			i++
			if i >= len(args) {
				return errors.New("--instance requires a UUID")
			}
			instance = args[i]
		case "--out":
			i++
			if i >= len(args) {
				return errors.New("--out requires a path")
			}
			output = args[i]
		default:
			return errors.New("unknown config export option")
		}
	}
	if !cores.UUID(instance, false) {
		return errors.New("--instance requires a canonical UUID")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer repo.Close()
	token := os.Getenv("CORE_CONTROL_TOKEN")
	snapshots := &cores.Snapshots{DB: repo.Pool, Master: cfg.MasterKey, Token: token,
		SigningKey: os.Getenv("CORE_CONFIG_SIGNING_KEY"), SignatureMode: os.Getenv("CORE_CONFIG_SIGNATURE_MODE"),
		BaseURL: "http://api:8080", PolicyPath: os.Getenv("STATUS_REWRITE_CONFIG")}
	snapshot, err := snapshots.Build(ctx, instance, true)
	if err != nil {
		return err
	}
	body := snapshot.Body
	actor := os.Getenv("ADMIN_USERNAME")
	if actor == "" {
		actor = "local"
	}
	_, err = repo.Pool.Exec(ctx, `insert into audit_events(id,actor,action,target_type,target_id,payload) values($1,$2,'core.config.export','core_instance',$3,$4)`,
		uuid.NewString(), actor, instance, map[string]any{"sha256": cores.Hash(body)})
	if err != nil {
		return err
	}
	if output == "" {
		_, err = os.Stdout.Write(body)
		if err == nil {
			_, err = os.Stdout.Write([]byte("\n"))
		}
		return err
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(body)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(output)
	}
	return err
}
