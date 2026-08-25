package users

import "context"

type UserRepository interface {
	GetByEmail(ctx context.Context, email string) (User, PasswordHash, error)
	GetByID(ctx context.Context, id string) (User, error)
	Create(ctx context.Context, u User, hash PasswordHash) (User, error)
	FindByProviderID(ctx context.Context, provider, providerID string) (OAuthAccount, User, error)
	CreateOAuthAccount(ctx context.Context, provider, providerID, email string, user User) (OAuthAccount, User, error)
	List(ctx context.Context, q string, limit, offset int) ([]User, int, error)
	UpdateProfile(ctx context.Context, id, name, surname string) (User, error)
	Delete(ctx context.Context, id string) error
}

type AuthService interface {
	Register(ctx context.Context, email, password, name, surname string) (User, error)
	Login(ctx context.Context, email, password string) (AccessToken, RefreshToken, error)
	OAuthLogin(ctx context.Context, provider, providerID, email, name, surname string) (User, AccessToken, RefreshToken, error)
	Refresh(ctx context.Context, refresh RefreshToken) (AccessToken, RefreshToken, error)
}

type UsersService interface {
	GetMe(ctx context.Context, userID string) (User, error)
	UpdateMe(ctx context.Context, userID, name, surname string) (User, error)
}

type (
	AccessToken  string
	RefreshToken string
)
