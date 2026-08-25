package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/rbac"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/bnursik/business_surgery_backend/pkg/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type service struct {
	repo     users.UserRepository
	rbacRepo rbac.Repository
	jwt      *auth.Manager
}

func New(repo users.UserRepository, rbacRepo rbac.Repository, jwt *auth.Manager) users.AuthService {
	return &service{repo: repo, rbacRepo: rbacRepo, jwt: jwt}
}

func (s *service) Register(ctx context.Context, email, password, name, surname string) (users.User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	name = strings.TrimSpace(name)
	surname = strings.TrimSpace(surname)

	if name == "" || surname == "" || email == "" || password == "" {
		return users.User{}, users.ErrInvalidArgument
	}

	_, _, err := s.repo.GetByEmail(ctx, email)
	if err == nil {
		return users.User{}, users.ErrAlreadyExists
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return users.User{}, err
	}

	u := users.User{
		Email:     email,
		Name:      name,
		Surname:   surname,
		Role:      users.RoleParticipant,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	created, err := s.repo.Create(ctx, u, users.PasswordHash(hash))
	if err != nil {
		return users.User{}, err
	}

	if err := s.rbacRepo.AssignRoleByCode(ctx, created.ID.String(), string(users.RoleParticipant)); err != nil {
		return users.User{}, err
	}

	return created, nil
}

func (s *service) Login(ctx context.Context, email, password string) (users.AccessToken, users.RefreshToken, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return  "", "", users.ErrInvalidArgument
	}

	u, hash, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		return "", "", users.ErrUnauthorized
	}

	if !auth.CheckPassword(string(hash), password) {
		return "", "", users.ErrUnauthorized
	}

	perms, err := s.rbacRepo.GetUserPermissions(ctx, u.ID.String())
	if err != nil {
		return "", "", err
	}

	access, refresh, err := s.jwt.GenerateTokens(u.ID.String(), string(u.Role), perms)
	if err != nil {
		return "", "", err
	}

	return users.AccessToken(access), users.RefreshToken(refresh), nil
}

func (s *service) OAuthLogin(ctx context.Context, provider, providerID, email, name, surname string) (users.User, users.AccessToken, users.RefreshToken, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	providerID = strings.TrimSpace(providerID)
	email = strings.TrimSpace(strings.ToLower(email))
	name = strings.TrimSpace(name)
	surname = strings.TrimSpace(surname)

	if provider == "" || providerID == "" {
		return users.User{}, "", "", users.ErrInvalidArgument
	}

	_, u, err := s.repo.FindByProviderID(ctx, provider, providerID)
	if err == nil {
		perms, pErr := s.rbacRepo.GetUserPermissions(ctx, u.ID.String())
		if pErr != nil {
			return users.User{}, "", "", pErr
		}

		access, refresh, tokErr := s.jwt.GenerateTokens(u.ID.String(), string(u.Role), perms)
		if tokErr != nil {
			return users.User{}, "", "", tokErr
		}
		return u, users.AccessToken(access), users.RefreshToken(refresh), nil
	}
	if !errors.Is(err, users.ErrNotFound) {
		return users.User{}, "", "", err
	}

	var baseUser users.User
	if email != "" {
		if existing, _, getErr := s.repo.GetByEmail(ctx, email); getErr == nil {
			baseUser = existing
		} else if !errors.Is(getErr, users.ErrNotFound) {
			return users.User{}, "", "", getErr
		}
	}

	if baseUser.ID == uuid.Nil {
		if email == "" {
			return users.User{}, "", "", users.ErrInvalidArgument
		}

		pw, pwErr := randomPassword()
		if pwErr != nil {
			return users.User{}, "", "", pwErr
		}

		hash, hashErr := auth.HashPassword(pw)
		if hashErr != nil {
			return users.User{}, "", "", hashErr
		}

		baseUser = users.User{
			Email:     email,
			Name:      name,
			Surname:   surname,
			Role:      users.RoleParticipant,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		created, createErr := s.repo.Create(ctx, baseUser, users.PasswordHash(hash))
		if createErr != nil {
			return users.User{}, "", "", createErr
		}
		baseUser = created

		if err := s.rbacRepo.AssignRoleByCode(ctx, baseUser.ID.String(), string(users.RoleParticipant)); err != nil {
			return users.User{}, "", "", err
		}
	}

	_, u, err = s.repo.CreateOAuthAccount(ctx, provider, providerID, email, baseUser)
	if err != nil {
		if errors.Is(err, users.ErrAlreadyExists) {
			_, u, err = s.repo.FindByProviderID(ctx, provider, providerID)
		}
		if err != nil {
			return users.User{}, "", "", err
		}
	}

	perms, err := s.rbacRepo.GetUserPermissions(ctx, u.ID.String())
	if err != nil {
		return users.User{}, "", "", err
	}

	access, refresh, err := s.jwt.GenerateTokens(u.ID.String(), string(u.Role), perms)
	if err != nil {
		return users.User{}, "", "", err
	}

	return u, users.AccessToken(access), users.RefreshToken(refresh), nil
}

func (s *service) Refresh(ctx context.Context, refresh users.RefreshToken) (users.AccessToken, users.RefreshToken, error) {
	tkn, err := jwt.Parse(string(refresh), func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, users.ErrUnauthorized
		}
		return s.jwt.Secret(), nil
	})
	if err != nil || !tkn.Valid {
		return "", "", users.ErrUnauthorized
	}

	claims, ok := tkn.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", users.ErrUnauthorized
	}

	if typ, _ := claims["typ"].(string); typ != "refresh" {
		return "", "", users.ErrUnauthorized
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", "", users.ErrUnauthorized
	}

	u, err := s.repo.GetByID(ctx, sub)
	if err != nil {
		return "", "", users.ErrUnauthorized
	}

	perms, err := s.rbacRepo.GetUserPermissions(ctx, u.ID.String())
	if err != nil {
		return "", "", err
	}

	access, newRefresh, err := s.jwt.GenerateTokens(u.ID.String(), string(u.Role), perms)
	if err != nil {
		return "", "", err
	}

	return users.AccessToken(access), users.RefreshToken(newRefresh), nil
}

func randomPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
