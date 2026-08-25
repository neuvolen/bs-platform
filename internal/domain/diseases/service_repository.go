package diseases

import "context"

type Repository interface {
	List(ctx context.Context, organID, categoryID string) ([]Disease, error)
	GetByID(ctx context.Context, id string) (Disease, error)
	Create(ctx context.Context, d Disease) (Disease, error)
	Update(ctx context.Context, id string, d Disease) (Disease, error)
	Delete(ctx context.Context, id string) error
	ListAssignedDiseaseIDs(ctx context.Context, userID string) ([]string, error)
}

type Service interface {
	List(ctx context.Context, organID, categoryID string) ([]Disease, error)
	Get(ctx context.Context, id string) (Disease, error)
	Create(ctx context.Context, d Disease) (Disease, error)
	Update(ctx context.Context, id string, d Disease) (Disease, error)
	Delete(ctx context.Context, id string) error
	ListAssignedDiseaseIDs(ctx context.Context, userID string) (map[string]struct{}, error)
}
