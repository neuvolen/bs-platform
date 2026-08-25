package diseasecategories

import "context"

type Repository interface {
	List(ctx context.Context) ([]Category, error)
	GetByID(ctx context.Context, id string) (Category, error)
	Create(ctx context.Context, c Category) (Category, error)
	Update(ctx context.Context, id string, c Category) (Category, error)
	Delete(ctx context.Context, id string) error
}

type Service interface {
	List(ctx context.Context) ([]Category, error)
	Get(ctx context.Context, id string) (Category, error)
	Create(ctx context.Context, c Category) (Category, error)
	Update(ctx context.Context, id string, c Category) (Category, error)
	Delete(ctx context.Context, id string) error
}
