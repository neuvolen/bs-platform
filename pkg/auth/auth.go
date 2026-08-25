package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

func CheckPassword(hash, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

type Manager struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func (m *Manager) Secret() []byte {
	return m.secret
}

func NewManager(secret string, accessTTL, refreshTTL time.Duration) *Manager {
	return &Manager{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

func (m *Manager) GenerateTokens(userID, role string, perms []string) (access, refresh string, err error) {
	now := time.Now()

	// Access
	ac := jwt.MapClaims{
		"sub":   userID,
		"role":  role,
		"perms": perms,
		"typ":   "access",
		"iat":   now.Unix(),
		"exp":   now.Add(m.accessTTL).Unix(),
	}
	at := jwt.NewWithClaims(jwt.SigningMethodHS256, ac)
	access, err = at.SignedString(m.secret)
	if err != nil {
		return "", "", err
	}

	// Refresh
	rc := jwt.MapClaims{
		"sub": userID,
		"typ": "refresh",
		"iat": now.Unix(),
		"exp": now.Add(m.refreshTTL).Unix(),
	}
	rt := jwt.NewWithClaims(jwt.SigningMethodHS256, rc)
	refresh, err = rt.SignedString(m.secret)
	if err != nil {
		return "", "", err
	}

	return access, refresh, nil
}


