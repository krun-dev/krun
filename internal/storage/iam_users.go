package storage

import (
	"context"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func writeUsers(ctx context.Context, tx ormTransaction, before, after map[string]domain.User) error {
	for id, user := range after {
		previous, existed := before[id]
		if existed && previous == user {
			continue
		}
		row := models.User{UserID: id, Source: user.Source, Subject: user.Subject, Name: user.Name, Email: user.Email, EmailVerified: user.EmailVerified, Status: user.Status, AuthVersion: user.AuthVersion}
		if !existed {
			query := tx.orm.NewInsert().Model(&row).Column("user_id", "source", "subject", "name", "email", "email_verified", "status", "auth_version").On("CONFLICT DO NOTHING").Returning("NULL")
			if err := ormCAS(ctx, "Identity was created concurrently", query); err != nil {
				return err
			}
			continue
		}
		query := tx.orm.NewUpdate().Model(&row).Column("source", "subject", "name", "email", "email_verified", "status", "auth_version", "updated_at").Value("updated_at", "now()").WherePK().
			Where("auth_version = ?", previous.AuthVersion).Where("source = ?", previous.Source).Where("subject = ?", previous.Subject).Where("name = ?", previous.Name).Where("email = ?", previous.Email).Where("email_verified = ?", previous.EmailVerified).Where("status = ?", previous.Status)
		if err := ormCAS(ctx, "User changed", query); err != nil {
			return err
		}
	}
	for id, previous := range before {
		if _, exists := after[id]; exists {
			continue
		}
		row := models.User{UserID: id}
		query := tx.orm.NewDelete().Model(&row).WherePK().Where("auth_version = ?", previous.AuthVersion).Where("source = ?", previous.Source).Where("subject = ?", previous.Subject).Where("name = ?", previous.Name).Where("email = ?", previous.Email).Where("email_verified = ?", previous.EmailVerified).Where("status = ?", previous.Status)
		if err := ormCAS(ctx, "User changed", query); err != nil {
			return err
		}
	}
	return nil
}
