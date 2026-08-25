package organs

import "context"

type Repository interface {
	List(ctx context.Context) ([]Organ, error)
	GetByID(ctx context.Context, id string) (Organ, error)
	Create(ctx context.Context, o Organ) (Organ, error)
	Update(ctx context.Context, id string, o Organ) (Organ, error)
	Delete(ctx context.Context, id string) error
}

type Service interface {
	List(ctx context.Context) ([]Organ, error)
	Get(ctx context.Context, id string) (Organ, error)
	Create(ctx context.Context, o Organ) (Organ, error)
	Update(ctx context.Context, id string, o Organ) (Organ, error)
	Delete(ctx context.Context, id string) error
}
